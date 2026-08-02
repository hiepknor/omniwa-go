package whatsmeow_service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/evolution-foundation/evolution-go/pkg/config"
	event_emission "github.com/evolution-foundation/evolution-go/pkg/events/emission"
	event_outbox "github.com/evolution-foundation/evolution-go/pkg/events/outbox"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	logger_wrapper "github.com/evolution-foundation/evolution-go/pkg/logger"
	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	"github.com/google/uuid"
	"go.mau.fi/whatsmeow/types"
)

type emissionBuilder struct{}

func (emissionBuilder) Build(instanceID, eventType string, _ any) (*projection_model.DurableEvent, error) {
	now := time.Now().UTC()
	return &projection_model.DurableEvent{
		ID: uuid.NewString(), InstanceID: instanceID, Type: eventType,
		OccurredAt: now, IngestedAt: now, ExpiresAt: now.Add(time.Hour), Summary: json.RawMessage(`{}`),
	}, nil
}

type emissionRecorder struct {
	mu         sync.Mutex
	deliveries []event_outbox.Delivery
	err        error
}

func (r *emissionRecorder) Record(_ context.Context, _ *projection_model.DurableEvent, deliveries []event_outbox.Delivery) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deliveries = append([]event_outbox.Delivery(nil), deliveries...)
	return r.err
}

func (r *emissionRecorder) routes() []event_outbox.Delivery {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]event_outbox.Delivery(nil), r.deliveries...)
}

type countingProducer struct {
	mu    sync.Mutex
	calls int
}

func (p *countingProducer) Produce(string, []byte, string, string) error {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	return nil
}

func (*countingProducer) CreateGlobalQueues() error { return nil }

func (p *countingProducer) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func TestEmitExternalEventUsesDurableWebhookRabbitAndDirectRealtime(t *testing.T) {
	recorder := &emissionRecorder{}
	emitter, err := event_emission.NewEmitter(emissionBuilder{}, recorder, event_emission.Settings{
		GlobalWebhookEnabled: true, GlobalRabbitEnabled: true, AMQPGlobalEvents: []string{"MESSAGE"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	websocket, nats := &countingProducer{}, &countingProducer{}
	settings := &config.Config{
		NatsGlobalEnabled: true, NatsGlobalEvents: []string{"MESSAGE"},
		LogDirectory: t.TempDir(), LogMaxSize: 1, LogMaxBackups: 1, LogMaxAge: 1,
	}
	service := &whatsmeowService{
		config: settings, websocketProducer: websocket, natsProducer: nats, externalEvents: emitter,
		appCtx: context.Background(), loggerWrapper: logger_wrapper.NewLoggerManager(settings),
	}
	instance := &instance_model.Instance{
		Id: uuid.NewString(), Events: "ALL", Webhook: "https://instance.example/events",
		RabbitmqEnable: "enabled", WebSocketEnable: "enabled", NatsEnable: "enabled",
	}
	if !service.EmitExternalEvent(instance, "Message", nil, instance.Id+".message", []byte(`{"event":"Message"}`)) {
		t.Fatal("emission was rejected")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && (websocket.count() != 1 || nats.count() != 2) {
		time.Sleep(time.Millisecond)
	}
	routes := recorder.routes()
	if len(routes) != 4 || websocket.count() != 1 || nats.count() != 2 {
		t.Fatalf("routes=%#v websocket=%d nats=%d", routes, websocket.count(), nats.count())
	}
	for _, route := range routes {
		if route.Transport != event_outbox.TransportWebhook && route.Transport != event_outbox.TransportRabbitMQ {
			t.Fatalf("non-durable transport entered outbox: %#v", route)
		}
	}
}

func TestEmitExternalEventAtomicFailureSuppressesRealtimeFanout(t *testing.T) {
	recorder := &emissionRecorder{err: errors.New("atomic write failed")}
	emitter, err := event_emission.NewEmitter(emissionBuilder{}, recorder, event_emission.Settings{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	websocket, nats := &countingProducer{}, &countingProducer{}
	settings := &config.Config{NatsGlobalEnabled: true, NatsGlobalEvents: []string{"MESSAGE"}, LogDirectory: t.TempDir(), LogMaxSize: 1, LogMaxBackups: 1, LogMaxAge: 1}
	service := &whatsmeowService{
		config: settings, websocketProducer: websocket, natsProducer: nats, externalEvents: emitter,
		appCtx: context.Background(), loggerWrapper: logger_wrapper.NewLoggerManager(settings),
	}
	instance := &instance_model.Instance{Id: uuid.NewString(), Events: "ALL", WebSocketEnable: "enabled", NatsEnable: "enabled"}
	if service.EmitExternalEvent(instance, "Message", nil, instance.Id+".message", []byte(`{"event":"Message"}`)) {
		t.Fatal("failed atomic record was accepted")
	}
	time.Sleep(25 * time.Millisecond)
	if websocket.count() != 0 || nats.count() != 0 {
		t.Fatalf("realtime fan-out occurred after failure: websocket=%d nats=%d", websocket.count(), nats.count())
	}
}

func TestEmitExternalEventEnrichesOutboundPhoneMetadataBeforeOutboxRecord(t *testing.T) {
	recorder := &emissionRecorder{}
	emitter, err := event_emission.NewEmitter(emissionBuilder{}, recorder, event_emission.Settings{
		GlobalRabbitEnabled: true, AMQPGlobalEvents: []string{"SEND_MESSAGE"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	settings := &config.Config{
		PhoneNumberExposureEnabled: true,
		LogDirectory:               t.TempDir(), LogMaxSize: 1, LogMaxBackups: 1, LogMaxAge: 1,
	}
	service := &whatsmeowService{
		config: settings, externalEvents: emitter, appCtx: context.Background(),
		loggerWrapper: logger_wrapper.NewLoggerManager(settings),
	}
	instance := &instance_model.Instance{Id: uuid.NewString()}
	info := types.MessageInfo{MessageSource: types.MessageSource{
		Sender: mustPhoneTestJID(t, "15550006@s.whatsapp.net"),
		Chat:   mustPhoneTestJID(t, "15550007@s.whatsapp.net"), IsFromMe: true,
	}}
	if !service.EmitExternalEvent(instance, "SendMessage", info, instance.Id+".sendmessage", []byte(`{"event":"SendMessage","data":{"legacy":"kept"}}`)) {
		t.Fatal("emission was rejected")
	}
	routes := recorder.routes()
	if len(routes) != 1 || routes[0].Transport != event_outbox.TransportRabbitMQ {
		t.Fatalf("routes=%#v", routes)
	}
	var root map[string]any
	if err := json.Unmarshal(routes[0].Payload, &root); err != nil {
		t.Fatal(err)
	}
	data := root["data"].(map[string]any)
	if data["senderPhoneNumber"] != "15550006" || data["recipientPhoneNumber"] != "15550007" || data["legacy"] != "kept" {
		t.Fatalf("data=%#v", data)
	}
}
