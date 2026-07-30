package nats_producer

import (
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

type fakePublishConnection struct {
	publishedSubject string
	flushTimeout     time.Duration
	publishErr       error
	flushErr         error
	lastErr          error
}

func (f *fakePublishConnection) Publish(subject string, _ []byte) error {
	f.publishedSubject = subject
	return f.publishErr
}

func (f *fakePublishConnection) FlushTimeout(timeout time.Duration) error {
	f.flushTimeout = timeout
	return f.flushErr
}

func (f *fakePublishConnection) LastError() error { return f.lastErr }

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

func TestPublishAndFlushRequiresServerAdmission(t *testing.T) {
	t.Parallel()
	connection := &fakePublishConnection{}
	if err := publishAndFlush(connection, "messages.upsert", []byte(`{"ok":true}`), time.Second); err != nil {
		t.Fatal(err)
	}
	if connection.publishedSubject != "messages.upsert" || connection.flushTimeout != time.Second {
		t.Fatalf("publish admission was not flushed: %#v", connection)
	}

	for _, test := range []struct {
		name string
		conn publishConnection
	}{
		{name: "missing connection", conn: nil},
		{name: "publish failure", conn: &fakePublishConnection{publishErr: errors.New("publish failed")}},
		{name: "flush failure", conn: &fakePublishConnection{flushErr: errors.New("flush failed")}},
		{name: "asynchronous failure", conn: &fakePublishConnection{lastErr: errors.New("server rejected publish")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := publishAndFlush(test.conn, "messages.upsert", []byte(`{}`), time.Second); err == nil {
				t.Fatal("unconfirmed NATS publish was accepted")
			}
		})
	}
}
