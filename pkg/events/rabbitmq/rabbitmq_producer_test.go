package rabbitmq_producer

import (
	"errors"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

type fakeMessagePublisher struct {
	err       error
	queue     string
	published amqp.Publishing
}

func (f *fakeMessagePublisher) Publish(_ string, queue string, _ bool, _ bool, message amqp.Publishing) error {
	f.queue = queue
	f.published = message
	return f.err
}

func TestPublishAndAwaitConfirmationRequiresBrokerAck(t *testing.T) {
	t.Parallel()
	publisher := &fakeMessagePublisher{}
	confirmed := make(chan amqp.Confirmation, 1)
	confirmed <- amqp.Confirmation{DeliveryTag: 1, Ack: true}
	if err := publishAndAwaitConfirmation(publisher, confirmed, "message", []byte(`{"ok":true}`), time.Second); err != nil {
		t.Fatal(err)
	}
	if publisher.queue != "message" || publisher.published.DeliveryMode != amqp.Persistent {
		t.Fatalf("persistent publish was not issued: %#v", publisher)
	}

	rejected := make(chan amqp.Confirmation, 1)
	rejected <- amqp.Confirmation{DeliveryTag: 1, Ack: false}
	closed := make(chan amqp.Confirmation)
	close(closed)
	for _, test := range []struct {
		name          string
		publisher     messagePublisher
		confirmations <-chan amqp.Confirmation
		timeout       time.Duration
	}{
		{name: "publish error", publisher: &fakeMessagePublisher{err: errors.New("publish failed")}, confirmations: confirmed, timeout: time.Second},
		{name: "broker nack", publisher: &fakeMessagePublisher{}, confirmations: rejected, timeout: time.Second},
		{name: "closed stream", publisher: &fakeMessagePublisher{}, confirmations: closed, timeout: time.Second},
		{name: "confirmation timeout", publisher: &fakeMessagePublisher{}, confirmations: make(chan amqp.Confirmation), timeout: time.Millisecond},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := publishAndAwaitConfirmation(test.publisher, test.confirmations, "message", []byte(`{}`), test.timeout); err == nil {
				t.Fatal("unconfirmed RabbitMQ publish was accepted")
			}
		})
	}
}
