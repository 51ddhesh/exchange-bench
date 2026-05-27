package runner

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/51ddhesh/exchange-bench/internal/protocol"
	"github.com/51ddhesh/exchange-bench/internal/workload"

	"github.com/HdrHistogram/hdrhistogram-go"
)

// TickResult is the unit of output from the runner's dispatch loop.
// One is produced per tick, even on error.
//
// Error is non-nil only when the pipe is dead (io.EOF from the sandbox stdout,
// or a write failure on sandbox stdin). Parse errors from malformed contestant
// output are NOT surfaced here — the validator detects them by finding no valid
// ACK/REJ in Responses.
type TickResult struct {
	Tick       workload.Tick
	Responses  []protocol.Response // FILLs collected before the ACK/REJ, then the ACK/REJ itself
	IntendedAt int64               // time.Now().UnixNano() at the ticker fire — for coordinated omission
	ReceivedAt int64               // time.Now().UnixNano() immediately after ACK/REJ received
	Error      error               // nil unless pipe is dead
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

// Runner drives the dispatch loop: writes ticks to the sandbox stdin at the
// requested rate, collects responses from stdout, records latency, and streams
// TickResults to the validator via Results().
type Runner struct {
	sb      Sandbox
	reader  *protocol.Reader
	hist    *hdrhistogram.Histogram
	results chan TickResult
}

// New constructs a Runner over an already-started Sandbox.
// The caller is responsible for starting the sandbox (StartSandbox) before
// passing it here, and for calling sb.Kill() after Run returns.
func New(sb Sandbox) *Runner {
	return &Runner{
		sb:     sb,
		reader: protocol.NewReader(sb.Stdout()),
		// 1µs–60s range, 3 significant figures. Covers both sub-millisecond
		// HFT responses and pathologically slow contestants without overflow.
		hist:    hdrhistogram.New(1, 60_000_000, 3),
		results: make(chan TickResult, 4096),
	}
}

// Results returns the read-only end of the TickResult channel.
// The channel is closed when Run returns. The validator must drain it fully.
func (r *Runner) Results() <-chan TickResult {
	return r.results
}

// Run sends ticks to the sandbox at ratePerSec ticks/second, records latency,
// and streams TickResults. Returns RunMetrics and any terminal error.
//
// The 60-second wall-clock timeout must be enforced by the caller:
//
//	ctx, cancel = context.WithTimeout(parent, 60*time.Second)
//	defer cancel()
//
// Run owns the results channel and closes it before returning. Do not send
// to r.results after Run returns.
func (r *Runner) Run(ctx context.Context, ticks []workload.Tick, ratePerSec int) (RunMetrics, error) {
	defer close(r.results)

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

		if err := protocol.WriteTick(r.sb.Stdin(), tick); err != nil {
			// Pipe to contestant is dead. No point continuing.
			return metrics, err
		}

		responses, receivedAt, err := r.collectUntilACK()

		metrics.TicksSent++

		if err != nil {
			// Pipe closed while waiting for ACK. Emit the partial result
			// so the validator has an accurate tick count, then stop.
			r.results <- TickResult{
				Tick:       tick,
				Responses:  responses,
				IntendedAt: intendedAt,
				ReceivedAt: receivedAt,
				Error:      err,
			}
			return metrics, nil
		}

		// Record latency using intendedAt (not actual send time) so coordinated
		// omission is captured correctly: if the ticker fires late because the
		// previous tick was slow, the intended slot time is still what matters.
		deltaUs := (receivedAt - intendedAt) / 1000
		if deltaUs < 1 {
			deltaUs = 1 // HDR histogram minimum is 1
		}
		r.hist.RecordValue(deltaUs) //nolint:errcheck — only fails if value > max (60s), which means the contestant is broken anyway

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

	metrics.P50LatencyUs = r.hist.ValueAtQuantile(50)
	metrics.P90LatencyUs = r.hist.ValueAtQuantile(90)
	metrics.P99LatencyUs = r.hist.ValueAtQuantile(99)
	return metrics, nil
}

// collectUntilACK reads response lines from the contestant until an ACK or REJ
// is received, or the pipe closes. FILL lines are accumulated into the slice.
//
// On a parse error the loop stops immediately. The caller gets whatever
// responses were collected so far with a nil error — the missing ACK/REJ tells
// the validator this tick was incorrect, and the dispatch loop continues with
// the next tick rather than stalling waiting for an ACK that will never come.
//
// On io.EOF or a real scan error, the second return value is 0 and the error
// is non-nil — the dispatch loop treats this as a dead pipe and stops.
func (r *Runner) collectUntilACK() ([]protocol.Response, int64, error) {
	var responses []protocol.Response

	for {
		resp, err := r.reader.ReadResponse()

		if err == io.EOF {
			return responses, 0, io.EOF
		}
		if err != nil {
			// Parse errors (malformed lines) are non-fatal — return
			// whatever we have so far and let the dispatch loop continue.
			var parseErr *protocol.ParseError
			if errors.As(err, &parseErr) {
				return responses, time.Now().UnixNano(), nil
			}
			// Scanner I/O error: treat as dead pipe.
			return responses, 0, err
		}

		// Note: parse errors from malformed lines are returned by ReadResponse
		// as (Response{}, err). We only reach here when err == nil, which means
		// ReadResponse handed us a successfully parsed response — including a
		// parse-error sentinel? No: re-reading reader.go: parse errors return
		// (Response{}, non-nil-err). So this branch only fires on io.EOF and
		// real scan errors. Parse errors fall into the err != nil branch above
		// and break the loop — correct behaviour.
		responses = append(responses, resp)

		if resp.Type == protocol.RespACK || resp.Type == protocol.RespREJ {
			return responses, time.Now().UnixNano(), nil
		}
	}
}

// isAcked returns true if the last response in the slice is an ACK.
// A REJ means the contestant processed the tick but rejected it — not an ack.
func isAcked(responses []protocol.Response) bool {
	if len(responses) == 0 {
		return false
	}
	return responses[len(responses)-1].Type == protocol.RespACK
}
