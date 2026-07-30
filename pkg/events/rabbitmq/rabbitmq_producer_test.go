package rabbitmq_producer

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
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
	deliveryID := uuid.NewString()
	if err := publishAndAwaitConfirmation(context.Background(), publisher, confirmed, "message", []byte(`{"ok":true}`), deliveryID, time.Second); err != nil {
		t.Fatal(err)
	}
	if publisher.queue != "message" || publisher.published.DeliveryMode != amqp.Persistent || publisher.published.MessageId != deliveryID {
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
			if err := publishAndAwaitConfirmation(context.Background(), test.publisher, test.confirmations, "message", []byte(`{}`), uuid.NewString(), test.timeout); err == nil {
				t.Fatal("unconfirmed RabbitMQ publish was accepted")
			}
		})
	}
}

func TestDeliverConfirmedHonorsContextWhileConnectionGateIsBusy(t *testing.T) {
	t.Parallel()
	producer := &rabbitMQProducer{connGate: make(chan struct{}, 1)}
	producer.connGate <- struct{}{}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := producer.DeliverConfirmed(ctx, "message", []byte(`{"ok":true}`), "enabled", uuid.NewString())
	if time.Since(started) > time.Second {
		t.Fatal("confirmed delivery ignored its context while waiting for the connection gate")
	}
	var classified *ConfirmedDeliveryError
	if !errors.As(err, &classified) || classified.Code != "connection_unavailable" || !classified.Retryable {
		t.Fatalf("busy connection classification = %#v, %v", classified, err)
	}
}
