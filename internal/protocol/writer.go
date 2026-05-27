package protocol

import (
	"fmt"
	"io"

	"github.com/51ddhesh/exchange-bench/internal/workload"
)

// WriteTick serialises one Tick to a single \n-terminated ASCII line on w.
//
// Wire formats:
//
//	ADD <order_id> <side> <type> <price> <qty>\n
//	CAN <order_id>\n
//
// <price> is the ×10000 int64 expressed as a fixed-4-decimal string, e.g.
//
//	1000000 -> "100.0000"
//	1005000 -> "100.5000"
//	    100 -> "0.0100"
//	      0 -> "0.0000"   (market orders)
//
// No buffering is applied. The runner controls flushing.
// Returns the first write error encountered, if any.
func WriteTick(w io.Writer, tick workload.Tick) error {
	switch tick.Type {
	case workload.TickAdd:
		_, err := fmt.Fprintf(w, "ADD %s %c %c %s %d\n",
			tick.OrderID,
			tick.Side,
			tick.OrdType,
			formatPrice(tick.Price),
			tick.Qty,
		)
		return err

	case workload.TickCancel:
		_, err := fmt.Fprintf(w, "CAN %s\n", tick.OrderID)
		return err

	default:
		return fmt.Errorf("protocol/writer: unknown tick type %d", tick.Type)
	}
}

// formatPrice converts a ×10000 fixed-point int64 to its 4-decimal-place
// ASCII representation without using float64 arithmetic.
//
// Algorithm:
//
//	intPart  = price / 10000
//	fracPart = price % 10000  (always ≥ 0 because price ≥ 0 by invariant)
//
// %04d zero-pads the fractional part:
//
//	1000000 % 10000 = 0    → "0000"
//	1005000 % 10000 = 5000 → "5000"
//	    100 % 10000 = 100  → "0100"
func formatPrice(price int64) string {
	return fmt.Sprintf("%d.%04d", price/10000, price%10000)
}
