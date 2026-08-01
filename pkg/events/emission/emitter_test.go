package emission

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	event_outbox "github.com/evolution-foundation/evolution-go/pkg/events/outbox"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	"github.com/google/uuid"
)

type builderFunc func(string, string, any) (*projection_model.DurableEvent, error)

func (f builderFunc) Build(instanceID, eventType string, raw any) (*projection_model.DurableEvent, error) {
	return f(instanceID, eventType, raw)
}

type recordingStore struct {
	event      *projection_model.DurableEvent
	deliveries []event_outbox.Delivery
	err        error
}

func (s *recordingStore) Record(_ context.Context, event *projection_model.DurableEvent, deliveries []event_outbox.Delivery) error {
	s.event = event
	s.deliveries = append([]event_outbox.Delivery(nil), deliveries...)
	return s.err
}

func testBuilder(instanceID, eventType string, _ any) (*projection_model.DurableEvent, error) {
	now := time.Now().UTC()
	return &projection_model.DurableEvent{
		ID: uuid.NewString(), InstanceID: instanceID, Type: eventType,
		OccurredAt: now, IngestedAt: now, ExpiresAt: now.Add(time.Hour), Summary: json.RawMessage(`{}`),
	}, nil
}

func TestEmitterAlwaysPlansConfiguredInstanceRoutes(t *testing.T) {
	t.Parallel()
	store := &recordingStore{}
	emitter, err := NewEmitter(builderFunc(testBuilder), store, Settings{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	instance := &instance_model.Instance{Id: uuid.NewString(), Events: "ALL", Webhook: "https://example.test", RabbitmqEnable: "enabled"}
	if err := emitter.Record(context.Background(), Event{Instance: instance, Type: "Message", QueueName: instance.Id + ".message", Payload: []byte(`{"event":"Message"}`)}); err != nil {
		t.Fatal(err)
	}
	if store.event == nil || store.event.InstanceID != instance.Id || len(store.deliveries) != 2 ||
		store.deliveries[0].Transport != event_outbox.TransportWebhook || store.deliveries[1].Transport != event_outbox.TransportRabbitMQ {
		t.Fatalf("default emission event=%#v deliveries=%#v", store.event, store.deliveries)
	}
}

func TestEmitterPlansSelectedRoutesFromOneAuthoritativeDecision(t *testing.T) {
	t.Parallel()
	store := &recordingStore{}
	emitter, err := NewEmitter(builderFunc(testBuilder), store, Settings{
		GlobalWebhookEnabled: true, GlobalRabbitEnabled: true,
		AMQPGlobalEvents: []string{"MESSAGE"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	instance := &instance_model.Instance{
		Id: uuid.NewString(), Events: "MESSAGE", Webhook: "https://instance.example.test/events", RabbitmqEnable: "true",
	}
	queue := instance.Id + ".message"
	payload := []byte(`{"event":"Message","data":{"Info":{"Chat":"15551234567@s.whatsapp.net"}}}`)
	if err := emitter.Record(context.Background(), Event{Instance: instance, Type: "Message", QueueName: queue, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if len(store.deliveries) != 4 {
		t.Fatalf("routes = %#v", store.deliveries)
	}
	want := []struct {
		transport   event_outbox.Transport
		destination event_outbox.Destination
		key         string
	}{
		{event_outbox.TransportWebhook, event_outbox.DestinationGlobal, queue},
		{event_outbox.TransportWebhook, event_outbox.DestinationInstance, queue},
		{event_outbox.TransportRabbitMQ, event_outbox.DestinationInstance, queue},
		{event_outbox.TransportRabbitMQ, event_outbox.DestinationGlobal, "message"},
	}
	for index, expected := range want {
		actual := store.deliveries[index]
		if uuid.Validate(actual.ID) != nil || actual.Transport != expected.transport || actual.Destination != expected.destination || actual.RoutingKey != expected.key || string(actual.Payload) != string(payload) {
			t.Fatalf("route %d = %#v", index, actual)
		}
	}
}

func TestInstanceSubscriptionPreservesGroupNewsletterAndLegacyEmptyBehavior(t *testing.T) {
	t.Parallel()
	group := []byte(`{"event":"Message","data":{"Info":{"Chat":"120363@g.us"}}}`)
	newsletter := []byte(`{"event":"Receipt","data":{"Chat":"channel@newsletter"}}`)
	if !InstanceSubscribed("GROUP", "Message", group) {
		t.Fatal("group message was not selected by GROUP subscription")
	}
	if !InstanceSubscribed("NEWSLETTER", "Receipt", newsletter) {
		t.Fatal("newsletter receipt was not selected by NEWSLETTER subscription")
	}
	if InstanceSubscribed("", "Message", group) {
		t.Fatal("empty legacy subscription unexpectedly enabled delivery")
	}
	if InstanceSubscribed(" MESSAGE", "Message", group) {
		t.Fatal("legacy whitespace-invalid subscription unexpectedly enabled delivery")
	}
	if !InstanceSubscribed("MESSAGE", "ButtonClick", []byte(`{}`)) {
		t.Fatal("MESSAGE subscription did not retain ButtonClick compatibility")
	}
}

func TestGlobalRabbitSpecificEventsRemainAuthoritative(t *testing.T) {
	t.Parallel()
	emitter, err := NewEmitter(builderFunc(testBuilder), &recordingStore{}, Settings{
		GlobalRabbitEnabled: true, AMQPSpecificEvents: []string{"Receipt"}, AMQPGlobalEvents: []string{"MESSAGE"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := emitter.GlobalRabbitQueue("Message"); ok {
		t.Fatal("global group fallback bypassed configured specific-event priority")
	}
	if queue, ok := emitter.GlobalRabbitQueue("Receipt"); !ok || queue != "receipt" {
		t.Fatalf("specific queue = %q, %t", queue, ok)
	}
}

func TestGlobalRabbitLegacyEventAliasesRemainCompatible(t *testing.T) {
	t.Parallel()
	emitter, err := NewEmitter(builderFunc(testBuilder), &recordingStore{}, Settings{
		GlobalRabbitEnabled: true,
		AMQPGlobalEvents:    []string{"messages.upsert", "messages.update", "connection.update"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		event string
		queue string
		want  bool
	}{
		{event: "Message", queue: "message", want: true},
		{event: "SendMessage", queue: "sendmessage", want: true},
		{event: "Receipt", queue: "receipt", want: true},
		{event: "Connected", queue: "connected", want: true},
		{event: "CallOffer", want: false},
	} {
		t.Run(test.event, func(t *testing.T) {
			queue, ok := emitter.GlobalRabbitQueue(test.event)
			if ok != test.want || queue != test.queue {
				t.Fatalf("queue = %q, %t; want %q, %t", queue, ok, test.queue, test.want)
			}
		})
	}
}

func TestEmitterPropagatesAtomicFailure(t *testing.T) {
	t.Parallel()
	want := errors.New("transaction failed")
	store := &recordingStore{err: want}
	emitter, err := NewEmitter(builderFunc(testBuilder), store, Settings{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	instance := &instance_model.Instance{Id: uuid.NewString()}
	err = emitter.Record(context.Background(), Event{Instance: instance, Type: "Message", QueueName: "message", Payload: []byte(`{}`)})
	if !errors.Is(err, want) {
		t.Fatalf("record error = %v", err)
	}
}
