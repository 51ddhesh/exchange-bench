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

	"github.com/51ddhesh/exchange-bench/internal/compiler"
	"github.com/51ddhesh/exchange-bench/internal/coordinator"
	"github.com/51ddhesh/exchange-bench/internal/runner"
	"github.com/51ddhesh/exchange-bench/internal/workload"
)

// validExtensions maps declared language to accepted file extensions.
var validExtensions = map[string][]string{
	"cpp":    {".cpp", ".cc", ".cxx"},
	"rust":   {".rs"},
	"go":     {".go"},
	"python": {".py"},
	"zig":    {".zig"},
}

// ── job types ────────────────────────────────────────────────────────────────

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
	SourcePath   string // path to uploaded source file; used by compiler in Step 1
}

type jobStatus struct {
	SubmissionID string   `json:"submission_id"`
	TeamID       string   `json:"team_id"`
	Attempt      int64    `json:"attempt"`
	RunID        string   `json:"run_id"`
	Language     string   `json:"language"`
	State        jobState `json:"status"`
	Error        string   `json:"error,omitempty"`

	// populated when state == stateDone
	TicksSent  int64   `json:"ticks_sent,omitempty"`
	TicksAcked int64   `json:"ticks_acked,omitempty"`
	PeakTPS    float64 `json:"peak_tps,omitempty"`
	P50Us      int64   `json:"p50_us,omitempty"`
	P90Us      int64   `json:"p90_us,omitempty"`
	P99Us      int64   `json:"p99_us,omitempty"`
	TimedOut   bool    `json:"timed_out,omitempty"`
}

// ── API server ───────────────────────────────────────────────────────────────

type apiServer struct {
	baseCfg     coordinator.Config // template; RunID + SubmissionID overridden per job
	seed        int64
	ticks       int
	runTimeout  time.Duration
	seccompPath string
	jobCh       chan job
	statuses    sync.Map   // submission_id → *jobStatus
	attempts    sync.Map   // team_id        → *atomic.Int64
	teamJobs    sync.Map   // team_id        → *[]string  (submission_ids, append-only)
	mu          sync.Mutex // protects teamJobs appends
}

func newAPIServer(baseCfg coordinator.Config, seed int64, ticks int, runTimeout time.Duration, queueDepth int, seccompPath string) *apiServer {
	return &apiServer{
		baseCfg:     baseCfg,
		seed:        seed,
		ticks:       ticks,
		runTimeout:  runTimeout,
		seccompPath: seccompPath,
		jobCh:       make(chan job, queueDepth),
	}
}

// nextSubmissionID atomically increments the attempt counter for teamID and
// returns the new submission_id and attempt number.
func (a *apiServer) nextSubmissionID(teamID string) (string, int64) {
	val, _ := a.attempts.LoadOrStore(teamID, &atomic.Int64{})
	n := val.(*atomic.Int64).Add(1)
	return fmt.Sprintf("%s_%d", teamID, n), n
}

// recordTeamJob appends submissionID to the team's ordered history.
func (a *apiServer) recordTeamJob(teamID, submissionID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	val, _ := a.teamJobs.LoadOrStore(teamID, &[]string{})
	list := val.(*[]string)
	*list = append(*list, submissionID)
}

// startWorkers launches n goroutines that drain jobCh.
func (a *apiServer) startWorkers(ctx context.Context, n int) {
	for i := 0; i < n; i++ {
		go a.worker(ctx)
	}
}

func (a *apiServer) worker(ctx context.Context) {
	// Pre-generate the tick slice once — same seed means same sequence.
	// All workers share it read-only; workload.Generate is pure.
	ticks := workload.Generate(a.seed, a.ticks)

	for {
		select {
		case <-ctx.Done():
			return
		case j, ok := <-a.jobCh:
			if !ok {
				return
			}
			a.runJob(ctx, j, ticks)
		}
	}
}

func (a *apiServer) runJob(ctx context.Context, j job, ticks []workload.Tick) {
	a.updateStatus(j.SubmissionID, func(s *jobStatus) {
		s.State = stateRunning
	})

	// Step 1: Compile
	outDir := filepath.Join("/tmp/exchange-bench", j.SubmissionID, "out")
	artifactPath, compilerOutput, err := compiler.Compile(ctx, j.SourcePath, j.Language, outDir)
	if err != nil {
		a.updateStatus(j.SubmissionID, func(s *jobStatus) {
			s.State = stateFailed
			s.Error = fmt.Sprintf("compile error: %s", compilerOutput)
		})
		return
	}

	// Step 2: Start sandbox
	sb := runner.NewSandbox(a.seccompPath)
	if err := sb.Start(ctx, artifactPath, j.Language); err != nil {
		a.updateStatus(j.SubmissionID, func(s *jobStatus) {
			s.State = stateFailed
			s.Error = fmt.Sprintf("sandbox start: %v", err)
		})
		return
	}
	defer sb.Kill()

	// Step 3: Run coordinator
	cfg := a.baseCfg
	cfg.RunID = j.RunID
	cfg.SubmissionID = j.SubmissionID
	cfg.ContestantEndpoint = sb.Endpoint()

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

func (a *apiServer) updateStatus(submissionID string, fn func(*jobStatus)) {
	val, ok := a.statuses.Load(submissionID)
	if !ok {
		return
	}
	fn(val.(*jobStatus))
}

// ── HTTP handlers ─────────────────────────────────────────────────────────────

// POST /submissions
// Body: multipart/form-data with fields: team_id, language, source (file)
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

	dir := filepath.Join("/tmp/exchange-bench", submissionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		http.Error(w, "internal error creating submission directory", http.StatusInternalServerError)
		return
	}

	srcPath := filepath.Join(dir, "source"+ext)
	f, err := os.Create(srcPath)
	if err != nil {
		http.Error(w, "internal error writing source file", http.StatusInternalServerError)
		return
	}
	if _, err := io.Copy(f, file); err != nil {
		f.Close()
		http.Error(w, "internal error writing source file", http.StatusInternalServerError)
		return
	}
	f.Close()

	j := job{
		SubmissionID: submissionID,
		TeamID:       teamID,
		Attempt:      attempt,
		RunID:        runID,
		Language:     language,
		SourcePath:   srcPath,
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

	select {
	case a.jobCh <- j:
	default:
		a.statuses.Delete(submissionID)
		os.RemoveAll(dir)
		http.Error(w, "queue full, try again later", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(status) //nolint:errcheck
}

// GET /submissions/{submission_id}
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

// GET /teams/{team_id}/submissions
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
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"team_id":     teamID,
			"submissions": []any{},
		})
		return
	}

	a.mu.Lock()
	ids := make([]string, len(*val.(*[]string)))
	copy(ids, *val.(*[]string))
	a.mu.Unlock()

	// Reverse: most recent attempt first.
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
	json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
		"team_id":     teamID,
		"submissions": submissions,
	})
}

// GET /health
func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok")) //nolint:errcheck
}

// extValid reports whether ext is in the allowed list.
func extValid(allowed []string, ext string) bool {
	for _, e := range allowed {
		if e == ext {
			return true
		}
	}
	return false
}

// ── main ──────────────────────────────────────────────────────────────────────

func main() {
	listen := flag.String("listen", ":8081", "HTTP listen address")
	workers := flag.String("workers", "localhost:9090", "comma-separated worker gRPC addresses")
	image := flag.String("image", "", "Docker image for contestant sandbox")
	pool := flag.Int("pool", 5, "max concurrent benchmark runs")
	queueDepth := flag.Int("queue-depth", 100, "max queued submissions")
	seccomp := flag.String("seccomp", "deployments/docker/seccomp/contestant.json", "seccomp profile path")
	seed := flag.Int64("seed", 42, "workload RNG seed")
	ticks := flag.Int("ticks", 100_000, "ticks per benchmark run")
	initRate := flag.Int("init-rate", 1_000, "starting rate per worker (ticks/sec)")
	maxRate := flag.Int("max-rate", 50_000, "rate cap per worker (ticks/sec)")
	ramp := flag.Duration("ramp", 5*time.Second, "ramp interval")
	timeout := flag.Duration("timeout", 300*time.Second, "per-run wall-clock timeout")
	flag.Parse()

	if *image == "" {
		fmt.Fprintln(os.Stderr, "error: --image is required")
		flag.Usage()
		os.Exit(1)
	}

	addrs := strings.Split(*workers, ",")
	baseCfg := coordinator.Config{
		WorkerAddrs:  addrs,
		Image:        *image,
		InitialRate:  *initRate,
		MaxRate:      *maxRate,
		RampInterval: *ramp,
	}

	srv := newAPIServer(baseCfg, *seed, *ticks, *timeout, *queueDepth, *seccomp)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv.startWorkers(ctx, *pool)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", handleHealth)
	mux.HandleFunc("/submissions", srv.handleSubmit)
	mux.HandleFunc("/submissions/", srv.handleGetSubmission)
	mux.HandleFunc("/teams/", srv.handleTeamSubmissions)

	log.Printf("[api] listening on %s  pool=%d  queue=%d  workers=%v  image=%s",
		*listen, *pool, *queueDepth, addrs, *image)

	if err := http.ListenAndServe(*listen, mux); err != nil {
		fmt.Fprintf(os.Stderr, "api: %v\n", err)
		os.Exit(1)
	}
}
