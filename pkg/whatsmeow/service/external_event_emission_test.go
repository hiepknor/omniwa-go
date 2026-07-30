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

func (r *emissionRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.deliveries)
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

func TestEmitExternalEventWebhookCanaryKeepsOtherTransportsDirect(t *testing.T) {
	recorder := &emissionRecorder{}
	emitter, err := event_emission.NewEmitter(emissionBuilder{}, recorder, event_emission.Settings{
		DurableTransports: []string{"webhook"}, GlobalWebhookEnabled: true,
		GlobalRabbitEnabled: true, AMQPGlobalEvents: []string{"MESSAGE"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rabbit, webhook, websocket, nats := &countingProducer{}, &countingProducer{}, &countingProducer{}, &countingProducer{}
	settings := &config.Config{
		AmqpGlobalEnabled: true, NatsGlobalEnabled: true, NatsGlobalEvents: []string{"MESSAGE"},
		LogDirectory: t.TempDir(), LogMaxSize: 1, LogMaxBackups: 1, LogMaxAge: 1,
	}
	service := &whatsmeowService{
		config: settings, rabbitmqProducer: rabbit, webhookProducer: webhook,
		websocketProducer: websocket, natsProducer: nats, externalEvents: emitter,
		appCtx: context.Background(), loggerWrapper: logger_wrapper.NewLoggerManager(settings),
	}
	instance := &instance_model.Instance{
		Id: uuid.NewString(), Name: "test", Events: "ALL", Webhook: "https://instance.example/events",
		RabbitmqEnable: "enabled", WebSocketEnable: "enabled", NatsEnable: "enabled",
	}
	payload := []byte(`{"event":"Message","data":{"Info":{"Chat":"15551234567@s.whatsapp.net"}}}`)
	if !service.EmitExternalEvent(instance, "Message", nil, instance.Id+".message", payload) {
		t.Fatal("emission was rejected")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && (rabbit.count() != 2 || websocket.count() != 1 || nats.count() != 2) {
		time.Sleep(10 * time.Millisecond)
	}
	if recorder.count() != 2 || rabbit.count() != 2 || webhook.count() != 0 || websocket.count() != 1 || nats.count() != 2 {
		t.Fatalf("routes=%d rabbit=%d webhook=%d websocket=%d nats=%d", recorder.count(), rabbit.count(), webhook.count(), websocket.count(), nats.count())
	}
}

func TestEmitExternalEventDefaultKeepsLegacyDirectFanout(t *testing.T) {
	recorder := &emissionRecorder{}
	emitter, err := event_emission.NewEmitter(emissionBuilder{}, recorder, event_emission.Settings{
		GlobalWebhookEnabled: true, GlobalRabbitEnabled: true, AMQPGlobalEvents: []string{"MESSAGE"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rabbit, webhook, websocket, nats := &countingProducer{}, &countingProducer{}, &countingProducer{}, &countingProducer{}
	settings := &config.Config{
		AmqpGlobalEnabled: true, AmqpGlobalEvents: []string{"MESSAGE"}, NatsGlobalEnabled: true, NatsGlobalEvents: []string{"MESSAGE"},
		LogDirectory: t.TempDir(), LogMaxSize: 1, LogMaxBackups: 1, LogMaxAge: 1,
	}
	service := &whatsmeowService{
		config: settings, rabbitmqProducer: rabbit, webhookProducer: webhook,
		websocketProducer: websocket, natsProducer: nats, externalEvents: emitter,
		appCtx: context.Background(), loggerWrapper: logger_wrapper.NewLoggerManager(settings),
	}
	instance := &instance_model.Instance{
		Id: uuid.NewString(), Events: "ALL", Webhook: "https://instance.example/events",
		RabbitmqEnable: "enabled", WebSocketEnable: "enabled", NatsEnable: "enabled",
	}
	payload := []byte(`{"event":"Message"}`)
	if !service.EmitExternalEvent(instance, "Message", nil, instance.Id+".message", payload) {
		t.Fatal("emission was rejected")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && (rabbit.count() != 2 || webhook.count() != 1 || websocket.count() != 1 || nats.count() != 2) {
		time.Sleep(10 * time.Millisecond)
	}
	if recorder.count() != 0 || rabbit.count() != 2 || webhook.count() != 1 || websocket.count() != 1 || nats.count() != 2 {
		t.Fatalf("routes=%d rabbit=%d webhook=%d websocket=%d nats=%d", recorder.count(), rabbit.count(), webhook.count(), websocket.count(), nats.count())
	}
}

func TestEmitExternalEventRabbitCanaryKeepsOtherTransportsDirect(t *testing.T) {
	recorder := &emissionRecorder{}
	emitter, err := event_emission.NewEmitter(emissionBuilder{}, recorder, event_emission.Settings{
		DurableTransports: []string{"rabbitmq"}, GlobalWebhookEnabled: true,
		GlobalRabbitEnabled: true, AMQPGlobalEvents: []string{"MESSAGE"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rabbit, webhook, websocket, nats := &countingProducer{}, &countingProducer{}, &countingProducer{}, &countingProducer{}
	settings := &config.Config{
		AmqpGlobalEnabled: true, NatsGlobalEnabled: true, NatsGlobalEvents: []string{"MESSAGE"},
		LogDirectory: t.TempDir(), LogMaxSize: 1, LogMaxBackups: 1, LogMaxAge: 1,
	}
	service := &whatsmeowService{
		config: settings, rabbitmqProducer: rabbit, webhookProducer: webhook,
		websocketProducer: websocket, natsProducer: nats, externalEvents: emitter,
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
	for time.Now().Before(deadline) && (webhook.count() != 1 || websocket.count() != 1 || nats.count() != 2) {
		time.Sleep(10 * time.Millisecond)
	}
	if recorder.count() != 2 || rabbit.count() != 0 || webhook.count() != 1 || websocket.count() != 1 || nats.count() != 2 {
		t.Fatalf("routes=%d rabbit=%d webhook=%d websocket=%d nats=%d", recorder.count(), rabbit.count(), webhook.count(), websocket.count(), nats.count())
	}
}

func TestEmitExternalEventAtomicFailureSuppressesEveryDirectTransport(t *testing.T) {
	recorder := &emissionRecorder{err: errors.New("atomic write failed")}
	emitter, err := event_emission.NewEmitter(emissionBuilder{}, recorder, event_emission.Settings{
		GlobalWebhookEnabled: true, GlobalRabbitEnabled: true, AMQPGlobalEvents: []string{"MESSAGE"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rabbit, webhook, websocket, nats := &countingProducer{}, &countingProducer{}, &countingProducer{}, &countingProducer{}
	settings := &config.Config{
		AmqpGlobalEnabled: true, AmqpGlobalEvents: []string{"MESSAGE"}, NatsGlobalEnabled: true, NatsGlobalEvents: []string{"MESSAGE"},
		LogDirectory: t.TempDir(), LogMaxSize: 1, LogMaxBackups: 1, LogMaxAge: 1,
	}
	service := &whatsmeowService{
		config: settings, rabbitmqProducer: rabbit, webhookProducer: webhook,
		websocketProducer: websocket, natsProducer: nats, externalEvents: emitter,
		appCtx: context.Background(), loggerWrapper: logger_wrapper.NewLoggerManager(settings),
	}
	instance := &instance_model.Instance{
		Id: uuid.NewString(), Events: "ALL", Webhook: "https://instance.example/events",
		RabbitmqEnable: "enabled", WebSocketEnable: "enabled", NatsEnable: "enabled",
	}
	if service.EmitExternalEvent(instance, "Message", nil, instance.Id+".message", []byte(`{"event":"Message"}`)) {
		t.Fatal("failed atomic record was accepted")
	}
	time.Sleep(50 * time.Millisecond)
	if rabbit.count() != 0 || webhook.count() != 0 || websocket.count() != 0 || nats.count() != 0 {
		t.Fatalf("direct fan-out occurred after failure: rabbit=%d webhook=%d websocket=%d nats=%d", rabbit.count(), webhook.count(), websocket.count(), nats.count())
	}
}
