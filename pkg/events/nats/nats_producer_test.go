package nats_producer

import (
	"testing"

	"github.com/nats-io/nats.go"
)

func TestNewNatsProducerWithBlankURLIsDisabled(t *testing.T) {
	t.Parallel()

	connectCalls := 0
	producer, ok := newNatsProducer(
		"  ",
		true,
		[]string{"messages.upsert"},
		nil,
		func(string, ...nats.Option) (*nats.Conn, error) {
			connectCalls++
			return nil, nil
		},
	).(*natsProducer)
	if !ok {
		t.Fatal("expected NATS producer implementation")
	}
	if connectCalls != 0 {
		t.Fatalf("expected no NATS connection attempt, got %d", connectCalls)
	}
	if producer.conn != nil {
		t.Fatal("expected no NATS connection for a blank URL")
	}
	if producer.natsGlobalEnabled {
		t.Fatal("expected global NATS publishing to be disabled")
	}
	if producer.natsGlobalEvents != nil {
		t.Fatal("expected global NATS events to be cleared")
	}
}
