package botworker

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	proto "github.com/51ddhesh/exchange-bench/internal/coordinator/proto"
	"github.com/51ddhesh/exchange-bench/internal/runner"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type runState int

const (
	stateIdle     runState = 0
	statePrepared runState = 1
	stateFiring   runState = 2
	stateDone     runState = 3
)

type workerServer struct {
	proto.UnimplementedWorkerServiceServer

	mu       sync.Mutex
	state    runState
	runID    string
	workerID string
	seccomp  string // retained for future sandbox use; not forwarded to firer
	f        *firer

	s3Client *s3.Client
	s3Bucket string
	sb       runner.Sandbox
}

func NewWorkerServer(workerID, seccomp string, s3Client *s3.Client) *workerServer {
	return &workerServer{
		workerID: workerID, 
		seccomp:  seccomp,
		s3Client: s3Client,
		s3Bucket: os.Getenv("S3_BUCKET"),
	}
}

func (w *workerServer) StartSandbox(ctx context.Context, req *proto.StartSandboxRequest) (*proto.StartSandboxResponse, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Clean up previous sandbox if exists
	if w.sb != nil {
		w.sb.Kill()
		w.sb = nil
	}

	baseTmp := os.Getenv("WORKER_TMP_DIR")
	if baseTmp == "" {
		baseTmp = "/tmp"
	}
	workDir := filepath.Join(baseTmp, req.RunId)
	os.MkdirAll(workDir, 0755)

	localBinPath := filepath.Join(workDir, "binary")
	f, err := os.Create(localBinPath)
	if err != nil {
		return &proto.StartSandboxResponse{Error: fmt.Sprintf("create binary file: %v", err)}, nil
	}
	if strings.HasPrefix(req.BinaryS3Key, "file://") {
		localSourcePath := strings.TrimPrefix(req.BinaryS3Key, "file://")
		src, err := os.Open(localSourcePath)
		if err != nil {
			f.Close()
			return &proto.StartSandboxResponse{Error: fmt.Sprintf("open local binary: %v", err)}, nil
		}
		io.Copy(f, src)
		src.Close()
		f.Close()
	} else {
		out, err := w.s3Client.GetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(w.s3Bucket),
			Key:    aws.String(req.BinaryS3Key),
		})
		if err != nil {
			f.Close()
			return &proto.StartSandboxResponse{Error: fmt.Sprintf("download binary: %v", err)}, nil
		}
		
		io.Copy(f, out.Body)
		f.Close()
		out.Body.Close()
	}

	os.Chmod(localBinPath, 0755)

	sb := runner.NewSandbox(w.seccomp)
	if err := sb.Start(ctx, localBinPath, req.Language); err != nil {
		return &proto.StartSandboxResponse{Error: fmt.Sprintf("start sandbox: %v", err)}, nil
	}

	w.sb = sb
	return &proto.StartSandboxResponse{Endpoint: sb.Endpoint()}, nil
}

func (w *workerServer) Prepare(ctx context.Context, req *proto.PrepareRequest) (*proto.PrepareResponse, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.runID = req.RunId
	w.f = newFirer(req.Ticks, int32(req.RatePerSec))
	w.state = statePrepared

	return &proto.PrepareResponse{Ready: true}, nil
}

func (w *workerServer) Fire(req *proto.FireRequest, stream proto.WorkerService_FireServer) error {
	w.mu.Lock()
	if w.state != statePrepared || w.runID != req.RunId {
		w.mu.Unlock()
		return fmt.Errorf("worker not prepared for run %q", req.RunId)
	}
	f := w.f
	w.state = stateFiring
	w.mu.Unlock()

	var drainWg sync.WaitGroup
	drainWg.Add(1)
	go func() {
		defer drainWg.Done()
		for evt := range f.Events() {
			stream.Send(&proto.TelemetryEvent{ //nolint:errcheck
				OrderId:      evt.OrderID,
				IntendedAtNs: evt.IntendedAtNs,
				ReceivedAtNs: evt.ReceivedAtNs,
				Acked:        evt.Acked,
				Violation:    evt.Violation,
			})
		}
	}()

	runErr := f.Run(stream.Context(), req.FireAtUnixNs, req.ContestantEndpoint)
	drainWg.Wait()

	w.mu.Lock()
	w.state = stateDone
	w.mu.Unlock()

	return runErr
}

func (w *workerServer) SetRate(ctx context.Context, req *proto.SetRateRequest) (*proto.SetRateResponse, error) {
	w.mu.Lock()
	f := w.f
	w.mu.Unlock()

	if f != nil {
		f.SetRate(req.RatePerSec)
	}
	return &proto.SetRateResponse{}, nil
}

func (w *workerServer) CollectMetrics(ctx context.Context, req *proto.CollectRequest) (*proto.WorkerMetrics, error) {
	w.mu.Lock()
	f := w.f
	w.mu.Unlock()

	if f == nil {
		return &proto.WorkerMetrics{WorkerId: w.workerID}, nil
	}

	m := f.Metrics()
	return &proto.WorkerMetrics{
		TicksSent:  m.TicksSent,
		TicksAcked: m.TicksAcked,
		P50Us:      m.P50LatencyUs,
		P90Us:      m.P90LatencyUs,
		P99Us:      m.P99LatencyUs,
		PeakTps:    m.PeakTPS,
		TimedOut:   m.TimedOut,
		WorkerId:   w.workerID,
	}, nil
}
