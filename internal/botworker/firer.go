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

// Run is not yet implemented. It requires the Step 4 WebSocket bot fleet
// rewrite. Currently spin-waits until fireAtUnixNs then exits cleanly so
// that Events() and Metrics() callers do not deadlock.
func (f *firer) Run(ctx context.Context, fireAtUnixNs int64) error {
	for time.Now().UnixNano() < fireAtUnixNs {
		runtime.Gosched()
	}
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
