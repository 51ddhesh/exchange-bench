package protocol

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Reader wraps a bufio.Scanner over the contestant's stdout pipe and exposes
// ReadResponse, which parses exactly one line per call.
//
// All parse errors are non-fatal by design. ReadResponse returns
// (Response{}, err) on any malformed line. The runner is expected to treat
// such a result as an incorrect tick and continue — it must never crash
// because a contestant produced garbage.
//
// Only two errors signal a dead pipe and require the runner to abort:
//   - io.EOF  — contestant process closed stdout cleanly
//   - any non-nil scanner error — underlying I/O failure
type Reader struct {
	scanner *bufio.Scanner
}

// NewReader constructs a Reader. r is typically the contestant's stdout pipe.
// The underlying bufio.Scanner uses the default 64 KB line buffer, which is
// ample for any valid or adversarially long response line.
func NewReader(r io.Reader) *Reader {
	return &Reader{scanner: bufio.NewScanner(r)}
}

// ReadResponse reads one line from the contestant's stdout and parses it.
//
// Return semantics:
//
//	(Response, nil)      — well-formed line, caller should inspect Response.Type
//	(Response{}, err)    — parse error: line was malformed; non-fatal, tick is wrong
//	(Response{}, io.EOF) — pipe closed cleanly; runner should stop
//	(Response{}, err)    — scanner I/O error; runner should abort
func (rd *Reader) ReadResponse() (Response, error) {
	if !rd.scanner.Scan() {
		err := rd.scanner.Err()
		if err == nil {
			return Response{}, io.EOF
		}
		return Response{}, err
	}

	line := rd.scanner.Text()
	if line == "" {
		return Response{}, newParseError("empty line")
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
// Exactly 2 fields.
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
// Exactly 6 fields.
//
// <exec_price> is the contestant's fixed-4-decimal string. parsePrice inverts
// the formatPrice transformation without going through float64.
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
// Minimum 2 fields. Reason is optional and may contain spaces.
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

// parsePrice inverts formatPrice: "100.0000" → 1000000.
//
// It never uses float64. Algorithm:
//  1. Split on ".". If no dot, treat the whole string as the integer part
//     and multiply by 10000.
//  2. Parse integer and fractional parts as int64 separately.
//  3. Normalise fracStr to exactly 4 digits: truncate if longer, right-pad
//     with zeros if shorter. This makes the function robust against
//     contestants that omit trailing zeros ("100.5" → 1005000) or add
//     extra digits ("100.50000" → 1005000).
//  4. Return intPart*10000 + fracPart.
//
// Any non-numeric character in either part causes a strconv error, which
// the caller surfaces as a non-fatal parse error.
func parsePrice(s string) (int64, error) {
	dotIdx := strings.Index(s, ".")
	if dotIdx == -1 {
		// No decimal point. Contestant wrote a bare integer.
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return 0, err
		}
		return n * 10000, nil
	}

	intStr := s[:dotIdx]
	fracStr := s[dotIdx+1:]

	// Normalise fracStr to exactly 4 digits.
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
