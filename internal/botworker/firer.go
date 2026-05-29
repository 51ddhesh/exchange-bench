package botworker

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/51ddhesh/exchange-bench/internal/coordinator/proto"
	"github.com/51ddhesh/exchange-bench/internal/protocol"
	"github.com/51ddhesh/exchange-bench/internal/runner"
	"github.com/51ddhesh/exchange-bench/internal/workload"
	"github.com/HdrHistogram/hdrhistogram-go"
)

type firerMetrics struct {
	TicksSent    int64
	TicksAcked   int64
	P50LatencyUs int64
	P90LatencyUs int64
	P99LatencyUs int64
	PeakTPS      float64
	TimedOut     bool
}

type inFlight struct {
	m sync.Map
}

func (f *inFlight) store(orderID string, intendedAt int64) {
	f.m.Store(orderID, intendedAt)
}

func (f *inFlight) pop(orderID string) (int64, bool) {
	v, ok := f.m.LoadAndDelete(orderID)

	if !ok {
		return 0, false
	}

	return v.(int64), true
}

type firer struct {
	ticks      []proto.Tick
	image      string
	seccomp    string
	rate       atomic.Int32
	ticksSent  atomic.Int64
	ticksAcked atomic.Int64
	timedOut   atomic.Bool
	hist       *hdrhistogram.Histogram
	histMu     sync.Mutex
	events     chan TelemetryEvent
	done       chan struct{}
}

func newFirer(ticks []*proto.Tick, image string, initialRate int32, seccomp string) *firer {
	f := &firer{
		image:   image,
		seccomp: seccomp,
		hist:    hdrhistogram.New(1, 60_000_000, 3),
		events:  make(chan TelemetryEvent, 4096),
		done:    make(chan struct{}),
	}

	f.ticks = make([]proto.Tick, len(ticks))

	for i, t := range ticks {
		f.ticks[i] = *t
	}

	f.rate.Store(initialRate)
	return f
}

// Run spin-waits until fireAtUnixNs, starts the sandbox, then launches the
// writer and reader goroutines. Writer fires ticks open-loop; reader collects
// ACKs and records RTT. Returns after all ticks are fired and all ACKs drained.
func (f *firer) Run(ctx context.Context, fireAtUnixNs int64) error {
	for time.Now().UnixNano() < fireAtUnixNs {
		runtime.Gosched()
	}

	sb, err := runner.StartSandbox(ctx, f.image, f.seccomp)
	if err != nil {
		close(f.events)
		close(f.done)
		return err
	}

	inf := &inFlight{}
	reader := protocol.NewReader(sb.Stdout())

	// Reader goroutine
	var readerWg sync.WaitGroup
	readerWg.Add(1)
	go func() {
		defer readerWg.Done()
		defer close(f.events)

		for {
			resp, err := reader.ReadResponse()
			if err != nil {
				return
			}

			if resp.Type != protocol.RespACK && resp.Type != protocol.RespREJ {
				continue
			}

			receivedAt := time.Now().UnixNano()
			intendedAt, ok := inf.pop(resp.OrderID)

			if !ok {
				continue
			}

			deltaUs := (receivedAt - intendedAt) / 1000
			if deltaUs < 1 {
				deltaUs = 1
			}

			f.histMu.Lock()
			f.hist.RecordValue(deltaUs)
			f.histMu.Unlock()

			acked := resp.Type == protocol.RespACK

			if acked {
				f.ticksAcked.Add(1)
			}

			f.events <- TelemetryEvent{
				OrderID:      resp.OrderID,
				IntendedAtNs: intendedAt,
				ReceivedAtNs: receivedAt,
				Acked:        acked,
			}
		}
	}()

	// Open loop writer
	currentRate := f.rate.Load()
	ticker := time.NewTicker(tickInterval(currentRate))
	defer ticker.Stop()

	for i := range f.ticks {
		select {
		case <-ctx.Done():
			f.timedOut.Store(true)
			goto writerDone
		case <-ticker.C:
		}

		if newRate := f.rate.Load(); newRate != currentRate {
			currentRate = newRate
			ticker.Reset(tickInterval(currentRate))
		}

		tick := protoToWorkload(f.ticks[i])
		intendedAt := time.Now().UnixNano()
		inf.store(tick.OrderID, intendedAt)
		f.ticksSent.Add(1)

		if err := protocol.WriteTick(sb.Stdin(), tick); err != nil {
			goto writerDone
		}
	}

writerDone:
	sb.Stdin().Close() // EOF
	sb.Kill()
	readerWg.Wait()
	close(f.done)
	return nil
}

func tickInterval(ratePerSec int32) time.Duration {
	if ratePerSec <= 0 {
		return time.Second
	}

	return time.Second / time.Duration(ratePerSec)
}

func protoToWorkload(t proto.Tick) workload.Tick {
	return workload.Tick{
		Type:    workload.TickType(t.Type),
		OrderID: t.OrderId,
		Side:    byte(t.Side),
		OrdType: byte(t.OrdType),
		Price:   t.Price,
		Qty:     t.Qty,
	}
}

func (f *firer) SetRate(ratePerSec int32) {
	f.rate.Store(ratePerSec)
}

func (f *firer) Events() <-chan TelemetryEvent {
	return f.events
}

func (f *firer) Metrics() firerMetrics {
	<-f.done
	f.histMu.Lock()
	defer f.histMu.Unlock()

	return firerMetrics{
		TicksSent:    f.ticksSent.Load(),
		TicksAcked:   f.ticksAcked.Load(),
		P50LatencyUs: f.hist.ValueAtQuantile(50),
		P90LatencyUs: f.hist.ValueAtQuantile(90),
		P99LatencyUs: f.hist.ValueAtQuantile(99),
		TimedOut:     f.timedOut.Load(),
	}
}
