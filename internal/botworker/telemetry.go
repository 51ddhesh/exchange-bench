package botworker

import (
	"github.com/51ddhesh/exchange-bench/internal/protocol"
	"github.com/51ddhesh/exchange-bench/internal/workload"
)

type TelemetryEvent struct {
	OrderID      string
	IntendedAtNs int64
	ReceivedAtNs int64
	Acked        bool
	Violation    string
	// Validation fields: populated by bot.readLoop, consumed by the
	// per-bot correlator in firer.Run. Not transmitted over the wire.
	Tick      workload.Tick
	Responses []protocol.Response
}
