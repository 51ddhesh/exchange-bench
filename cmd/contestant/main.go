// contestant is a reference implementation of the exchange-bench wire protocol
// over WebSocket. It accepts concurrent connections on :8080/orders, each
// backed by its own independent Engine + Sequencer instance.
//
// Inbound frames (platform → contestant):
//
//	ADD <order_id> <side> <type> <price> <qty>
//	CAN <order_id>
//
// Outbound frames (contestant → platform), one message per frame:
//
//	FILL <order_id> <maker_id> <taker_id> <exec_price> <exec_qty>
//	ACK  <order_id>
//	REJ  <order_id> [reason]
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/51ddhesh/exchange-bench/internal/orderbook"
	"github.com/coder/websocket"
)

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/orders", handleOrders)

	srv := &http.Server{Addr: ":8080", Handler: mux}

	go func() {
		quit := make(chan os.Signal, 1)
		signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)
		<-quit
		srv.Shutdown(context.Background()) //nolint:errcheck
	}()

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "contestant: %v\n", err)
		os.Exit(1)
	}
}

// handleOrders upgrades the HTTP connection to WebSocket, spins up an
// independent reference engine for this connection, then dispatches frames
// until the client closes or an error occurs.
func handleOrders(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // bots connect programmatically, no browser Origin
	})
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	engine := orderbook.NewEngine(1_000_000)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go engine.Run(ctx)

	seq := orderbook.NewSequencer(engine)

	for {
		_, msg, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if err := dispatch(ctx, conn, seq, strings.TrimSpace(string(msg))); err != nil {
			return
		}
	}
}

func dispatch(ctx context.Context, conn *websocket.Conn, seq *orderbook.Sequencer, line string) error {
	if line == "" {
		return nil
	}
	fields := strings.Fields(line)
	switch fields[0] {
	case "ADD":
		return handleAdd(ctx, conn, seq, fields)
	case "CAN":
		return handleCancel(ctx, conn, seq, fields)
	}
	return nil
}

// handleAdd processes an ADD frame. Sends zero or more FILL frames followed
// by exactly one ACK frame. Sends REJ on malformed input.
func handleAdd(ctx context.Context, conn *websocket.Conn, seq *orderbook.Sequencer, fields []string) error {
	// ADD <order_id> <side> <type> <price> <qty>
	if len(fields) != 6 {
		if len(fields) >= 2 {
			return conn.Write(ctx, websocket.MessageText, []byte("REJ "+fields[1]+" bad_format"))
		}
		return nil
	}

	orderID := fields[1]
	side := sideFromByte(fields[2][0])
	ordType := ordTypeFromByte(fields[3][0])
	price := parsePrice(fields[4])
	qty, err := strconv.ParseInt(fields[5], 10, 64)
	if err != nil {
		return conn.Write(ctx, websocket.MessageText, []byte("REJ "+orderID+" bad_qty"))
	}

	fills, _, _ := seq.Add(orderID, side, ordType, price, qty)

	for _, f := range fills {
		makerExt := seq.ExternalID(f.MakerOrderID)
		takerExt := seq.ExternalID(f.TakerOrderID)
		msg := fmt.Sprintf("FILL %s %s %s %s %d",
			orderID, makerExt, takerExt,
			formatPrice(f.ExecPrice), f.ExecQty)
		if err := conn.Write(ctx, websocket.MessageText, []byte(msg)); err != nil {
			return err
		}
	}
	return conn.Write(ctx, websocket.MessageText, []byte("ACK "+orderID))
}

// handleCancel processes a CAN frame. Sends ACK on success, REJ if not found.
func handleCancel(ctx context.Context, conn *websocket.Conn, seq *orderbook.Sequencer, fields []string) error {
	// CAN <order_id>
	if len(fields) != 2 {
		return nil
	}
	orderID := fields[1]
	if seq.Cancel(orderID) == orderbook.CancelOK {
		return conn.Write(ctx, websocket.MessageText, []byte("ACK "+orderID))
	}
	return conn.Write(ctx, websocket.MessageText, []byte("REJ "+orderID+" not_found"))
}

// ── helpers ──────────────────────────────────────────────────────────────────

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

// parsePrice inverts formatPrice: "100.5000" → 1_005_000. Never uses float64.
func parsePrice(s string) int64 {
	dotIdx := strings.Index(s, ".")
	if dotIdx == -1 {
		n, _ := strconv.ParseInt(s, 10, 64)
		return n * 10_000
	}
	intStr := s[:dotIdx]
	fracStr := s[dotIdx+1:]
	if len(fracStr) > 4 {
		fracStr = fracStr[:4]
	}
	for len(fracStr) < 4 {
		fracStr += "0"
	}
	intPart, _ := strconv.ParseInt(intStr, 10, 64)
	fracPart, _ := strconv.ParseInt(fracStr, 10, 64)
	return intPart*10_000 + fracPart
}

// formatPrice mirrors the platform's wire format: 1_005_000 → "100.5000".
func formatPrice(price int64) string {
	return fmt.Sprintf("%d.%04d", price/10_000, price%10_000)
}
