package protocol

import (
	"context"
	"fmt"

	"github.com/51ddhesh/exchange-bench/internal/workload"
	"github.com/coder/websocket"
)

// WriteTick serialises one Tick as a single WebSocket text frame on conn.
//
// Wire formats (no trailing newline — frame boundary replaces newline):
//
//	ADD <order_id> <side> <type> <price> <qty>
//	CAN <order_id>
//
// <price> is the ×10000 int64 expressed as a fixed-4-decimal string, e.g.
//
//	1000000 → "100.0000"
//	1005000 → "100.5000"
//	    100 → "0.0100"
//	      0 → "0.0000"  (market orders)
func WriteTick(ctx context.Context, conn *websocket.Conn, tick workload.Tick) error {
	var msg string
	switch tick.Type {
	case workload.TickAdd:
		msg = fmt.Sprintf("ADD %s %c %c %s %d",
			tick.OrderID,
			tick.Side,
			tick.OrdType,
			formatPrice(tick.Price),
			tick.Qty,
		)
	case workload.TickCancel:
		msg = fmt.Sprintf("CAN %s", tick.OrderID)
	default:
		return fmt.Errorf("protocol/writer: unknown tick type %d", tick.Type)
	}
	return conn.Write(ctx, websocket.MessageText, []byte(msg))
}

// formatPrice converts a ×10000 fixed-point int64 to its 4-decimal-place
// ASCII representation without using float64 arithmetic.
func formatPrice(price int64) string {
	return fmt.Sprintf("%d.%04d", price/10000, price%10000)
}
