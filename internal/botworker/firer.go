package botworker

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/51ddhesh/exchange-bench/internal/coordinator/proto"
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
	ticks  []proto.Tick
	image  string
	rate   atomic.Int32
	hist   *hdrhistogram.Histogram
	events chan TelemetryEvent
	done   chan struct{}
}

func newFirer(ticks []*proto.Tick, image string, initialRate int32) *firer {
	f := &firer{
		image:  image,
		hist:   hdrhistogram.New(1, 60_000_000, 3),
		events: make(chan TelemetryEvent, 4096),
		done:   make(chan struct{}),
	}

	f.ticks = make([]proto.Tick, len(ticks))

	for i, t := range ticks {
		f.ticks[i] = *t
	}

	f.rate.Store(initialRate)
	return f
}

// Run waits until fireAtUnixNs then fires all ticks open-loop.
// Closes f.events when done.
func (f *firer) Run(ctx context.Context, fireAtUnixNs int64) error {
	// spin-wait for synchronized start
	for time.Now().UnixNano() < fireAtUnixNs {
		runtime.Gosched()
	}

	// TODO: open-loop writer+reader goroutines
	close(f.events)
	close(f.done)

	return nil
}

func (f *firer) SetRate(ratePerSec int32) {
	f.rate.Store(ratePerSec)
}

func (f *firer) Events() <-chan TelemetryEvent {
	return f.events
}

func (f *firer) Metrics() firerMetrics {
	<-f.done

	return firerMetrics{
		P50LatencyUs: f.hist.ValueAtQuantile(50),
		P90LatencyUs: f.hist.ValueAtQuantile(90),
		P99LatencyUs: f.hist.ValueAtQuantile(99),
	}
}
