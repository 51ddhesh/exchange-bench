package coordinator

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	proto "github.com/51ddhesh/exchange-bench/internal/coordinator/proto"
	"github.com/51ddhesh/exchange-bench/internal/runner"
	"github.com/51ddhesh/exchange-bench/internal/workload"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type Config struct {
	WorkerAddrs  []string
	Image        string
	RunID        string
	SubmissionID string // added — "team-1_1" format
	InitialRate  int
	MaxRate      int
	RampInterval time.Duration
}

type Coordinator struct {
	cfg     Config
	clients []proto.WorkerServiceClient
	conns   []*grpc.ClientConn
}

func New(cfg Config) (*Coordinator, error) {
	c := &Coordinator{cfg: cfg}
	for _, addr := range cfg.WorkerAddrs {
		conn, err := grpc.NewClient(addr,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithDefaultCallOptions(grpc.MaxCallSendMsgSize(64*1024*1024)),
		)

		if err != nil {
			c.Close()
			return nil, fmt.Errorf("coordinator: dial %s: %w", addr, err)
		}
		c.conns = append(c.conns, conn)
		c.clients = append(c.clients, proto.NewWorkerServiceClient(conn))
	}
	return c, nil
}

func (c *Coordinator) Close() {
	for _, conn := range c.conns {
		conn.Close()
	}
}

func (c *Coordinator) Run(ctx context.Context, ticks []workload.Tick) (runner.RunMetrics, error) {
	shards := splitShards(ticks, len(c.clients))

	// Prepare gets its own 60s deadline — it's just serialization + handshake.
	prepCtx, prepCancel := context.WithTimeout(ctx, 60*time.Second)
	defer prepCancel()

	prepErrs := make([]error, len(c.clients))
	var prepWg sync.WaitGroup
	for i, client := range c.clients {
		prepWg.Add(1)
		go func(i int, client proto.WorkerServiceClient) {
			defer prepWg.Done()
			resp, err := client.Prepare(prepCtx, &proto.PrepareRequest{
				RunId:      c.cfg.RunID,
				Image:      c.cfg.Image,
				Ticks:      workloadToProto(shards[i]),
				RatePerSec: int32(c.cfg.InitialRate),
			})
			if err != nil {
				prepErrs[i] = err
				return
			}
			if !resp.Ready {
				prepErrs[i] = fmt.Errorf("worker %d not ready: %s", i, resp.Error)
			}
		}(i, client)
	}
	prepWg.Wait()
	prepCancel()
	for _, err := range prepErrs {
		if err != nil {
			return runner.RunMetrics{}, fmt.Errorf("coordinator: prepare failed: %w", err)
		}
	}

	// Fire gets a fresh context independent of prepare time.
	fireCtx, fireCancel := context.WithTimeout(ctx, 300*time.Second)
	defer fireCancel()

	fireAt := time.Now().Add(3 * time.Second)

	telemetryCh := make(chan *proto.TelemetryEvent, 8192)
	fireErrs := make([]error, len(c.clients))
	var fireWg sync.WaitGroup

	for i, client := range c.clients {
		fireWg.Add(1)
		go func(i int, client proto.WorkerServiceClient) {
			defer fireWg.Done()
			stream, err := client.Fire(fireCtx, &proto.FireRequest{
				RunId:        c.cfg.RunID,
				FireAtUnixNs: fireAt.UnixNano(),
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "[coordinator] Fire worker %d error: %v\n", i, err)
				fireErrs[i] = err
				return
			}
			fmt.Fprintf(os.Stderr, "[coordinator] Fire worker %d stream open\n", i)
			for {
				evt, err := stream.Recv()
				if err != nil {
					fmt.Fprintf(os.Stderr, "[coordinator] Fire worker %d stream closed: %v\n", i, err)
					return
				}
				telemetryCh <- evt
			}
		}(i, client)
	}

	// Close fan-in once all Fire streams finish.
	go func() {
		fireWg.Wait()
		close(telemetryCh)
	}()

	// currentRate is written by the ramp loop and read by the saturation detector.
	var currentRate atomic.Int64
	currentRate.Store(int64(c.cfg.InitialRate) * int64(len(c.clients)))

	saturated := make(chan struct{})

	// ── Ramp-up loop ─────────────────────────────────────────────────────────
	go func() {
		rate := c.cfg.InitialRate
		for {
			select {
			case <-fireCtx.Done():
				return
			case <-saturated:
				return
			case <-time.After(c.cfg.RampInterval):
			}
			rate = min(rate*2, c.cfg.MaxRate)
			for _, client := range c.clients {
				client.SetRate(fireCtx, &proto.SetRateRequest{ //nolint:errcheck
					RunId:      c.cfg.RunID,
					RatePerSec: int32(rate),
				})
			}
			currentRate.Store(int64(rate) * int64(len(c.clients)))
			if rate >= c.cfg.MaxRate {
				return
			}
		}
	}()

	// ── Telemetry fan-in + PeakTPS detection ─────────────────────────────────
	// Count ACKs in 1-second windows. Two consecutive windows where
	// ackRate < sendRate*0.95 = saturation. PeakTPS = highest window before that.
	var peakTPS float64
	var consecBelow int
	windowStart := time.Now()
	var windowAcks int64
	satOnce := sync.Once{}

	for evt := range telemetryCh {
		if evt.Acked {
			windowAcks++
		}

		if time.Since(windowStart) >= time.Second {
			ackRate := float64(windowAcks)
			sendRate := float64(currentRate.Load())

			// Always track highest ACK rate seen.
			if ackRate > peakTPS {
				peakTPS = ackRate
			}

			// Saturation: two consecutive windows below 95% of send rate.
			if ackRate < sendRate*0.95 {
				consecBelow++
				if consecBelow >= 2 {
					satOnce.Do(func() { close(saturated) })
				}
			} else {
				consecBelow = 0
			}

			windowAcks = 0
			windowStart = time.Now()
		}
	}

	// Drain the saturated channel in case the ramp loop never fired it.
	satOnce.Do(func() { close(saturated) })

	// ── Phase 3: Collect final metrics ───────────────────────────────────────
	workerMetrics := make([]*proto.WorkerMetrics, len(c.clients))
	var collectWg sync.WaitGroup
	for i, client := range c.clients {
		collectWg.Add(1)
		go func(i int, client proto.WorkerServiceClient) {
			defer collectWg.Done()
			m, err := client.CollectMetrics(ctx, &proto.CollectRequest{RunId: c.cfg.RunID})
			if err != nil {
				workerMetrics[i] = &proto.WorkerMetrics{}
				return
			}
			workerMetrics[i] = m
		}(i, client)
	}
	collectWg.Wait()

	result := merge(workerMetrics)
	result.PeakTPS = peakTPS
	return result, nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

func splitShards(ticks []workload.Tick, n int) [][]workload.Tick {
	shards := make([][]workload.Tick, n)
	size := len(ticks) / n
	for i := 0; i < n-1; i++ {
		shards[i] = ticks[i*size : (i+1)*size]
	}
	shards[n-1] = ticks[(n-1)*size:]
	return shards
}

func workloadToProto(ticks []workload.Tick) []*proto.Tick {
	out := make([]*proto.Tick, len(ticks))
	for i, t := range ticks {
		out[i] = &proto.Tick{
			Type:    int32(t.Type),
			OrderId: t.OrderID,
			Side:    int32(t.Side),
			OrdType: int32(t.OrdType),
			Price:   t.Price,
			Qty:     t.Qty,
		}
	}
	return out
}
