package protocol

import (
	"fmt"

	"github.com/51ddhesh/exchange-bench/internal/workload"
)

// ParseError is returned by ReadResponse when a line was successfully read
// from the pipe but could not be parsed. The runner treats this as non-fatal
// and continues the dispatch loop — the validator will detect the missing
// ACK/REJ and mark the tick as incorrect.
type ParseError struct {
	Err error
}

func (e *ParseError) Error() string { return e.Err.Error() }
func (e *ParseError) Unwrap() error { return e.Err }

// newParseError wraps an error message as a ParseError.
func newParseError(format string, args ...any) error {
	return &ParseError{Err: fmt.Errorf(format, args...)}
}

// Request is the outbound unit sent to the contestant — one tick per send.
// Wrapping the Tick makes the runner→validator handoff type-explicit and
// leaves room for per-request metadata (e.g. send timestamp) without
// touching workload.Tick.
type Request struct {
	Tick workload.Tick
}

// ResponseType distinguishes the three legal response classes.
// Zero value is deliberately invalid so an uninitialized Response is
// detectable by the validator without an extra boolean field.
type ResponseType int8

const (
	RespACK  ResponseType = 1
	RespFILL ResponseType = 2
	RespREJ  ResponseType = 3
)

// Response is one parsed line from the contestant's stdout.
//
// Field semantics by ResponseType:
//
//	ACK  = Type, OrderID populated. All others zero.
//	FILL = Type, OrderID, MakerOrderID, TakerOrderID, ExecPrice, ExecQty populated.
//	        ExecPrice is ×10000 fixed-point (same scale as the orderbook).
//	        Reason is "".
//	REJ  = Type, OrderID, Reason populated. Numeric fields zero.
type Response struct {
	Type         ResponseType
	OrderID      string // contestant's string ID, echoed back verbatim
	MakerOrderID string // FILL only
	TakerOrderID string // FILL only
	ExecPrice    int64  // FILL only; ×10000 fixed-point
	ExecQty      int64  // FILL only
	Reason       string // REJ only
}
