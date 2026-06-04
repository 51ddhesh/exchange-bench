package botworker

import (
	"context"
	"fmt"
	"sync"

	proto "github.com/51ddhesh/exchange-bench/internal/coordinator/proto"
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
}

func NewWorkerServer(workerID, seccomp string) *workerServer {
	return &workerServer{workerID: workerID, seccomp: seccomp}
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
