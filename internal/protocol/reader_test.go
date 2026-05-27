package protocol

import (
	"io"
	"strings"
	"testing"

	"github.com/51ddhesh/exchange-bench/internal/workload"
)

// readerFrom builds a Reader over a literal string.
// Used in every subtest to avoid repetition.
func readerFrom(s string) *Reader {
	return NewReader(strings.NewReader(s))
}

// TestReadResponse covers all legal and illegal line shapes.
// Every subtest is self-contained: it constructs its own Reader so
// scanner state never leaks between cases.
func TestReadResponse(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantType ResponseType // checked only when wantErr == false
		wantResp Response     // full struct checked when wantErr == false
		wantErr  bool
		wantEOF  bool // true when we expect io.EOF specifically
	}{
		// --- ACK ---
		{
			name:  "valid ACK",
			input: "ACK o42\n",
			wantResp: Response{
				Type:    RespACK,
				OrderID: "o42",
			},
		},
		{
			name:    "ACK too few fields",
			input:   "ACK\n",
			wantErr: true,
		},
		{
			name:    "ACK too many fields",
			input:   "ACK o42 extra\n",
			wantErr: true,
		},

		// --- FILL ---
		{
			name:  "valid FILL",
			input: "FILL o42 o10 o42 100.0000 5\n",
			wantResp: Response{
				Type:         RespFILL,
				OrderID:      "o42",
				MakerOrderID: "o10",
				TakerOrderID: "o42",
				ExecPrice:    1000000,
				ExecQty:      5,
			},
		},
		{
			name:  "valid FILL non-round price",
			input: "FILL o7 o3 o7 100.5000 12\n",
			wantResp: Response{
				Type:         RespFILL,
				OrderID:      "o7",
				MakerOrderID: "o3",
				TakerOrderID: "o7",
				ExecPrice:    1005000,
				ExecQty:      12,
			},
		},
		{
			name:    "FILL too few fields",
			input:   "FILL o42 o10\n",
			wantErr: true,
		},
		{
			name:    "FILL too many fields",
			input:   "FILL o42 o10 o42 100.0000 5 extra\n",
			wantErr: true,
		},
		{
			name:    "FILL bad price non-numeric",
			input:   "FILL o42 o10 o42 notaprice 5\n",
			wantErr: true,
		},
		{
			name:    "FILL bad qty non-numeric",
			input:   "FILL o42 o10 o42 100.0000 abc\n",
			wantErr: true,
		},

		// --- REJ ---
		{
			name:  "valid REJ no reason",
			input: "REJ o42\n",
			wantResp: Response{
				Type:    RespREJ,
				OrderID: "o42",
				Reason:  "",
			},
		},
		{
			name:  "valid REJ with single-word reason",
			input: "REJ o42 rejected\n",
			wantResp: Response{
				Type:    RespREJ,
				OrderID: "o42",
				Reason:  "rejected",
			},
		},
		{
			name:  "valid REJ with multi-word reason",
			input: "REJ o42 price out of range\n",
			wantResp: Response{
				Type:    RespREJ,
				OrderID: "o42",
				Reason:  "price out of range",
			},
		},
		{
			name:    "REJ too few fields",
			input:   "REJ\n",
			wantErr: true,
		},

		// --- malformed ---
		{
			name:    "empty line",
			input:   "\n",
			wantErr: true,
		},
		{
			name:    "unknown command",
			input:   "FOO o42\n",
			wantErr: true,
		},
		{
			name:    "unknown command looks like valid token",
			input:   "CANCEL o42\n",
			wantErr: true,
		},

		// --- pipe closed ---
		{
			name:    "empty reader returns EOF",
			input:   "",
			wantEOF: true,
		},

		// --- partial line (no trailing newline) ---
		// bufio.Scanner flushes incomplete final line on EOF,
		// so this parses identically to a line with a newline.
		{
			name:  "ACK partial line no newline",
			input: "ACK o99",
			wantResp: Response{
				Type:    RespACK,
				OrderID: "o99",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rd := readerFrom(tc.input)
			got, err := rd.ReadResponse()

			if tc.wantEOF {
				if err != io.EOF {
					t.Fatalf("want io.EOF, got err=%v resp=%+v", err, got)
				}
				return
			}

			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got nil (resp=%+v)", got)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.wantResp {
				t.Errorf("response mismatch\n  want: %+v\n   got: %+v", tc.wantResp, got)
			}
		})
	}
}

// TestPriceRoundTrip verifies that formatPrice ∘ parsePrice = identity for
// a range of representative ×10000 fixed-point values.
// This is the critical invariant at the trust boundary: whatever the writer
// puts on the wire, the reader must recover exactly.
func TestPriceRoundTrip(t *testing.T) {
	prices := []int64{
		0,                // market order
		100,              // 0.0100
		1000000,          // 100.0000
		1005000,          // 100.5000
		1000100,          // 100.0100
		9999999,          // 999.9999
		10000000,         // 1000.0000
	}

	for _, p := range prices {
		s := formatPrice(p)
		got, err := parsePrice(s)
		if err != nil {
			t.Errorf("parsePrice(%q) error: %v (original price %d)", s, err, p)
			continue
		}
		if got != p {
			t.Errorf("round-trip failed: price %d → %q → %d", p, s, got)
		}
	}
}

// TestParsePriceRobust verifies parsePrice handles contestant deviations
// that should still produce a correct result rather than an error.
func TestParsePriceRobust(t *testing.T) {
	tests := []struct {
		input string
		want  int64
	}{
		{"100.5", 1005000},     // contestant omitted trailing zeros
		{"100.50", 1005000},    // two decimal digits
		{"100.500", 1005000},   // three decimal digits
		{"100.50001", 1005000}, // five digits — truncated to four
		{"100", 1000000},       // no decimal point at all
	}
	for _, tc := range tests {
		got, err := parsePrice(tc.input)
		if err != nil {
			t.Errorf("parsePrice(%q) unexpected error: %v", tc.input, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parsePrice(%q): want %d, got %d", tc.input, tc.want, got)
		}
	}
}

// TestWriteTickRoundTrip feeds WriteTick into a Reader and checks that
// ReadResponse recovers the correct protocol fields.
// Covers ADD (limit), ADD (market), and CAN.
func TestWriteTickRoundTrip(t *testing.T) {
	t.Run("ADD limit buy", func(t *testing.T) {
		tick := workload.Tick{
			Type:    workload.TickAdd,
			OrderID: "o1",
			Side:    'B',
			OrdType: 'L',
			Price:   1005000,
			Qty:     10,
		}

		var buf strings.Builder
		if err := WriteTick(&buf, tick); err != nil {
			t.Fatalf("WriteTick error: %v", err)
		}

		// The wire line should be: "ADD o1 B L 100.5000 10\n"
		wantLine := "ADD o1 B L 100.5000 10\n"
		if buf.String() != wantLine {
			t.Errorf("wire line:\n  want: %q\n   got: %q", wantLine, buf.String())
		}
	})

	t.Run("ADD market sell", func(t *testing.T) {
		tick := workload.Tick{
			Type:    workload.TickAdd,
			OrderID: "o2",
			Side:    'S',
			OrdType: 'M',
			Price:   0,
			Qty:     5,
		}

		var buf strings.Builder
		if err := WriteTick(&buf, tick); err != nil {
			t.Fatalf("WriteTick error: %v", err)
		}

		wantLine := "ADD o2 S M 0.0000 5\n"
		if buf.String() != wantLine {
			t.Errorf("wire line:\n  want: %q\n   got: %q", wantLine, buf.String())
		}
	})

	t.Run("CAN", func(t *testing.T) {
		tick := workload.Tick{
			Type:    workload.TickCancel,
			OrderID: "o1",
		}

		var buf strings.Builder
		if err := WriteTick(&buf, tick); err != nil {
			t.Fatalf("WriteTick error: %v", err)
		}

		wantLine := "CAN o1\n"
		if buf.String() != wantLine {
			t.Errorf("wire line:\n  want: %q\n   got: %q", wantLine, buf.String())
		}
	})
}
