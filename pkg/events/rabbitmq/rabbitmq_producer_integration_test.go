package rabbitmq_producer

import (
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
	if err := publishAndAwaitConfirmation(channel, confirmations, queueName, []byte(`{"ok":true}`), 5*time.Second); err != nil {
		t.Fatal(err)
	}
	queue, err := channel.QueueInspect(queueName)
	if err != nil || queue.Messages != 1 {
		t.Fatalf("confirmed queue state = %#v, %v", queue, err)
	}
}
