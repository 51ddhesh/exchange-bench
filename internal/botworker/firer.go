package botworker

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	proto "github.com/51ddhesh/exchange-bench/internal/coordinator/proto"
	"github.com/51ddhesh/exchange-bench/internal/runner"
	"github.com/51ddhesh/exchange-bench/internal/validator"
	"github.com/51ddhesh/exchange-bench/internal/workload"
	hdrhistogram "github.com/HdrHistogram/hdrhistogram-go"
)

const botCount = 200

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
	ticks      []workload.Tick
	rate       atomic.Int32
	ticksSent  atomic.Int64
	ticksAcked atomic.Int64
	timedOut   atomic.Bool
	bots       [botCount]*bot
	events     chan TelemetryEvent
	done       chan struct{}
}

func newFirer(ticks []*proto.Tick, initialRate int32) *firer {
	f := &firer{
		ticks:  protoToWorkload(ticks),
		events: make(chan TelemetryEvent, 4096),
		done:   make(chan struct{}),
	}
	f.rate.Store(initialRate)
	return f
}

// Run spin-waits until fireAtUnixNs, then launches botCount bot goroutines
// each backed by a per-bot validator correlator. Blocks until all correlators
// complete, then closes events and done.
//
// Per-bot isolation: the contestant uses per-connection engines, so each bot's
// responses are validated against an independent reference engine sized to the
// shard. Using one shared engine across bots would produce false cross-bot fills.
func (f *firer) Run(ctx context.Context, fireAtUnixNs int64, endpoint string) error {
	for time.Now().UnixNano() < fireAtUnixNs {
		runtime.Gosched()
	}

	shards := splitTicks(f.ticks, botCount)

	// correlatorsWg tracks per-bot correlator goroutines. When all exit,
	// every event has been validated and written to f.events.
	var correlatorsWg sync.WaitGroup

	for i := 0; i < botCount; i++ {
		hist := hdrhistogram.New(1, 60_000_000, 3)
		var mu sync.Mutex

		// botEvtCh carries raw events from bot.readLoop to the correlator.
		// Closed by bot.run() via defer when the bot finishes.
		botEvtCh := make(chan TelemetryEvent, 4096)

		b := &bot{
			id:         i,
			ticks:      shards[i],
			endpoint:   endpoint,
			botCount:   botCount,
			rate:       &f.rate,
			botEvtCh:   botEvtCh,
			hist:       hist,
			histMu:     &mu,
			ticksSent:  &f.ticksSent,
			ticksAcked: &f.ticksAcked,
		}
		f.bots[i] = b

		// Per-bot validator: arena sized to shard length to avoid 1M-order
		// overhead × 200 bots. Consume starts the engine goroutine; it shuts
		// down when resultsCh is closed by the correlator.
		val := validator.NewWithCapacity(len(shards[i]) + 100)
		resultsCh := make(chan runner.TickResult, 4096)
		verdictCh := val.Consume(resultsCh)

		// Correlator: for each raw event, feeds a TickResult into the
		// validator, reads back the verdict, sets Violation, forwards to
		// f.events. Sequential per-bot, parallel across bots.
		correlatorsWg.Add(1)
		go func(
			botEvtCh <-chan TelemetryEvent,
			resultsCh chan<- runner.TickResult,
			verdictCh <-chan validator.TickVerdict,
		) {
			defer correlatorsWg.Done()
			for evt := range botEvtCh {
				resultsCh <- runner.TickResult{
					Tick:       evt.Tick,
					Responses:  evt.Responses,
					IntendedAt: evt.IntendedAtNs,
					ReceivedAt: evt.ReceivedAtNs,
				}
				verdict := <-verdictCh
				if !verdict.Correct {
					evt.Violation = string(verdict.Violation)
				}
				f.events <- evt
			}
			close(resultsCh)
			for range verdictCh {
			} // drain any buffered verdicts
		}(botEvtCh, resultsCh, verdictCh)

		// Bot goroutine closes botEvtCh when run() returns, unblocking
		// the correlator's range loop.
		go b.run(ctx)
	}

	correlatorsWg.Wait()
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

// Metrics blocks until Run returns, then merges per-bot HDR histograms.
func (f *firer) Metrics() firerMetrics {
	<-f.done

	merged := hdrhistogram.New(1, 60_000_000, 3)
	for _, b := range f.bots {
		if b == nil {
			continue
		}
		b.histMu.Lock()
		merged.Merge(b.hist)
		b.histMu.Unlock()
	}

	return firerMetrics{
		TicksSent:    f.ticksSent.Load(),
		TicksAcked:   f.ticksAcked.Load(),
		P50LatencyUs: merged.ValueAtQuantile(50),
		P90LatencyUs: merged.ValueAtQuantile(90),
		P99LatencyUs: merged.ValueAtQuantile(99),
		TimedOut:     f.timedOut.Load(),
	}
}

func splitTicks(ticks []workload.Tick, n int) [][]workload.Tick {
	shards := make([][]workload.Tick, n)
	size := len(ticks) / n
	for i := 0; i < n-1; i++ {
		shards[i] = ticks[i*size : (i+1)*size]
	}
	shards[n-1] = ticks[(n-1)*size:]
	return shards
}

func protoToWorkload(ticks []*proto.Tick) []workload.Tick {
	out := make([]workload.Tick, len(ticks))
	for i, t := range ticks {
		out[i] = workload.Tick{
			Type:    workload.TickType(t.Type),
			OrderID: t.OrderId,
			Side:    byte(t.Side),
			OrdType: byte(t.OrdType),
			Price:   t.Price,
			Qty:     t.Qty,
		}
	}
	return out
}
