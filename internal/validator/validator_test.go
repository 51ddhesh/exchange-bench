package validator

import (
	"testing"

	"github.com/51ddhesh/exchange-bench/internal/protocol"
	"github.com/51ddhesh/exchange-bench/internal/runner"
	"github.com/51ddhesh/exchange-bench/internal/workload"
)

// runVerdicts is the test helper: pipes results into a fresh Validator and
// collects all verdicts synchronously. No runner, no Docker required.
func runVerdicts(t *testing.T, results []runner.TickResult) []TickVerdict {
	t.Helper()
	v := New()
	ch := make(chan runner.TickResult, len(results))
	for _, r := range results {
		ch <- r
	}
	close(ch)

	var out []TickVerdict
	for vd := range v.Consume(ch) {
		out = append(out, vd)
	}
	return out
}

// ── tick constructors ────────────────────────────────────────────────────────

func addResult(id string, side byte, ordType byte, price, qty int64, responses ...protocol.Response) runner.TickResult {
	return runner.TickResult{
		Tick: workload.Tick{
			Type:    workload.TickAdd,
			OrderID: id,
			Side:    side,
			OrdType: ordType,
			Price:   price,
			Qty:     qty,
		},
		Responses: responses,
	}
}

func cancelResult(id string, responses ...protocol.Response) runner.TickResult {
	return runner.TickResult{
		Tick:      workload.Tick{Type: workload.TickCancel, OrderID: id},
		Responses: responses,
	}
}

func ack(id string) protocol.Response {
	return protocol.Response{Type: protocol.RespACK, OrderID: id}
}

func rej(id string) protocol.Response {
	return protocol.Response{Type: protocol.RespREJ, OrderID: id, Reason: "test"}
}

func fill(orderID, maker, taker string, price, qty int64) protocol.Response {
	return protocol.Response{
		Type:         protocol.RespFILL,
		OrderID:      orderID,
		MakerOrderID: maker,
		TakerOrderID: taker,
		ExecPrice:    price,
		ExecQty:      qty,
	}
}

// ── tests ────────────────────────────────────────────────────────────────────

// TestCorrectNoFill: a resting limit order with no counterpart — contestant
// correctly ACKs with no fills. Reference book also produces no fills.
func TestCorrectNoFill(t *testing.T) {
	verdicts := runVerdicts(t, []runner.TickResult{
		addResult("o1", 'B', 'L', 1_000_000, 10, ack("o1")),
	})
	if !verdicts[0].Correct {
		t.Errorf("want correct, got %v: %s", verdicts[0].Violation, verdicts[0].Detail)
	}
}

// TestCorrectWithFill: resting buy, then a matching sell. Contestant correctly
// reports FILL then ACK. Reference produces exactly the same fill.
func TestCorrectWithFill(t *testing.T) {
	verdicts := runVerdicts(t, []runner.TickResult{
		addResult("o1", 'B', 'L', 1_000_000, 10, ack("o1")),
		addResult("o2", 'S', 'L', 1_000_000, 10,
			fill("o2", "o1", "o2", 1_000_000, 10),
			ack("o2"),
		),
	})
	if !verdicts[0].Correct {
		t.Errorf("tick 0 want correct, got %v", verdicts[0].Violation)
	}
	if !verdicts[1].Correct {
		t.Errorf("tick 1 want correct, got %v: %s", verdicts[1].Violation, verdicts[1].Detail)
	}
}

// TestCorrectCancel: buy rests, then is cancelled. Contestant ACKs the cancel.
func TestCorrectCancel(t *testing.T) {
	verdicts := runVerdicts(t, []runner.TickResult{
		addResult("o1", 'B', 'L', 1_000_000, 10, ack("o1")),
		cancelResult("o1", ack("o1")),
	})
	if !verdicts[1].Correct {
		t.Errorf("cancel want correct, got %v: %s", verdicts[1].Violation, verdicts[1].Detail)
	}
}

// TestOverfill: contestant reports 2 fills but reference only generates 1.
func TestOverfill(t *testing.T) {
	verdicts := runVerdicts(t, []runner.TickResult{
		addResult("o1", 'B', 'L', 1_000_000, 10, ack("o1")),
		addResult("o2", 'S', 'L', 1_000_000, 10,
			fill("o2", "o1", "o2", 1_000_000, 10),
			fill("o2", "o99", "o2", 1_000_000, 1), // phantom extra fill
			ack("o2"),
		),
	})
	if verdicts[1].Violation != ViolationOverfill {
		t.Errorf("want OVERFILL, got %v: %s", verdicts[1].Violation, verdicts[1].Detail)
	}
}

// TestUnderfill: reference generates a fill but contestant reports none.
func TestUnderfill(t *testing.T) {
	verdicts := runVerdicts(t, []runner.TickResult{
		addResult("o1", 'B', 'L', 1_000_000, 10, ack("o1")),
		// Contestant only ACKs — no FILL emitted.
		addResult("o2", 'S', 'L', 1_000_000, 10, ack("o2")),
	})
	if verdicts[1].Violation != ViolationUnderfill {
		t.Errorf("want UNDERFILL, got %v: %s", verdicts[1].Violation, verdicts[1].Detail)
	}
}

// TestPriceTimePriority: two buys at the same price; sell should match the
// first (time priority). Contestant reports the second as maker instead.
func TestPriceTimePriority(t *testing.T) {
	verdicts := runVerdicts(t, []runner.TickResult{
		addResult("o1", 'B', 'L', 1_000_000, 10, ack("o1")), // first in queue
		addResult("o2", 'B', 'L', 1_000_000, 10, ack("o2")), // second in queue
		addResult("o3", 'S', 'L', 1_000_000, 10,
			fill("o3", "o2", "o3", 1_000_000, 10), // wrong maker: o2 instead of o1
			ack("o3"),
		),
	})
	if verdicts[2].Violation != ViolationPriceTime {
		t.Errorf("want PRICE_TIME_PRIORITY, got %v: %s", verdicts[2].Violation, verdicts[2].Detail)
	}
}

// TestWrongExecPrice: contestant reports the taker's price instead of the
// maker's resting price.
func TestWrongExecPrice(t *testing.T) {
	// Maker rests at 100.0000 (1_000_000). Taker arrives at 100.5000 (1_005_000).
	// Exec price must be the maker's price: 1_000_000.
	verdicts := runVerdicts(t, []runner.TickResult{
		addResult("o1", 'S', 'L', 1_000_000, 10, ack("o1")),
		addResult("o2", 'B', 'L', 1_005_000, 10,
			fill("o2", "o1", "o2", 1_005_000, 10), // wrong price: taker's instead of maker's
			ack("o2"),
		),
	})
	if verdicts[1].Violation != ViolationWrongPrice {
		t.Errorf("want WRONG_EXEC_PRICE, got %v: %s", verdicts[1].Violation, verdicts[1].Detail)
	}
}

// TestZombieFill: buy rests, gets cancelled, then sell arrives. Reference
// produces no fills. Contestant incorrectly reports a fill against the
// cancelled maker.
func TestZombieFill(t *testing.T) {
	verdicts := runVerdicts(t, []runner.TickResult{
		addResult("o1", 'B', 'L', 1_000_000, 10, ack("o1")),
		cancelResult("o1", ack("o1")),
		addResult("o2", 'S', 'L', 1_000_000, 10,
			fill("o2", "o1", "o2", 1_000_000, 10), // o1 was cancelled
			ack("o2"),
		),
	})
	if verdicts[2].Violation != ViolationZombieFill {
		t.Errorf("want ZOMBIE_FILL, got %v: %s", verdicts[2].Violation, verdicts[2].Detail)
	}
}

// TestCancelMismatch_OKButREJed: reference says CancelOK but contestant REJs.
func TestCancelMismatch_OKButREJed(t *testing.T) {
	verdicts := runVerdicts(t, []runner.TickResult{
		addResult("o1", 'B', 'L', 1_000_000, 10, ack("o1")),
		cancelResult("o1", rej("o1")), // contestant wrongly rejects a valid cancel
	})
	if verdicts[1].Violation != ViolationCancelMismatch {
		t.Errorf("want CANCEL_MISMATCH, got %v: %s", verdicts[1].Violation, verdicts[1].Detail)
	}
}

// TestCancelMismatch_NotFoundButACKed: reference says CancelNotFound (order
// never existed) but contestant ACKs.
func TestCancelMismatch_NotFoundButACKed(t *testing.T) {
	verdicts := runVerdicts(t, []runner.TickResult{
		// Cancel an order that was never added.
		cancelResult("ghost", ack("ghost")),
	})
	if verdicts[0].Violation != ViolationCancelMismatch {
		t.Errorf("want CANCEL_MISMATCH, got %v: %s", verdicts[0].Violation, verdicts[0].Detail)
	}
}

// TestCancelNotFound_BothAgree: reference and contestant both say not found.
func TestCancelNotFound_BothAgree(t *testing.T) {
	verdicts := runVerdicts(t, []runner.TickResult{
		cancelResult("ghost", rej("ghost")),
	})
	if !verdicts[0].Correct {
		t.Errorf("want correct, got %v: %s", verdicts[0].Violation, verdicts[0].Detail)
	}
}

// TestMultiLevelSweepCorrect: sell sweeps two price levels. Contestant
// correctly reports both fills in price-time order.
func TestMultiLevelSweepCorrect(t *testing.T) {
	verdicts := runVerdicts(t, []runner.TickResult{
		addResult("o1", 'B', 'L', 1_010_000, 5, ack("o1")), // best bid
		addResult("o2", 'B', 'L', 1_000_000, 5, ack("o2")), // second best
		addResult("o3", 'S', 'L', 1_000_000, 8,
			fill("o3", "o1", "o3", 1_010_000, 5),
			fill("o3", "o2", "o3", 1_000_000, 3),
			ack("o3"),
		),
	})
	if !verdicts[2].Correct {
		t.Errorf("multi-level sweep want correct, got %v: %s", verdicts[2].Violation, verdicts[2].Detail)
	}
}

// TestMultiLevelSweepWrongOrder: contestant reports fills in the wrong
// price-time order (sweeps lower level first).
func TestMultiLevelSweepWrongOrder(t *testing.T) {
	verdicts := runVerdicts(t, []runner.TickResult{
		addResult("o1", 'B', 'L', 1_010_000, 5, ack("o1")),
		addResult("o2", 'B', 'L', 1_000_000, 5, ack("o2")),
		addResult("o3", 'S', 'L', 1_000_000, 8,
			fill("o3", "o2", "o3", 1_000_000, 3), // wrong: lower level first
			fill("o3", "o1", "o3", 1_010_000, 5),
			ack("o3"),
		),
	})
	if verdicts[2].Correct {
		t.Error("wrong sweep order should fail")
	}
}

// TestMarketOrderIOC: market sell against an empty book. No fills expected.
// Contestant correctly returns no fills and ACKs.
func TestMarketOrderIOC(t *testing.T) {
	verdicts := runVerdicts(t, []runner.TickResult{
		addResult("o1", 'S', 'M', 0, 10, ack("o1")),
	})
	if !verdicts[0].Correct {
		t.Errorf("IOC on empty book want correct, got %v: %s", verdicts[0].Violation, verdicts[0].Detail)
	}
}

// TestPartialFill: buy for 10, sell for 6. Contestant correctly reports one
// partial fill. Buy should still rest with qty 4 (checked via a second sell).
func TestPartialFill(t *testing.T) {
	verdicts := runVerdicts(t, []runner.TickResult{
		addResult("o1", 'B', 'L', 1_000_000, 10, ack("o1")),
		addResult("o2", 'S', 'L', 1_000_000, 6,
			fill("o2", "o1", "o2", 1_000_000, 6),
			ack("o2"),
		),
		// o1 should still be resting with 4 remaining; a sell of 4 should match.
		addResult("o3", 'S', 'L', 1_000_000, 4,
			fill("o3", "o1", "o3", 1_000_000, 4),
			ack("o3"),
		),
	})
	for i, vd := range verdicts {
		if !vd.Correct {
			t.Errorf("tick %d want correct, got %v: %s", i, vd.Violation, vd.Detail)
		}
	}
}
