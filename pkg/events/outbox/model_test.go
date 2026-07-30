package outbox

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	"github.com/google/uuid"
)

func TestDeliveryDoesNotSerializeInternalState(t *testing.T) {
	delivery := Delivery{
		ID: uuid.NewString(), DurableEventID: uuid.NewString(), InstanceID: uuid.NewString(),
		Transport: TransportWebhook, Destination: DestinationInstance, RoutingKey: "instance.message",
		Payload: json.RawMessage(`{"message":"private"}`), Status: StatusPending,
	}
	encoded, err := json.Marshal(delivery)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != "{}" {
		t.Fatalf("internal delivery serialized as %s", encoded)
	}
}

func TestPrepareDeliverySanitizesCredentialsAndInitializesPolicy(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	event := &projection_model.DurableEvent{ID: uuid.NewString(), InstanceID: uuid.NewString()}
	delivery := Delivery{
		ID: uuid.NewString(), Transport: TransportRabbitMQ, Destination: DestinationGlobal, RoutingKey: "message",
		Payload: json.RawMessage(`{"instanceId":"safe","nested":{"instanceToken":"secret"}}`),
	}
	if err := prepareDelivery(&delivery, event, now); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(delivery.Payload), "instanceToken") || strings.Contains(string(delivery.Payload), "secret") {
		t.Fatalf("credential survived persistence boundary: %s", delivery.Payload)
	}
	if delivery.DurableEventID != event.ID || delivery.InstanceID != event.InstanceID || delivery.Status != StatusPending ||
		delivery.MaxAttempts != DefaultMaxAttempts || delivery.RetryPolicyVersion != RetryPolicyVersion || !delivery.AvailableAt.Equal(now) {
		t.Fatalf("delivery policy was not initialized: %#v", delivery)
	}
}

func TestPrepareDeliveryRejectsUnsupportedAndUnboundedInputs(t *testing.T) {
	event := &projection_model.DurableEvent{ID: uuid.NewString(), InstanceID: uuid.NewString()}
	base := Delivery{ID: uuid.NewString(), Transport: TransportWebhook, Destination: DestinationInstance, RoutingKey: "event", Payload: json.RawMessage(`{"ok":true}`)}
	tests := []struct {
		name   string
		mutate func(*Delivery)
	}{
		{name: "transport", mutate: func(value *Delivery) { value.Transport = "socket" }},
		{name: "destination", mutate: func(value *Delivery) { value.Destination = "arbitrary" }},
		{name: "routing key", mutate: func(value *Delivery) { value.RoutingKey = "" }},
		{name: "scalar payload", mutate: func(value *Delivery) { value.Payload = json.RawMessage(`"value"`) }},
		{name: "attempt budget", mutate: func(value *Delivery) { value.MaxAttempts = -1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := base
			test.mutate(&value)
			if err := prepareDelivery(&value, event, time.Now().UTC()); err == nil {
				t.Fatal("invalid delivery was accepted")
			}
		})
	}
}
