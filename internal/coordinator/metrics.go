package coordinator

import (
	"github.com/51ddhesh/exchange-bench/internal/coordinator/proto"
	"github.com/51ddhesh/exchange-bench/internal/runner"
)

func merge(workers []*proto.WorkerMetrics) runner.RunMetrics {
	var m runner.RunMetrics
	var totalSent int64

	for _, w := range workers {
		m.TicksSent += w.TicksSent
		m.TicksAcked += w.TicksAcked
		m.PeakTPS += w.PeakTps

		if w.TimedOut {
			m.TimedOut = true
		}

		totalSent += w.TicksSent
	}

	if totalSent == 0 {
		return m
	}

	var p50, p90, p99 float64

	for _, w := range workers {
		weight := float64(w.TicksSent) / float64(totalSent)
		p50 += weight * float64(w.P50Us)
		p90 += weight * float64(w.P90Us)
		p99 += weight * float64(w.P99Us)
	}

	m.P50LatencyUs = int64(p50)
	m.P90LatencyUs = int64(p90)
	m.P99LatencyUs = int64(p99)

	return m
}
