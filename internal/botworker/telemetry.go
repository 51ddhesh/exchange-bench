package botworker

type TelemetryEvent struct {
	OrderID      string
	IntendedAtNs int64
	ReceivedAtNs int64
	Acked        bool
	Violation    string
}
