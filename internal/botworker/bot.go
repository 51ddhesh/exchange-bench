package botworker

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/51ddhesh/exchange-bench/internal/protocol"
	"github.com/51ddhesh/exchange-bench/internal/workload"
	hdrhistogram "github.com/HdrHistogram/hdrhistogram-go"
	"github.com/coder/websocket"
)

// inFlightEntry stores what the writeLoop stamped when it sent a tick.
// Keyed by OrderID in the inf sync.Map. Deleted when the terminal
// ACK/REJ arrives in readLoop.
type inFlightEntry struct {
	intendedAt int64
	tick       workload.Tick
}

type bot struct {
	id         int
	ticks      []workload.Tick
	endpoint   string
	botCount   int
	rate       *atomic.Int32
	botEvtCh   chan<- TelemetryEvent // closed by run() via defer
	hist       *hdrhistogram.Histogram
	histMu     *sync.Mutex
	ticksSent  *atomic.Int64
	ticksAcked *atomic.Int64
}

// run dials the contestant, starts concurrent writeLoop and readLoop, waits
// 500ms after writing finishes for in-flight responses to drain, then
// cancels the connection. Closes botEvtCh when done.
func (b *bot) run(ctx context.Context) {
	defer close(b.botEvtCh)

	conn, _, err := websocket.Dial(ctx, b.endpoint, nil)
	if err != nil {
		return
	}

	botCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer conn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck

	reader := protocol.NewReader(conn, botCtx)
	var inf sync.Map // orderID → inFlightEntry

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		b.readLoop(botCtx, reader, &inf)
	}()

	b.writeLoop(botCtx, conn, &inf)
	time.Sleep(500 * time.Millisecond)
	cancel()
	wg.Wait()
}

func (b *bot) writeLoop(ctx context.Context, conn *websocket.Conn, inf *sync.Map) {
	lastRate := b.rate.Load()
	ticker := time.NewTicker(botInterval(lastRate, b.botCount))
	defer ticker.Stop()

	for _, tick := range b.ticks {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		if r := b.rate.Load(); r != lastRate {
			lastRate = r
			ticker.Reset(botInterval(r, b.botCount))
		}

		intendedAt := time.Now().UnixNano()
		inf.Store(tick.OrderID, inFlightEntry{intendedAt: intendedAt, tick: tick})

		if err := protocol.WriteTick(ctx, conn, tick); err != nil {
			return
		}
		b.ticksSent.Add(1)
	}
}

// readLoop accumulates FILL responses per order and emits one TelemetryEvent
// per terminal ACK/REJ. The fills map is local and only touched by this
// goroutine — no synchronization needed.
func (b *bot) readLoop(ctx context.Context, reader *protocol.Reader, inf *sync.Map) {
	fills := make(map[string][]protocol.Response)

	for {
		resp, err := reader.ReadResponse()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			var pe *protocol.ParseError
			if errors.As(err, &pe) {
				continue
			}
			return
		}

		if resp.Type == protocol.RespFILL {
			fills[resp.OrderID] = append(fills[resp.OrderID], resp)
			continue
		}

		// Terminal response — ACK or REJ.
		receivedAt := time.Now().UnixNano()

		var intendedAt int64
		var tick workload.Tick
		if v, ok := inf.LoadAndDelete(resp.OrderID); ok {
			e := v.(inFlightEntry)
			intendedAt = e.intendedAt
			tick = e.tick
		}

		if intendedAt == 0 {
			// Spurious response — no matching in-flight tick; discard.
			delete(fills, resp.OrderID)
			continue
		}

		deltaUs := (receivedAt - intendedAt) / 1000
		if deltaUs < 1 {
			deltaUs = 1
		}
		b.histMu.Lock()
		b.hist.RecordValue(deltaUs) //nolint:errcheck
		b.histMu.Unlock()

		acked := resp.Type == protocol.RespACK
		if acked {
			b.ticksAcked.Add(1)
		}

		// Bundle accumulated fills + terminal response. Protocol guarantees
		// FILLs arrive before ACK, so fills[resp.OrderID] is complete here.
		allResponses := append(fills[resp.OrderID], resp)
		delete(fills, resp.OrderID)

		b.botEvtCh <- TelemetryEvent{
			OrderID:      resp.OrderID,
			IntendedAtNs: intendedAt,
			ReceivedAtNs: receivedAt,
			Acked:        acked,
			Tick:         tick,
			Responses:    allResponses,
		}
	}
}

// botInterval computes the per-bot tick interval from the aggregate rate.
func botInterval(ratePerSec int32, botCount int) time.Duration {
	if ratePerSec <= 0 || botCount <= 0 {
		return time.Second
	}
	perBotRate := int(ratePerSec) / botCount
	if perBotRate < 1 {
		perBotRate = 1
	}
	return time.Second / time.Duration(perBotRate)
}
