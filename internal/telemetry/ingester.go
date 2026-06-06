package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	proto "github.com/51ddhesh/exchange-bench/internal/coordinator/proto"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kgo"
)

const (
	flushRows     = 1000
	flushInterval = 500 * time.Millisecond
)

// Ingester consumes telemetry events from Redpanda and writes them to
// TimescaleDB. On receiving the run-complete sentinel, it computes
// approx_percentile statistics and upserts run_scores.
type Ingester struct {
	client *kgo.Client
	db     *pgxpool.Pool
	topic  string
}

// NewIngester connects to Redpanda and TimescaleDB.
func NewIngester(brokers []string, topic, dsn string) (*Ingester, error) {
	client, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ConsumeTopics(topic),
	)
	if err != nil {
		return nil, fmt.Errorf("ingester: kafka client: %w", err)
	}

	db, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("ingester: db pool: %w", err)
	}

	return &Ingester{client: client, db: db, topic: topic}, nil
}

// Run polls Redpanda and processes events until ctx is cancelled.
func (ing *Ingester) Run(ctx context.Context) error {
	var pending [][]any
	flushTimer := time.NewTimer(flushInterval)
	defer flushTimer.Stop()

	flush := func() error {
		if len(pending) == 0 {
			return nil
		}
		rows := pending
		pending = pending[:0]
		return ing.insertBatch(ctx, rows)
	}

	for {
		select {
		case <-ctx.Done():
			_ = flush()
			return ctx.Err()
		case <-flushTimer.C:
			if err := flush(); err != nil {
				fmt.Printf("ingester: flush error: %v\n", err)
			}
			flushTimer.Reset(flushInterval)
		default:
		}

		fetches := ing.client.PollFetches(ctx)
		if fetches.IsClientClosed() {
			return flush()
		}
		fetches.EachError(func(_ string, _ int32, err error) {
			fmt.Printf("ingester: fetch error: %v\n", err)
		})

		fetches.EachRecord(func(rec *kgo.Record) {
			var evt proto.TelemetryEvent
			if err := json.Unmarshal(rec.Value, &evt); err != nil {
				return
			}

			if evt.OrderId == "__RUN_COMPLETE__" {
				// Flush pending rows first so percentile query sees them.
				if err := flush(); err != nil {
					fmt.Printf("ingester: pre-sentinel flush: %v\n", err)
				}
				if err := ing.finalizeRun(ctx, &evt); err != nil {
					fmt.Printf("ingester: finalize run %s: %v\n", evt.RunId, err)
				}
				return
			}

			latencyUs := (evt.ReceivedAtNs - evt.IntendedAtNs) / 1000
			if latencyUs < 1 {
				latencyUs = 1
			}
			eventTime := time.Unix(0, evt.IntendedAtNs)

			pending = append(pending, []any{
				eventTime,
				evt.RunId,
				evt.SubmissionId,
				evt.OrderId,
				evt.IntendedAtNs,
				evt.ReceivedAtNs,
				latencyUs,
				evt.Acked,
				evt.Violation,
			})

			if len(pending) >= flushRows {
				rows := pending
				pending = pending[:0]
				if err := ing.insertBatch(ctx, rows); err != nil {
					fmt.Printf("ingester: batch insert: %v\n", err)
				}
			}
		})
	}
}

func (ing *Ingester) insertBatch(ctx context.Context, rows [][]any) error {
	cols := []string{
		"time", "run_id", "submission_id", "order_id",
		"intended_at_ns", "received_at_ns", "latency_us",
		"acked", "violation",
	}
	_, err := ing.db.CopyFrom(ctx,
		pgx.Identifier{"telemetry_events"},
		cols,
		pgx.CopyFromRows(rows),
	)
	return err
}

// finalizeRun runs the percentile query for run_id and upserts run_scores.
// The submission_id in the sentinel is "team-1_1" format; team_id and attempt
// are parsed from it. Fields not available in telemetry (peak_tps,
// capacity_tps, correctness, composite_score, critical_flag, language) are
// left at their default values — the API server worker goroutine is the
// authoritative writer for those fields. The ingester only fills in the
// percentile columns.
//
// If no acked rows exist for this run_id (e.g. the run failed early and the
// sentinel was still published), the upsert writes zeros for all percentiles.
func (ing *Ingester) finalizeRun(ctx context.Context, sentinel *proto.TelemetryEvent) error {
	type percentiles struct {
		p50, p90, p99 float64
	}

	var p percentiles
	err := ing.db.QueryRow(ctx, `
		SELECT
			approx_percentile(0.50, percentile_agg(latency_us)),
			approx_percentile(0.90, percentile_agg(latency_us)),
			approx_percentile(0.99, percentile_agg(latency_us))
		FROM telemetry_events
		WHERE run_id = $1 AND acked = true
	`, sentinel.RunId).Scan(&p.p50, &p.p90, &p.p99)
	if err != nil {
		// No rows or extension not available — write zeros.
		fmt.Printf("ingester: percentile query for run %s: %v\n", sentinel.RunId, err)
		p = percentiles{}
	}

	teamID, attempt := parseSubmissionID(sentinel.SubmissionId)

	_, err = ing.db.Exec(ctx, `
		INSERT INTO run_scores
			(submission_id, team_id, attempt, run_id,
			 p50_us, p90_us, p99_us, completed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NOW())
		ON CONFLICT (submission_id) DO UPDATE SET
			run_id       = EXCLUDED.run_id,
			p50_us       = EXCLUDED.p50_us,
			p90_us       = EXCLUDED.p90_us,
			p99_us       = EXCLUDED.p99_us,
			completed_at = EXCLUDED.completed_at
	`,
		sentinel.SubmissionId,
		teamID,
		attempt,
		sentinel.RunId,
		int64(p.p50),
		int64(p.p90),
		int64(p.p99),
	)
	return err
}

// parseSubmissionID splits "team-1_2" into ("team-1", 2).
// Returns ("unknown", 0) on malformed input.
func parseSubmissionID(id string) (teamID string, attempt int) {
	for i := len(id) - 1; i >= 0; i-- {
		if id[i] == '_' {
			teamID = id[:i]
			fmt.Sscanf(id[i+1:], "%d", &attempt)
			return
		}
	}
	return "unknown", 0
}

// Close releases all resources.
func (ing *Ingester) Close() {
	ing.client.Close()
	ing.db.Close()
}
