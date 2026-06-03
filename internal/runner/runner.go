package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/51ddhesh/exchange-bench/internal/protocol"
	"github.com/51ddhesh/exchange-bench/internal/workload"
	"github.com/HdrHistogram/hdrhistogram-go"
	"github.com/coder/websocket"
)

// TickResult is the unit of output from the runner's dispatch loop.
// One is produced per tick, even on error.
//
// Error is non-nil only when the WebSocket connection is dead.
// Parse errors from malformed contestant output are NOT surfaced here —
// the validator detects them by finding no valid ACK/REJ in Responses.
type TickResult struct {
	Tick       workload.Tick
	Responses  []protocol.Response
	IntendedAt int64
	ReceivedAt int64
	Error      error
}

// RunMetrics is populated by Run. CapacityTPS and TicksCorrect are zero until
// the scorer fills them in after consuming all TickVerdicts from the validator.
type RunMetrics struct {
	P50LatencyUs int64
	P90LatencyUs int64
	P99LatencyUs int64
	PeakTPS      float64
	CapacityTPS  float64 // set by scorer
	TicksSent    int64
	TicksAcked   int64
	TicksCorrect int64 // set by scorer
	TimedOut     bool
}

// Runner drives the single-bot closed-loop dispatch for the smoke test.
// It dials the sandbox WebSocket endpoint, sends each tick, and waits for
// the ACK/REJ before sending the next tick.
//
// Closed-loop semantics are intentional here: the smoke test measures
// correctness, not throughput. The distributed fleet (Step 4) fires
// open-loop across 1,000 bots.
type Runner struct {
	sb      Sandbox
	results chan TickResult
}

// New constructs a Runner over an already-started Sandbox.
func New(sb Sandbox) *Runner {
	return &Runner{
		sb:      sb,
		results: make(chan TickResult, 4096),
	}
}

// Results returns the read-only end of the TickResult channel.
// The channel is closed when Run returns. The validator must drain it fully.
func (r *Runner) Results() <-chan TickResult {
	return r.results
}

// Run dials the sandbox WebSocket endpoint and drives the closed-loop dispatch.
// Returns RunMetrics and any terminal error.
//
// The caller is responsible for enforcing a wall-clock timeout via ctx.
func (r *Runner) Run(ctx context.Context, ticks []workload.Tick, ratePerSec int) (RunMetrics, error) {
	defer close(r.results)

	conn, _, err := websocket.Dial(ctx, r.sb.Endpoint(), nil)
	if err != nil {
		return RunMetrics{}, fmt.Errorf("runner: dial %s: %w", r.sb.Endpoint(), err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck

	reader := protocol.NewReader(conn, ctx)
	hist := hdrhistogram.New(1, 60_000_000, 3)

	var metrics RunMetrics
	interval := time.Second / time.Duration(ratePerSec)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for _, tick := range ticks {
		select {
		case <-ctx.Done():
			metrics.TimedOut = true
			return metrics, ctx.Err()
		case <-ticker.C:
		}

		intendedAt := time.Now().UnixNano()

		if err := protocol.WriteTick(ctx, conn, tick); err != nil {
			return metrics, err
		}

		responses, receivedAt, err := collectUntilACK(reader)
		metrics.TicksSent++

		if err != nil {
			r.results <- TickResult{
				Tick:       tick,
				Responses:  responses,
				IntendedAt: intendedAt,
				ReceivedAt: receivedAt,
				Error:      err,
			}
			return metrics, nil
		}

		// Use intendedAt (not actual send time) for coordinated-omission-correct
		// latency: if the ticker fires late, the intended slot time still matters.
		deltaUs := (receivedAt - intendedAt) / 1000
		if deltaUs < 1 {
			deltaUs = 1
		}
		hist.RecordValue(deltaUs) //nolint:errcheck

		if isAcked(responses) {
			metrics.TicksAcked++
		}

		r.results <- TickResult{
			Tick:       tick,
			Responses:  responses,
			IntendedAt: intendedAt,
			ReceivedAt: receivedAt,
		}
	}

	metrics.P50LatencyUs = hist.ValueAtQuantile(50)
	metrics.P90LatencyUs = hist.ValueAtQuantile(90)
	metrics.P99LatencyUs = hist.ValueAtQuantile(99)
	return metrics, nil
}

// collectUntilACK reads response frames until ACK/REJ or connection closes.
// FILL frames are accumulated. Parse errors are non-fatal — the loop returns
// whatever was collected so the validator can mark the tick incorrect.
func collectUntilACK(reader *protocol.Reader) ([]protocol.Response, int64, error) {
	var responses []protocol.Response

	for {
		resp, err := reader.ReadResponse()

		if err == io.EOF {
			return responses, 0, io.EOF
		}
		if err != nil {
			var parseErr *protocol.ParseError
			if errors.As(err, &parseErr) {
				return responses, time.Now().UnixNano(), nil
			}
			return responses, 0, err
		}

		responses = append(responses, resp)

		if resp.Type == protocol.RespACK || resp.Type == protocol.RespREJ {
			return responses, time.Now().UnixNano(), nil
		}
	}
}

// isAcked returns true if the last response in the slice is an ACK.
func isAcked(responses []protocol.Response) bool {
	if len(responses) == 0 {
		return false
	}
	return responses[len(responses)-1].Type == protocol.RespACK
}
