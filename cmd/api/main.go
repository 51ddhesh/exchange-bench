package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/51ddhesh/exchange-bench/internal/coordinator"
	"github.com/51ddhesh/exchange-bench/internal/compiler"
	"github.com/51ddhesh/exchange-bench/internal/runner"
	"github.com/51ddhesh/exchange-bench/internal/workload"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	proto "github.com/51ddhesh/exchange-bench/internal/coordinator/proto"
)

var validExtensions = map[string][]string{
	"cpp":    {".cpp", ".cc", ".cxx"},
	"rust":   {".rs"},
	"go":     {".go"},
	"python": {".py"},
	"zig":    {".zig"},
}

type jobState string

const (
	stateQueued  jobState = "queued"
	stateRunning jobState = "running"
	stateDone    jobState = "done"
	stateFailed  jobState = "failed"
)

type job struct {
	SubmissionID string
	TeamID       string
	Attempt      int64
	RunID        string
	Language     string
	SourcePath   string
	ArtifactPath string
}

type jobStatus struct {
	SubmissionID string   `json:"submission_id"`
	TeamID       string   `json:"team_id"`
	Attempt      int64    `json:"attempt"`
	RunID        string   `json:"run_id"`
	Language     string   `json:"language"`
	State        jobState `json:"status"`
	Error        string   `json:"error,omitempty"`

	TicksSent  int64   `json:"ticks_sent,omitempty"`
	TicksAcked int64   `json:"ticks_acked,omitempty"`
	PeakTPS    float64 `json:"peak_tps,omitempty"`
	P50Us      int64   `json:"p50_us,omitempty"`
	P90Us      int64   `json:"p90_us,omitempty"`
	P99Us      int64   `json:"p99_us,omitempty"`
	TimedOut   bool    `json:"timed_out,omitempty"`
}

type apiServer struct {
	baseCfg     coordinator.Config
	seed        int64
	ticks       int
	runTimeout  time.Duration
	seccompPath string
	db          *pgxpool.Pool // nil if --dsn not provided
	jobCh       chan job
	statuses    sync.Map
	attempts    sync.Map
	teamJobs    sync.Map
	mu          sync.Mutex

	s3Client    *s3.Client
	sqsClient   *sqs.Client
	s3Bucket    string
	sqsQueueUrl string
	localMode   bool
}

func newAPIServer(
	baseCfg coordinator.Config,
	seed int64,
	ticks int,
	runTimeout time.Duration,
	queueDepth int,
	seccompPath string,
	db *pgxpool.Pool,
	s3Client *s3.Client,
	sqsClient *sqs.Client,
	s3Bucket string,
	sqsQueueUrl string,
	localMode bool,
) *apiServer {
	return &apiServer{
		baseCfg:     baseCfg,
		seed:        seed,
		ticks:       ticks,
		runTimeout:  runTimeout,
		seccompPath: seccompPath,
		db:          db,
		jobCh:       make(chan job, queueDepth),
		s3Client:    s3Client,
		sqsClient:   sqsClient,
		s3Bucket:    s3Bucket,
		sqsQueueUrl: sqsQueueUrl,
		localMode:   localMode,
	}
}

func (a *apiServer) nextSubmissionID(teamID string) (string, int64) {
	val, _ := a.attempts.LoadOrStore(teamID, &atomic.Int64{})
	n := val.(*atomic.Int64).Add(1)
	return fmt.Sprintf("%s_%d", teamID, n), n
}

func (a *apiServer) recordTeamJob(teamID, submissionID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	val, _ := a.teamJobs.LoadOrStore(teamID, &[]string{})
	list := val.(*[]string)
	*list = append(*list, submissionID)
}

func (a *apiServer) startWorkers(ctx context.Context, n int) {
	for i := 0; i < n; i++ {
		go a.worker(ctx, i)
	}
}

func (a *apiServer) worker(ctx context.Context, id int) {
	ticks := workload.Generate(a.seed, a.ticks)
	for {
		select {
		case <-ctx.Done():
			return
		case j, ok := <-a.jobCh:
			if !ok {
				return
			}
			a.runJob(ctx, j, ticks, id)
		}
	}
}

func (a *apiServer) runJob(ctx context.Context, j job, ticks []workload.Tick, id int) {
	a.updateStatus(j.SubmissionID, func(s *jobStatus) {
		s.State = stateRunning
	})

	if a.localMode {
		outDir := filepath.Join("/tmp/exchange-bench-shared", j.SubmissionID, "out")
		os.MkdirAll(outDir, 0o755)
		
		artifactPath, compilerOutput, err := compiler.Compile(ctx, j.SourcePath, j.Language, outDir)
		if err != nil {
			a.updateStatus(j.SubmissionID, func(s *jobStatus) {
				s.State = stateFailed
				s.Error = fmt.Sprintf("compile error: %s", compilerOutput)
			})
			return
		}
		j.ArtifactPath = "file://" + artifactPath
	}

	// Step 2: Start sandbox on a worker
	workerAddr := a.baseCfg.WorkerAddrs[id%len(a.baseCfg.WorkerAddrs)]
	conn, err := grpc.DialContext(ctx, workerAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		a.updateStatus(j.SubmissionID, func(s *jobStatus) {
			s.State = stateFailed
			s.Error = fmt.Sprintf("dial worker: %v", err)
		})
		return
	}
	defer conn.Close()

	client := proto.NewWorkerServiceClient(conn)
	resp, err := client.StartSandbox(ctx, &proto.StartSandboxRequest{
		RunId:        j.RunID,
		BinaryS3Key:  j.ArtifactPath,
		Language:     j.Language,
	})
	if err != nil || resp.Error != "" {
		a.updateStatus(j.SubmissionID, func(s *jobStatus) {
			s.State = stateFailed
			if err != nil {
				s.Error = fmt.Sprintf("start sandbox rpc: %v", err)
			} else {
				s.Error = fmt.Sprintf("start sandbox worker error: %s", resp.Error)
			}
		})
		return
	}

	contestantEndpoint := resp.Endpoint

	// Step 3: Run coordinator
	cfg := a.baseCfg
	cfg.RunID = j.RunID
	cfg.SubmissionID = j.SubmissionID
	cfg.ContestantEndpoint = contestantEndpoint

	c, err := coordinator.New(cfg)
	if err != nil {
		a.updateStatus(j.SubmissionID, func(s *jobStatus) {
			s.State = stateFailed
			s.Error = err.Error()
		})
		return
	}
	defer c.Close()

	runCtx, cancel := context.WithTimeout(ctx, a.runTimeout)
	defer cancel()

	metrics, err := c.Run(runCtx, ticks)
	if err != nil {
		a.updateStatus(j.SubmissionID, func(s *jobStatus) {
			s.State = stateFailed
			s.Error = err.Error()
		})
		return
	}

	a.upsertRunScore(ctx, j, metrics)

	a.updateStatus(j.SubmissionID, func(s *jobStatus) {
		s.State = stateDone
		s.TicksSent = metrics.TicksSent
		s.TicksAcked = metrics.TicksAcked
		s.PeakTPS = metrics.PeakTPS
		s.P50Us = metrics.P50LatencyUs
		s.P90Us = metrics.P90LatencyUs
		s.P99Us = metrics.P99LatencyUs
		s.TimedOut = metrics.TimedOut
	})
}

func (a *apiServer) upsertRunScore(ctx context.Context, j job, metrics runner.RunMetrics) {
	if a.db == nil {
		return
	}
	correctness := float64(0)
	if metrics.TicksSent > 0 {
		correctness = float64(metrics.TicksCorrect) / float64(metrics.TicksSent)
	}

	avg_latency := float64(metrics.P50LatencyUs + metrics.P90LatencyUs + metrics.P99LatencyUs) / 3.0
	if avg_latency < 1.0 {
		avg_latency = 1.0
	}
	multiplier := 1000.0 / avg_latency
	composite_score := float64(metrics.PeakTPS) * correctness * multiplier

	_, err := a.db.Exec(ctx, `
		INSERT INTO run_scores
			(submission_id, team_id, attempt, run_id, language,
			 ticks_sent, ticks_acked, peak_tps, capacity_tps,
			 correctness, composite_score, critical_flag, completed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, NOW())
		ON CONFLICT (submission_id) DO UPDATE SET
			run_id          = EXCLUDED.run_id,
			language        = EXCLUDED.language,
			ticks_sent      = EXCLUDED.ticks_sent,
			ticks_acked     = EXCLUDED.ticks_acked,
			peak_tps        = EXCLUDED.peak_tps,
			capacity_tps    = EXCLUDED.capacity_tps,
			correctness     = EXCLUDED.correctness,
			composite_score = EXCLUDED.composite_score,
			critical_flag   = EXCLUDED.critical_flag,
			completed_at    = EXCLUDED.completed_at
	`,
		j.SubmissionID,
		j.TeamID,
		j.Attempt,
		j.RunID,
		j.Language,
		metrics.TicksSent,
		metrics.TicksAcked,
		metrics.PeakTPS,
		metrics.CapacityTPS,
		correctness,
		composite_score,
		false,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[api] upsert run_scores %s: %v\n", j.SubmissionID, err)
	}
}

func (a *apiServer) updateStatus(submissionID string, fn func(*jobStatus)) {
	val, ok := a.statuses.Load(submissionID)
	if !ok {
		return
	}
	fn(val.(*jobStatus))
}

// ── HTTP handlers ─────────────────────────────────────────────────────────────

func (a *apiServer) handleSubmit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := r.ParseMultipartForm(1 << 20); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}

	teamID := r.FormValue("team_id")
	if teamID == "" {
		http.Error(w, "team_id is required", http.StatusBadRequest)
		return
	}

	language := r.FormValue("language")
	exts, ok := validExtensions[language]
	if !ok {
		http.Error(w, fmt.Sprintf("unsupported language %q; supported: cpp, rust, go, python, zig", language), http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("source")
	if err != nil {
		http.Error(w, "source file is required", http.StatusBadRequest)
		return
	}
	defer file.Close()

	if header.Size > 1<<20 {
		http.Error(w, "source file exceeds 1MB limit", http.StatusBadRequest)
		return
	}

	ext := filepath.Ext(header.Filename)
	if !extValid(exts, ext) {
		http.Error(w, fmt.Sprintf("extension %q is not valid for language %q", ext, language), http.StatusBadRequest)
		return
	}

	submissionID, attempt := a.nextSubmissionID(teamID)
	runID := fmt.Sprintf("run-%d", time.Now().UnixNano())

	file.Seek(0, 0)
	
	var sourcePath string
	if a.localMode {
		outDir := filepath.Join("/tmp/exchange-bench-shared", submissionID, "sources")
		os.MkdirAll(outDir, 0o755)
		localPath := filepath.Join(outDir, "source"+ext)
		f, err := os.Create(localPath)
		if err != nil {
			http.Error(w, "internal error saving file locally: "+err.Error(), http.StatusInternalServerError)
			return
		}
		io.Copy(f, file)
		f.Close()
		sourcePath = localPath
	} else {
		s3Key := fmt.Sprintf("sources/%s/source%s", submissionID, ext)
		if a.s3Client != nil && a.s3Bucket != "" {
			_, err = a.s3Client.PutObject(r.Context(), &s3.PutObjectInput{
				Bucket: aws.String(a.s3Bucket),
				Key:    aws.String(s3Key),
				Body:   file,
			})
			if err != nil {
				http.Error(w, "internal error uploading to S3: "+err.Error(), http.StatusInternalServerError)
				return
			}
			sourcePath = s3Key
		} else {
			http.Error(w, "S3 not configured", http.StatusInternalServerError)
			return
		}
	}

	j := job{
		SubmissionID: submissionID,
		TeamID:       teamID,
		Attempt:      attempt,
		RunID:        runID,
		Language:     language,
		SourcePath:   sourcePath,
	}

	status := &jobStatus{
		SubmissionID: submissionID,
		TeamID:       teamID,
		Attempt:      attempt,
		RunID:        runID,
		Language:     language,
		State:        stateQueued,
	}
	a.statuses.Store(submissionID, status)
	a.recordTeamJob(teamID, submissionID)

	if a.localMode {
		select {
		case a.jobCh <- j:
			// Enqueued
		default:
			a.statuses.Delete(submissionID)
			http.Error(w, "job queue is full", http.StatusServiceUnavailable)
			return
		}
	} else {
		jobBytes, _ := json.Marshal(j)
		if a.sqsClient != nil && a.sqsQueueUrl != "" {
			_, err = a.sqsClient.SendMessage(r.Context(), &sqs.SendMessageInput{
				QueueUrl:    aws.String(a.sqsQueueUrl),
				MessageBody: aws.String(string(jobBytes)),
			})
			if err != nil {
				a.statuses.Delete(submissionID)
				http.Error(w, "internal error queueing job: "+err.Error(), http.StatusInternalServerError)
				return
			}
		} else {
			a.statuses.Delete(submissionID)
			http.Error(w, "SQS not configured", http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(status) //nolint:errcheck
}

func (a *apiServer) handleGetSubmission(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	submissionID := strings.TrimPrefix(r.URL.Path, "/submissions/")
	if submissionID == "" {
		http.Error(w, "submission_id required", http.StatusBadRequest)
		return
	}

	val, ok := a.statuses.Load(submissionID)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(val.(*jobStatus)) //nolint:errcheck
}

func (a *apiServer) handleTeamSubmissions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/teams/")
	teamID := strings.TrimSuffix(path, "/submissions")
	if teamID == "" || teamID == path {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	val, ok := a.teamJobs.Load(teamID)
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"team_id":     teamID,
			"submissions": []any{},
		}) //nolint:errcheck
		return
	}

	a.mu.Lock()
	ids := make([]string, len(*val.(*[]string)))
	copy(ids, *val.(*[]string))
	a.mu.Unlock()

	for i, j := 0, len(ids)-1; i < j; i, j = i+1, j-1 {
		ids[i], ids[j] = ids[j], ids[i]
	}

	submissions := make([]*jobStatus, 0, len(ids))
	for _, sid := range ids {
		if sv, ok := a.statuses.Load(sid); ok {
			submissions = append(submissions, sv.(*jobStatus))
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"team_id":     teamID,
		"submissions": submissions,
	}) //nolint:errcheck
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok")) //nolint:errcheck
}

type webhookPayload struct {
	SubmissionID string `json:"submission_id"`
	ArtifactPath string `json:"artifact_path"`
	Status       string `json:"status"`
	Error        string `json:"error,omitempty"`
}

func (a *apiServer) handleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload webhookPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	val, ok := a.statuses.Load(payload.SubmissionID)
	if !ok {
		http.Error(w, "submission not found", http.StatusNotFound)
		return
	}
	st := val.(*jobStatus)

	if payload.Status == "failed" {
		a.updateStatus(payload.SubmissionID, func(s *jobStatus) {
			s.State = stateFailed
			s.Error = "compilation failed: " + payload.Error
		})
		w.WriteHeader(http.StatusOK)
		return
	}

	j := job{
		SubmissionID: st.SubmissionID,
		TeamID:       st.TeamID,
		Attempt:      st.Attempt,
		RunID:        st.RunID,
		Language:     st.Language,
		ArtifactPath: payload.ArtifactPath,
	}

	select {
	case a.jobCh <- j:
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "internal queue full", http.StatusServiceUnavailable)
	}
}

func extValid(allowed []string, ext string) bool {
	for _, e := range allowed {
		if e == ext {
			return true
		}
	}
	return false
}

func parseBrokers(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

// ── HTTP middleware ─────────────────────────────────────────────────────────────

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ── main ──────────────────────────────────────────────────────────────────────

func main() {
	listen := flag.String("listen", ":8081", "HTTP listen address")
	workers := flag.String("workers", "localhost:9090", "comma-separated worker gRPC addresses")
	image := flag.String("image", "", "Docker image for contestant sandbox")
	pool := flag.Int("pool", 5, "max concurrent benchmark runs")
	queueDepth := flag.Int("queue-depth", 100, "max queued submissions")
	seed := flag.Int64("seed", 42, "workload RNG seed")
	ticks := flag.Int("ticks", 100_000, "ticks per benchmark run")
	initRate := flag.Int("init-rate", 1_000, "starting rate per worker (ticks/sec)")
	maxRate := flag.Int("max-rate", 50_000, "rate cap per worker (ticks/sec)")
	ramp := flag.Duration("ramp", 5*time.Second, "ramp interval")
	timeout := flag.Duration("timeout", 300*time.Second, "per-run wall-clock timeout")
	seccomp := flag.String("seccomp", "deployments/docker/seccomp/contestant.json", "seccomp profile path")
	dsn := flag.String("dsn", "", "TimescaleDB connection string (optional, enables score writes)")
	redpandaBrokers := flag.String("redpanda-brokers", "", "comma-separated Redpanda broker addresses (empty = skip telemetry)")
	redpandaTopic := flag.String("redpanda-topic", "telemetry-events", "Redpanda topic for telemetry events")
	localMode := flag.Bool("local", false, "Local testing mode (bypass AWS S3/SQS and compile locally)")
	flag.Parse()

	if *image == "" {
		fmt.Fprintln(os.Stderr, "error: --image is required")
		flag.Usage()
		os.Exit(1)
	}

	var db *pgxpool.Pool
	if *dsn != "" {
		var err error
		db, err = pgxpool.New(context.Background(), *dsn)
		if err != nil {
			fmt.Fprintf(os.Stderr, "api: db: %v\n", err)
			os.Exit(1)
		}
		defer db.Close()
	}

	addrs := strings.Split(*workers, ",")
	baseCfg := coordinator.Config{
		WorkerAddrs:     addrs,
		Image:           *image,
		InitialRate:     *initRate,
		MaxRate:         *maxRate,
		RampInterval:    *ramp,
		RedpandaBrokers: parseBrokers(*redpandaBrokers),
		RedpandaTopic:   *redpandaTopic,
	}

	var s3Client *s3.Client
	var sqsClient *sqs.Client
	var awsCfg aws.Config

	var s3Bucket string
	var sqsQueueUrl string

	if !*localMode {
		s3Bucket = os.Getenv("S3_BUCKET")
		sqsQueueUrl = os.Getenv("SQS_QUEUE_URL")
		if s3Bucket == "" || sqsQueueUrl == "" {
			log.Println("WARNING: S3_BUCKET or SQS_QUEUE_URL not set. Async compilation will fail.")
		}
		
		var err error
		awsCfg, err = config.LoadDefaultConfig(context.Background())
		if err != nil {
			fmt.Fprintf(os.Stderr, "api: failed to load AWS config: %v\n", err)
			os.Exit(1)
		}

		s3Client = s3.NewFromConfig(awsCfg)
		sqsClient = sqs.NewFromConfig(awsCfg)
	}

	srv := newAPIServer(baseCfg, *seed, *ticks, *timeout, *queueDepth, *seccomp, db, s3Client, sqsClient, s3Bucket, sqsQueueUrl, *localMode)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv.startWorkers(ctx, *pool)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", handleHealth)
	mux.HandleFunc("/submissions", srv.handleSubmit)
	mux.HandleFunc("/submissions/", srv.handleGetSubmission)
	mux.HandleFunc("/teams/", srv.handleTeamSubmissions)
	mux.HandleFunc("/webhook/compiler", srv.handleWebhook)

	log.Printf("[api] listening on %s  pool=%d  queue=%d  workers=%v  image=%s",
		*listen, *pool, *queueDepth, addrs, *image)

	if err := http.ListenAndServe(*listen, withCORS(mux)); err != nil {
		fmt.Fprintf(os.Stderr, "api: %v\n", err)
		os.Exit(1)
	}
}
