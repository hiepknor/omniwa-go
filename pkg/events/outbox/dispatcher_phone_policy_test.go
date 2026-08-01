package outbox

import (
	"context"
	"encoding/json"
	"testing"

	event_payload "github.com/evolution-foundation/evolution-go/pkg/events/payload"
)

type captureWebhook struct{ payload []byte }

func (c *captureWebhook) DeliverConfirmed(_ context.Context, _ string, payload []byte, _ string) error {
	c.payload = append([]byte(nil), payload...)
	return nil
}

type captureRabbit struct{}

func (*captureRabbit) DeliverConfirmed(context.Context, string, []byte, string, string) error {
	return nil
}

type dispatcherTargets struct{}

func (dispatcherTargets) WebhookTarget(context.Context, Destination, string) (string, error) {
	return "https://example.invalid", nil
}
func (dispatcherTargets) RabbitMQMode(context.Context, Destination, string) (string, error) {
	return "enabled", nil
}

func TestTransportDispatcherAppliesRestartKillSwitchAtDelivery(t *testing.T) {
	webhook := &captureWebhook{}
	policy := event_payload.NewPhonePayloadPolicy(false)
	dispatcher, err := NewTransportDispatcher(webhook, &captureRabbit{}, dispatcherTargets{}, policy)
	if err != nil {
		t.Fatal(err)
	}
	err = dispatcher.Deliver(context.Background(), Delivery{
		ID: "11111111-1111-1111-1111-111111111111", InstanceID: "22222222-2222-2222-2222-222222222222",
		Transport: TransportWebhook, Destination: DestinationInstance,
		Payload: []byte(`{"event":"Message","data":{"senderPhoneNumber":"15550001","Sender":"legacy"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(webhook.payload, &root); err != nil {
		t.Fatal(err)
	}
	data := root["data"].(map[string]any)
	if _, exists := data["senderPhoneNumber"]; exists || data["Sender"] != "legacy" {
		t.Fatalf("payload=%s", webhook.payload)
	}
}

func TestTransportDispatcherDeadLettersMalformedPolicyPayload(t *testing.T) {
	policy := event_payload.NewPhonePayloadPolicy(false)
	dispatcher, _ := NewTransportDispatcher(&captureWebhook{}, &captureRabbit{}, dispatcherTargets{}, policy)
	err := dispatcher.Deliver(context.Background(), Delivery{
		ID: "11111111-1111-1111-1111-111111111111", InstanceID: "22222222-2222-2222-2222-222222222222",
		Transport: TransportWebhook, Destination: DestinationInstance, Payload: []byte(`{"data":`),
	})
	deliveryErr, ok := err.(*DeliveryError)
	if !ok || deliveryErr.Code != "payload_policy_failed" || deliveryErr.Retryable {
		t.Fatalf("err=%#v", err)
	}
}
