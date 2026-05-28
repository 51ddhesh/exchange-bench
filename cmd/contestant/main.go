// contestant is a reference implementation of the exchange-bench wire protocol.
// It is backed by the reference matching engine, so any correct platform
// evaluation must score it at 100% correctness.
//
// Wire protocol (stdin):
//
//	ADD <order_id> <side> <type> <price> <qty>
//	CAN <order_id>
//
// Wire protocol (stdout):
//
//	FILL <order_id> <maker_id> <taker_id> <exec_price> <exec_qty>
//	ACK  <order_id>
//	REJ  <order_id> [reason]
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/51ddhesh/exchange-bench/internal/orderbook"
)

func main() {
	engine := orderbook.NewEngine(1_000_000)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go engine.Run(ctx)

	seq := orderbook.NewSequencer(engine)

	in := bufio.NewScanner(os.Stdin)
	out := bufio.NewWriter(os.Stdout)

	for in.Scan() {
		line := in.Text()
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}

		switch fields[0] {
		case "ADD":
			handleAdd(fields, seq, out)
		case "CAN":
			handleCancel(fields, seq, out)
		default:
			// Ignore unrecognised commands — don't write anything.
		}

		out.Flush()
	}

	if err := in.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "contestant: stdin error: %v\n", err)
		os.Exit(1)
	}
}

func handleAdd(fields []string, seq *orderbook.Sequencer, out *bufio.Writer) {
	// ADD <order_id> <side> <type> <price> <qty>
	if len(fields) != 6 {
		if len(fields) >= 2 {
			fmt.Fprintf(out, "REJ %s bad_format\n", fields[1])
		}
		return
	}

	orderID := fields[1]
	side := sideFromString(fields[2])
	ordType := ordTypeFromString(fields[3])
	price := parsePrice(fields[4])
	qty, err := strconv.ParseInt(fields[5], 10, 64)
	if err != nil {
		fmt.Fprintf(out, "REJ %s bad_qty\n", orderID)
		return
	}

	fills, _, _ := seq.Add(orderID, side, ordType, price, qty)

	for _, f := range fills {
		makerExt := seq.ExternalID(f.MakerOrderID)
		takerExt := seq.ExternalID(f.TakerOrderID)
		fmt.Fprintf(out, "FILL %s %s %s %s %d\n",
			orderID, makerExt, takerExt,
			formatPrice(f.ExecPrice), f.ExecQty)
	}
	fmt.Fprintf(out, "ACK %s\n", orderID)
}

func handleCancel(fields []string, seq *orderbook.Sequencer, out *bufio.Writer) {
	// CAN <order_id>
	if len(fields) != 2 {
		return
	}
	orderID := fields[1]
	result := seq.Cancel(orderID)
	if result == orderbook.CancelOK {
		fmt.Fprintf(out, "ACK %s\n", orderID)
	} else {
		fmt.Fprintf(out, "REJ %s not_found\n", orderID)
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func sideFromString(s string) orderbook.Side {
	if s == "B" {
		return orderbook.Buy
	}
	return orderbook.Sell
}

func ordTypeFromString(s string) orderbook.OrderType {
	if s == "L" {
		return orderbook.Limit
	}
	return orderbook.Market
}

// parsePrice inverts formatPrice: "100.5000" → 1_005_000.
// Does not use float64.
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
