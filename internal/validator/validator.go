package validator

import (
	"context"
	"fmt"

	"github.com/51ddhesh/exchange-bench/internal/orderbook"
	"github.com/51ddhesh/exchange-bench/internal/protocol"
	"github.com/51ddhesh/exchange-bench/internal/runner"
	"github.com/51ddhesh/exchange-bench/internal/workload"
)

type ViolationType string

const (
	ViolationNone           ViolationType = ""
	ViolationOverfill       ViolationType = "OVERFILL"
	ViolationUnderfill      ViolationType = "UNDERFILL"
	ViolationPriceTime      ViolationType = "PRICE_TIME_PRIORITY"
	ViolationWrongPrice     ViolationType = "WRONG_EXEC_PRICE"
	ViolationZombieFill     ViolationType = "ZOMBIE_FILL"
	ViolationCancelMismatch ViolationType = "CANCEL_MISMATCH"
)

type TickVerdict struct {
	TickIndex int
	OrderID   string
	Correct   bool
	Violation ViolationType
	Detail    string
	RefFills  []orderbook.Fill
	GotFills  []protocol.Response
}

// Validator replays each TickResult through an independent reference engine
// and compares contestant output against reference truth.
//
// Sequencer independence invariant: this Validator owns its own Engine and
// Sequencer. No state is shared with the runner or any other component.
// The reference engine is driven exclusively by Consume's goroutine — one
// tick at a time, in the exact order ticks arrive on the results channel.
type Validator struct {
	engine       *orderbook.Engine
	seq          *orderbook.Sequencer
	engineCancel context.CancelFunc
}

func New() *Validator {
	return NewWithCapacity(1_000_000)
}

// NewWithCapacity creates a Validator with a pre-allocated arena of the given
// capacity. Use this for per-bot validators where 1M is wasteful; size to the
// shard length + headroom.
func NewWithCapacity(capacity int) *Validator {
	engine := orderbook.NewEngine(capacity)
	seq := orderbook.NewSequencer(engine)
	return &Validator{engine: engine, seq: seq}
}

// Consume starts the reference engine and processes results in a dedicated
// goroutine. Returns a verdict channel that is closed when results is drained.
//
// Call once per evaluation run. The engine goroutine exits when Consume's
// goroutine finishes draining results.
func (v *Validator) Consume(results <-chan runner.TickResult) <-chan TickVerdict {
	verdicts := make(chan TickVerdict, 4096)

	ctx, cancel := context.WithCancel(context.Background())
	v.engineCancel = cancel
	go v.engine.Run(ctx)

	go func() {
		defer close(verdicts)
		defer cancel()

		// cancelled tracks string IDs of successfully cancelled orders so that
		// subsequent FILL responses against those IDs are flagged ZOMBIE_FILL.
		cancelled := make(map[string]struct{})
		idx := 0

		for result := range results {
			var verdict TickVerdict
			switch result.Tick.Type {
			case workload.TickAdd:
				verdict = v.verifyAdd(idx, result, cancelled)
			case workload.TickCancel:
				verdict = v.verifyCancel(idx, result, cancelled)
			default:
				verdict = TickVerdict{
					TickIndex: idx,
					OrderID:   result.Tick.OrderID,
					Correct:   false,
					Violation: ViolationNone,
					Detail:    fmt.Sprintf("unknown tick type %d", result.Tick.Type),
				}
			}
			verdicts <- verdict
			idx++
		}
	}()

	return verdicts
}

// Reset cancels the current engine and replaces all internal state with a
// fresh instance. The engine's Run goroutine must have exited (or will exit
// shortly after the context cancel) before the new sequencer touches any
// shared state — in practice, call Reset only after Consume's results channel
// is fully drained and the verdicts channel is closed.
func (v *Validator) Reset() {
	if v.engineCancel != nil {
		v.engineCancel()
		v.engineCancel = nil
	}
	v.engine = orderbook.NewEngine(1_000_000)
	v.seq = orderbook.NewSequencer(v.engine)
}

// ── per-tick verification ────────────────────────────────────────────────────

func (v *Validator) verifyAdd(idx int, result runner.TickResult, cancelled map[string]struct{}) TickVerdict {
	tick := result.Tick

	refFills, _, _ := v.seq.Add(
		tick.OrderID,
		sideFromByte(tick.Side),
		ordTypeFromByte(tick.OrdType),
		tick.Price,
		tick.Qty,
	)

	// Extract FILL responses from contestant output.
	var gotFills []protocol.Response
	for _, resp := range result.Responses {
		if resp.Type == protocol.RespFILL {
			gotFills = append(gotFills, resp)
		}
	}

	// ZOMBIE_FILL: contestant filled a previously cancelled maker.
	// Check before counting, because a zombie fill is also an overfill but
	// the zombie classification is more specific and actionable.
	for _, f := range gotFills {
		if _, wasCancelled := cancelled[f.MakerOrderID]; wasCancelled {
			return TickVerdict{
				TickIndex: idx,
				OrderID:   tick.OrderID,
				Correct:   false,
				Violation: ViolationZombieFill,
				Detail:    fmt.Sprintf("fill references cancelled maker %q", f.MakerOrderID),
				RefFills:  refFills,
				GotFills:  gotFills,
			}
		}
	}

	// Count mismatch.
	if len(gotFills) > len(refFills) {
		return TickVerdict{
			TickIndex: idx,
			OrderID:   tick.OrderID,
			Correct:   false,
			Violation: ViolationOverfill,
			Detail:    fmt.Sprintf("expected %d fills, got %d", len(refFills), len(gotFills)),
			RefFills:  refFills,
			GotFills:  gotFills,
		}
	}
	if len(gotFills) < len(refFills) {
		return TickVerdict{
			TickIndex: idx,
			OrderID:   tick.OrderID,
			Correct:   false,
			Violation: ViolationUnderfill,
			Detail:    fmt.Sprintf("expected %d fills, got %d", len(refFills), len(gotFills)),
			RefFills:  refFills,
			GotFills:  gotFills,
		}
	}

	// Per-fill comparison in order. Exec price is the maker's resting price.
	for i, ref := range refFills {
		got := gotFills[i]
		refMakerExt := v.seq.ExternalID(ref.MakerOrderID)
		refTakerExt := v.seq.ExternalID(ref.TakerOrderID)

		if got.MakerOrderID != refMakerExt {
			return TickVerdict{
				TickIndex: idx,
				OrderID:   tick.OrderID,
				Correct:   false,
				Violation: ViolationPriceTime,
				Detail:    fmt.Sprintf("fill[%d]: want maker %q, got %q", i, refMakerExt, got.MakerOrderID),
				RefFills:  refFills,
				GotFills:  gotFills,
			}
		}
		if got.TakerOrderID != refTakerExt {
			return TickVerdict{
				TickIndex: idx,
				OrderID:   tick.OrderID,
				Correct:   false,
				Violation: ViolationPriceTime,
				Detail:    fmt.Sprintf("fill[%d]: want taker %q, got %q", i, refTakerExt, got.TakerOrderID),
				RefFills:  refFills,
				GotFills:  gotFills,
			}
		}
		if got.ExecPrice != ref.ExecPrice {
			return TickVerdict{
				TickIndex: idx,
				OrderID:   tick.OrderID,
				Correct:   false,
				Violation: ViolationWrongPrice,
				Detail:    fmt.Sprintf("fill[%d]: want price %d, got %d", i, ref.ExecPrice, got.ExecPrice),
				RefFills:  refFills,
				GotFills:  gotFills,
			}
		}
		if got.ExecQty != ref.ExecQty {
			v := ViolationUnderfill
			if got.ExecQty > ref.ExecQty {
				v = ViolationOverfill
			}
			return TickVerdict{
				TickIndex: idx,
				OrderID:   tick.OrderID,
				Correct:   false,
				Violation: v,
				Detail:    fmt.Sprintf("fill[%d]: want qty %d, got %d", i, ref.ExecQty, got.ExecQty),
				RefFills:  refFills,
				GotFills:  gotFills,
			}
		}
	}

	return TickVerdict{
		TickIndex: idx,
		OrderID:   tick.OrderID,
		Correct:   true,
		Violation: ViolationNone,
		RefFills:  refFills,
		GotFills:  gotFills,
	}
}

func (v *Validator) verifyCancel(idx int, result runner.TickResult, cancelled map[string]struct{}) TickVerdict {
	tick := result.Tick

	refResult := v.seq.Cancel(tick.OrderID)

	// Record successful cancels so future ADD ticks can detect zombie fills.
	if refResult == orderbook.CancelOK {
		cancelled[tick.OrderID] = struct{}{}
	}

	// Find the contestant's terminal response (ACK or REJ).
	var termType protocol.ResponseType
	for _, resp := range result.Responses {
		if resp.Type == protocol.RespACK || resp.Type == protocol.RespREJ {
			termType = resp.Type
			break
		}
	}

	wantACK := refResult == orderbook.CancelOK
	gotACK := termType == protocol.RespACK

	if wantACK != gotACK {
		return TickVerdict{
			TickIndex: idx,
			OrderID:   tick.OrderID,
			Correct:   false,
			Violation: ViolationCancelMismatch,
			Detail: fmt.Sprintf("ref=%v wantACK=%v gotType=%v",
				refResult, wantACK, termType),
		}
	}

	return TickVerdict{
		TickIndex: idx,
		OrderID:   tick.OrderID,
		Correct:   true,
		Violation: ViolationNone,
	}
}

func sideFromByte(b byte) orderbook.Side {
	if b == 'B' {
		return orderbook.Buy
	}
	return orderbook.Sell
}

func ordTypeFromByte(b byte) orderbook.OrderType {
	if b == 'L' {
		return orderbook.Limit
	}
	return orderbook.Market
}
