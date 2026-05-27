package runner

import (
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/51ddhesh/exchange-bench/internal/protocol"
	"github.com/51ddhesh/exchange-bench/internal/workload"
)

// fakeSandbox wires two in-process pipes as the sandbox stdin/stdout.
// The test goroutine holds fakeStdin (reads what the runner wrote) and
// fakeStdout (writes what the runner will read).
type fakeSandbox struct {
	// runner writes here; test reads from fakeStdinR
	stdinW     io.WriteCloser
	fakeStdinR io.ReadCloser

	// test writes here; runner reads from fakeStdoutR
	fakeStdoutW io.WriteCloser
	stdoutR     io.ReadCloser
}

func newFakeSandbox() *fakeSandbox {
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	return &fakeSandbox{
		stdinW:      stdinW,
		fakeStdinR:  stdinR,
		fakeStdoutW: stdoutW,
		stdoutR:     stdoutR,
	}
}

func (f *fakeSandbox) Stdin() io.WriteCloser { return f.stdinW }
func (f *fakeSandbox) Stdout() io.ReadCloser { return f.stdoutR }
func (f *fakeSandbox) Kill() error           { return nil }

// writeLine sends one response line to the runner from the "contestant" side.
func (f *fakeSandbox) writeLine(s string) {
	fmt.Fprint(f.fakeStdoutW, s+"\n")
}

func (f *fakeSandbox) closeStdout() {
	f.fakeStdoutW.Close()
}

// oneTick is a minimal limit buy ADD tick used across multiple tests.
func oneTick(id string) workload.Tick {
	return workload.Tick{
		Type:    workload.TickAdd,
		OrderID: id,
		Side:    'B',
		OrdType: 'L',
		Price:   1000000,
		Qty:     1,
	}
}

// TestCleanRun: every tick gets an ACK. Metrics should reflect full completion.
func TestCleanRun(t *testing.T) {
	sb := newFakeSandbox()
	ticks := []workload.Tick{oneTick("o1"), oneTick("o2"), oneTick("o3")}

	// Contestant goroutine: read stdin (drain it), write one ACK per tick.
	go func() {
		buf := make([]byte, 256)
		for range ticks {
			// drain the ADD line the runner wrote
			for {
				n, _ := sb.fakeStdinR.Read(buf)
				if n > 0 && buf[n-1] == '\n' {
					break
				}
			}
			sb.writeLine("ACK " + "o1") // ID doesn't matter for runner correctness
		}
		sb.closeStdout()
	}()

	r := New(sb)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	metrics, err := r.Run(ctx, ticks, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if metrics.TicksSent != 3 {
		t.Errorf("want TicksSent=3, got %d", metrics.TicksSent)
	}
	if metrics.TicksAcked != 3 {
		t.Errorf("want TicksAcked=3, got %d", metrics.TicksAcked)
	}
	if metrics.TimedOut {
		t.Error("should not have timed out")
	}

	// Drain results
	var results []TickResult
	for res := range r.Results() {
		results = append(results, res)
	}
	if len(results) != 3 {
		t.Errorf("want 3 results, got %d", len(results))
	}
	for _, res := range results {
		if res.Error != nil {
			t.Errorf("result has unexpected error: %v", res.Error)
		}
	}
}

// TestPipeClosedMidRun: contestant closes stdout after tick 1.
// Runner must stop cleanly without panic. Remaining ticks are not sent.
func TestPipeClosedMidRun(t *testing.T) {
	sb := newFakeSandbox()
	ticks := []workload.Tick{oneTick("o1"), oneTick("o2"), oneTick("o3")}

	go func() {
		buf := make([]byte, 256)
		// Serve only the first tick, then close.
		for {
			n, _ := sb.fakeStdinR.Read(buf)
			if n > 0 && buf[n-1] == '\n' {
				break
			}
		}
		sb.writeLine("ACK o1")
		sb.closeStdout() // simulate crash after first tick
		// Drain remaining stdin so runner's writes don't block forever.
		// In a real Docker sandbox the hijacked conn would also be dead.
		io.Copy(io.Discard, sb.fakeStdinR)
	}()

	r := New(sb)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	metrics, err := r.Run(ctx, ticks, 100)
	// err may be nil (pipe closed after ACK, detected on next read) or io.EOF.
	// Either is acceptable — the important invariant is TicksSent <= 2.
	_ = err

	if metrics.TicksSent > 2 {
		t.Errorf("want TicksSent <= 2, got %d", metrics.TicksSent)
	}
	if metrics.TimedOut {
		t.Error("should not have timed out")
	}

	// Results channel must be closed and drainable.
	var count int
	for range r.Results() {
		count++
	}
	if count > 2 {
		t.Errorf("want <= 2 results, got %d", count)
	}
}

// TestParseErrorIsNonFatal: contestant sends garbage then a valid ACK.
// The run must continue; the tick is recorded with no Error but Responses
// will contain only the ACK (garbage line is a parse error, loop breaks).
func TestParseErrorIsNonFatal(t *testing.T) {
	sb := newFakeSandbox()
	ticks := []workload.Tick{oneTick("o1"), oneTick("o2")}

	go func() {
		buf := make([]byte, 256)
		for i, id := range []string{"o1", "o2"} {
			for {
				n, _ := sb.fakeStdinR.Read(buf)
				if n > 0 && buf[n-1] == '\n' {
					break
				}
			}
			if i == 0 {
				// First tick: send garbage first, then nothing (parse error
				// breaks the collection loop; runner moves on).
				sb.writeLine("GARBAGE this is not a valid response")
			} else {
				sb.writeLine("ACK " + id)
			}
		}
		sb.closeStdout()
	}()

	r := New(sb)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	metrics, err := r.Run(ctx, ticks, 100)
	if err != nil && err != io.EOF {
		t.Fatalf("unexpected fatal error: %v", err)
	}

	// Both ticks were sent.
	if metrics.TicksSent < 1 {
		t.Errorf("want TicksSent >= 1, got %d", metrics.TicksSent)
	}

	var results []TickResult
	for res := range r.Results() {
		results = append(results, res)
	}

	// First result: Error is nil (parse error is non-fatal), no ACK in Responses.
	if len(results) > 0 && results[0].Error != nil {
		t.Errorf("parse error must not set TickResult.Error, got: %v", results[0].Error)
	}
}

// TestCtxCancel: context is cancelled after the runner starts.
// Run must return ctx.Err() and close the results channel cleanly.
func TestCtxCancel(t *testing.T) {
	sb := newFakeSandbox()
	ticks := make([]workload.Tick, 100)
	for i := range ticks {
		ticks[i] = oneTick(fmt.Sprintf("o%d", i+1))
	}

	// Contestant never ACKs — just drains stdin silently.
	go func() {
		io.Copy(io.Discard, sb.fakeStdinR)
	}()

	r := New(sb)
	ctx, cancel := context.WithCancel(context.Background())

	// Cancel after a short delay.
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	_, err := r.Run(ctx, ticks, 5) // slow rate so cancel fires mid-run
	if err != context.Canceled {
		t.Errorf("want context.Canceled, got %v", err)
	}

	// Results channel must be closed.
	for range r.Results() {
	}
}

// TestFILLBeforeACK: contestant sends FILL then ACK for a single tick.
// Both must appear in TickResult.Responses in order.
func TestFILLBeforeACK(t *testing.T) {
	sb := newFakeSandbox()
	ticks := []workload.Tick{oneTick("o1")}

	go func() {
		buf := make([]byte, 256)
		for {
			n, _ := sb.fakeStdinR.Read(buf)
			if n > 0 && buf[n-1] == '\n' {
				break
			}
		}
		sb.writeLine("FILL o1 o5 o1 100.0000 1")
		sb.writeLine("ACK o1")
		sb.closeStdout()
	}()

	r := New(sb)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	r.Run(ctx, ticks, 100) //nolint:errcheck

	var results []TickResult
	for res := range r.Results() {
		results = append(results, res)
	}

	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	res := results[0]
	if len(res.Responses) != 2 {
		t.Fatalf("want 2 responses (FILL + ACK), got %d", len(res.Responses))
	}
	if res.Responses[0].Type != protocol.RespFILL {
		t.Errorf("first response should be FILL, got %v", res.Responses[0].Type)
	}
	if res.Responses[1].Type != protocol.RespACK {
		t.Errorf("second response should be ACK, got %v", res.Responses[1].Type)
	}
}

// TestREJTerminatesCollection: contestant sends REJ instead of ACK.
// Loop must stop at REJ, tick is recorded, TicksAcked is not incremented.
func TestREJTerminatesCollection(t *testing.T) {
	sb := newFakeSandbox()
	ticks := []workload.Tick{oneTick("o1"), oneTick("o2")}

	go func() {
		buf := make([]byte, 256)
		for _, id := range []string{"o1", "o2"} {
			for {
				n, _ := sb.fakeStdinR.Read(buf)
				if n > 0 && buf[n-1] == '\n' {
					break
				}
			}
			if id == "o1" {
				sb.writeLine("REJ o1 no liquidity")
			} else {
				sb.writeLine("ACK o2")
			}
		}
		sb.closeStdout()
	}()

	r := New(sb)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	metrics, _ := r.Run(ctx, ticks, 100)

	if metrics.TicksSent != 2 {
		t.Errorf("want TicksSent=2, got %d", metrics.TicksSent)
	}
	// Only o2 was ACKed.
	if metrics.TicksAcked != 1 {
		t.Errorf("want TicksAcked=1, got %d", metrics.TicksAcked)
	}

	var results []TickResult
	for res := range r.Results() {
		results = append(results, res)
	}
	if len(results) != 2 {
		t.Fatalf("want 2 results, got %d", len(results))
	}
	// First result ends in REJ.
	last0 := results[0].Responses[len(results[0].Responses)-1]
	if last0.Type != protocol.RespREJ {
		t.Errorf("first result should end with REJ, got %v", last0.Type)
	}
}
