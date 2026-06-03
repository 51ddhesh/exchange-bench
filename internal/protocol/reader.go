package protocol

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/coder/websocket"
)

// Reader wraps a websocket.Conn and exposes ReadResponse, which parses
// exactly one WebSocket text frame per call.
//
// All parse errors are non-fatal by design. ReadResponse returns
// (Response{}, err) on any malformed frame. The runner treats such a result
// as an incorrect tick and continues — it must never crash because a
// contestant produced garbage.
//
// Two errors signal a dead connection and require the runner to abort:
//   - io.EOF  — contestant closed the WebSocket connection cleanly
//   - any non-ParseError — underlying connection failure
type Reader struct {
	conn *websocket.Conn
	ctx  context.Context
}

// NewReader constructs a Reader over a WebSocket connection.
// ctx is used for all subsequent conn.Read calls — pass the run context.
func NewReader(conn *websocket.Conn, ctx context.Context) *Reader {
	return &Reader{conn: conn, ctx: ctx}
}

// ReadResponse reads one WebSocket text frame from the contestant and parses it.
//
// Return semantics:
//
//	(Response, nil)      — well-formed frame, caller should inspect Response.Type
//	(Response{}, err)    — parse error: frame was malformed; non-fatal, tick is wrong
//	(Response{}, io.EOF) — connection closed cleanly; runner should stop
//	(Response{}, err)    — connection error; runner should abort
func (rd *Reader) ReadResponse() (Response, error) {
	_, msg, err := rd.conn.Read(rd.ctx)
	if err != nil {
		status := websocket.CloseStatus(err)
		if status == websocket.StatusNormalClosure || status == websocket.StatusGoingAway {
			return Response{}, io.EOF
		}
		return Response{}, err
	}

	line := strings.TrimSpace(string(msg))
	if line == "" {
		return Response{}, newParseError("empty frame")
	}

	fields := strings.Split(line, " ")

	switch fields[0] {
	case "ACK":
		return parseACK(fields)
	case "FILL":
		return parseFILL(fields)
	case "REJ":
		return parseREJ(fields)
	default:
		return Response{}, newParseError("unknown command %q", fields[0])
	}
}

// parseACK expects: ACK <order_id>
func parseACK(fields []string) (Response, error) {
	if len(fields) != 2 {
		return Response{}, newParseError("ACK wants 2 fields, got %d", len(fields))
	}
	return Response{
		Type:    RespACK,
		OrderID: fields[1],
	}, nil
}

// parseFILL expects: FILL <order_id> <maker_order_id> <taker_order_id> <exec_price> <exec_qty>
func parseFILL(fields []string) (Response, error) {
	if len(fields) != 6 {
		return Response{}, newParseError("FILL wants 6 fields, got %d", len(fields))
	}

	price, err := parsePrice(fields[4])
	if err != nil {
		return Response{}, newParseError("FILL bad exec_price %q: %w", fields[4], err)
	}

	qty, err := strconv.ParseInt(fields[5], 10, 64)
	if err != nil {
		return Response{}, newParseError("FILL bad exec_qty %q: %w", fields[5], err)
	}

	return Response{
		Type:         RespFILL,
		OrderID:      fields[1],
		MakerOrderID: fields[2],
		TakerOrderID: fields[3],
		ExecPrice:    price,
		ExecQty:      qty,
	}, nil
}

// parseREJ expects: REJ <order_id> [reason words...]
func parseREJ(fields []string) (Response, error) {
	if len(fields) < 2 {
		return Response{}, newParseError("REJ wants at least 2 fields, got %d", len(fields))
	}
	reason := ""
	if len(fields) > 2 {
		reason = strings.Join(fields[2:], " ")
	}
	return Response{
		Type:    RespREJ,
		OrderID: fields[1],
		Reason:  reason,
	}, nil
}

// parsePrice inverts formatPrice: "100.0000" → 1000000. Never uses float64.
func parsePrice(s string) (int64, error) {
	dotIdx := strings.Index(s, ".")
	if dotIdx == -1 {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return 0, err
		}
		return n * 10000, nil
	}

	intStr := s[:dotIdx]
	fracStr := s[dotIdx+1:]

	if len(fracStr) > 4 {
		fracStr = fracStr[:4]
	}
	for len(fracStr) < 4 {
		fracStr += "0"
	}

	intPart, err := strconv.ParseInt(intStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("integer part %q: %w", intStr, err)
	}

	fracPart, err := strconv.ParseInt(fracStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("fractional part %q: %w", fracStr, err)
	}

	return intPart*10000 + fracPart, nil
}
