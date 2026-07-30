package rabbitmq_producer

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
)

func TestPublishAndAwaitConfirmationWithRabbitMQ(t *testing.T) {
	url := os.Getenv("TEST_RABBITMQ_URL")
	if url == "" {
		t.Skip("TEST_RABBITMQ_URL is not set")
	}
	connection, err := amqp.Dial(url)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	channel, err := connection.Channel()
	if err != nil {
		t.Fatal(err)
	}
	defer channel.Close()
	if err := channel.Confirm(false); err != nil {
		t.Fatal(err)
	}
	queueName := "omniwa-confirm-test-" + uuid.NewString()
	if _, err := channel.QueueDeclare(queueName, false, true, true, false, nil); err != nil {
		t.Fatal(err)
	}
	confirmations := channel.NotifyPublish(make(chan amqp.Confirmation, 1))
	if err := publishAndAwaitConfirmation(context.Background(), channel, confirmations, queueName, []byte(`{"ok":true}`), uuid.NewString(), 5*time.Second); err != nil {
		t.Fatal(err)
	}
	queue, err := channel.QueueInspect(queueName)
	if err != nil || queue.Messages != 1 {
		t.Fatalf("confirmed queue state = %#v, %v", queue, err)
	}
}

func TestDeliverConfirmedWithRabbitMQCarriesStableMessageID(t *testing.T) {
	url := os.Getenv("TEST_RABBITMQ_URL")
	if url == "" {
		t.Skip("TEST_RABBITMQ_URL is not set")
	}
	connection, err := amqp.Dial(url)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	producer := NewRabbitMQProducer(connection, false, nil, nil, url, nil).(*rabbitMQProducer)
	queueName := "omniwa-outbox-confirm-test-" + uuid.NewString()
	deliveryID := uuid.NewString()
	if err := producer.DeliverConfirmed(context.Background(), queueName, []byte(`{"ok":true}`), "enabled", deliveryID); err != nil {
		t.Fatal(err)
	}
	channel, err := connection.Channel()
	if err != nil {
		t.Fatal(err)
	}
	defer channel.Close()
	t.Cleanup(func() { _, _ = channel.QueueDelete(queueName, false, false, false) })
	message, ok, err := channel.Get(queueName, false)
	if err != nil || !ok {
		t.Fatalf("confirmed message available=%t error=%v", ok, err)
	}
	if message.MessageId != deliveryID || message.DeliveryMode != amqp.Persistent {
		t.Fatalf("message metadata id=%q delivery_mode=%d", message.MessageId, message.DeliveryMode)
	}
	if err := message.Ack(false); err != nil {
		t.Fatal(err)
	}
}
