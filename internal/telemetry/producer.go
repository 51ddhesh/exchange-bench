package telemetry

import (
	"context"
	"encoding/json"
	"fmt"

	proto "github.com/51ddhesh/exchange-bench/internal/coordinator/proto"
	"github.com/twmb/franz-go/pkg/kgo"
)

// Producer publishes TelemetryEvents to a Redpanda topic.
// Serialisation is plain JSON; partition key is submission_id.
//
// Telemetry is best-effort: the coordinator's fan-in drops events when the
// producerCh is full. Dropped events affect leaderboard display lag only —
// final scores come from HDR histograms collected via CollectMetrics RPC.
type Producer struct {
	client *kgo.Client
	topic  string
}

// New creates a Producer connected to the given brokers.
func New(brokers []string, topic string) (*Producer, error) {
	client, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
	)
	if err != nil {
		return nil, fmt.Errorf("telemetry/producer: %w", err)
	}
	return &Producer{client: client, topic: topic}, nil
}

// Run drains ch until it is closed or ctx is cancelled, publishing each event
// to Redpanda asynchronously. Flushes any buffered records before returning.
func (p *Producer) Run(ctx context.Context, ch <-chan *proto.TelemetryEvent) error {
	for {
		select {
		case evt, ok := <-ch:
			if !ok {
				return p.client.Flush(ctx)
			}
			b, err := json.Marshal(evt)
			if err != nil {
				continue
			}
			p.client.Produce(ctx, &kgo.Record{
				Topic: p.topic,
				Key:   []byte(evt.SubmissionId),
				Value: b,
			}, nil)
		case <-ctx.Done():
			p.client.Flush(ctx) //nolint:errcheck
			return ctx.Err()
		}
	}
}

// Close closes the underlying Kafka client.
func (p *Producer) Close() {
	p.client.Close()
}
