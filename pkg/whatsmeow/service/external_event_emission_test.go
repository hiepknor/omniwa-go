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
	err   error
}

type compatibilityObserver struct {
	mu     sync.Mutex
	counts map[string]int
}

func newCompatibilityObserver() *compatibilityObserver {
	return &compatibilityObserver{counts: make(map[string]int)}
}

func (*compatibilityObserver) ObserveEmission(string, string, int) {}
func (*compatibilityObserver) ObserveRoute(event_outbox.Transport, event_outbox.Destination) {
}
func (o *compatibilityObserver) ObserveCompatibilityDispatch(transport event_outbox.Transport, outcome string) {
	o.mu.Lock()
	o.counts[string(transport)+":"+outcome]++
	o.mu.Unlock()
}

func (o *compatibilityObserver) count(transport event_outbox.Transport, outcome string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.counts[string(transport)+":"+outcome]
}

func (p *countingProducer) Produce(string, []byte, string, string) error {
	p.mu.Lock()
	p.calls++
	err := p.err
	p.mu.Unlock()
	return err
}

func (*countingProducer) CreateGlobalQueues() error { return nil }

func (p *countingProducer) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func TestEmitExternalEventWebhookCanaryKeepsOtherTransportsDirect(t *testing.T) {
	recorder := &emissionRecorder{}
	observer := newCompatibilityObserver()
	emitter, err := event_emission.NewEmitter(emissionBuilder{}, recorder, event_emission.Settings{
		DurableTransports: []string{"webhook"}, GlobalWebhookEnabled: true,
		GlobalRabbitEnabled: true, AMQPGlobalEvents: []string{"MESSAGE"},
	}, observer)
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
	for time.Now().Before(deadline) && (rabbit.count() != 2 || websocket.count() != 1 || nats.count() != 2 || observer.count(event_outbox.TransportRabbitMQ, "accepted") != 2) {
		time.Sleep(10 * time.Millisecond)
	}
	if recorder.count() != 2 || rabbit.count() != 2 || webhook.count() != 0 || websocket.count() != 1 || nats.count() != 2 {
		t.Fatalf("routes=%d rabbit=%d webhook=%d websocket=%d nats=%d", recorder.count(), rabbit.count(), webhook.count(), websocket.count(), nats.count())
	}
	if observer.count(event_outbox.TransportRabbitMQ, "accepted") != 2 || observer.count(event_outbox.TransportWebhook, "accepted") != 0 {
		t.Fatalf("unexpected compatibility telemetry: rabbit=%d webhook=%d", observer.count(event_outbox.TransportRabbitMQ, "accepted"), observer.count(event_outbox.TransportWebhook, "accepted"))
	}
}

func TestEmitExternalEventDefaultKeepsLegacyDirectFanout(t *testing.T) {
	recorder := &emissionRecorder{}
	observer := newCompatibilityObserver()
	emitter, err := event_emission.NewEmitter(emissionBuilder{}, recorder, event_emission.Settings{
		GlobalWebhookEnabled: true, GlobalRabbitEnabled: true, AMQPGlobalEvents: []string{"MESSAGE"},
	}, observer)
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
	for time.Now().Before(deadline) && (rabbit.count() != 2 || webhook.count() != 1 || websocket.count() != 1 || nats.count() != 2 || observer.count(event_outbox.TransportRabbitMQ, "accepted") != 2 || observer.count(event_outbox.TransportWebhook, "accepted") != 1) {
		time.Sleep(10 * time.Millisecond)
	}
	if recorder.count() != 0 || rabbit.count() != 2 || webhook.count() != 1 || websocket.count() != 1 || nats.count() != 2 {
		t.Fatalf("routes=%d rabbit=%d webhook=%d websocket=%d nats=%d", recorder.count(), rabbit.count(), webhook.count(), websocket.count(), nats.count())
	}
	if observer.count(event_outbox.TransportRabbitMQ, "accepted") != 2 || observer.count(event_outbox.TransportWebhook, "accepted") != 1 {
		t.Fatalf("unexpected compatibility telemetry: rabbit=%d webhook=%d", observer.count(event_outbox.TransportRabbitMQ, "accepted"), observer.count(event_outbox.TransportWebhook, "accepted"))
	}
}

func TestEmitExternalEventRabbitCanaryKeepsOtherTransportsDirect(t *testing.T) {
	recorder := &emissionRecorder{}
	observer := newCompatibilityObserver()
	emitter, err := event_emission.NewEmitter(emissionBuilder{}, recorder, event_emission.Settings{
		DurableTransports: []string{"rabbitmq"}, GlobalWebhookEnabled: true,
		GlobalRabbitEnabled: true, AMQPGlobalEvents: []string{"MESSAGE"},
	}, observer)
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
	for time.Now().Before(deadline) && (webhook.count() != 1 || websocket.count() != 1 || nats.count() != 2 || observer.count(event_outbox.TransportWebhook, "accepted") != 1) {
		time.Sleep(10 * time.Millisecond)
	}
	if recorder.count() != 2 || rabbit.count() != 0 || webhook.count() != 1 || websocket.count() != 1 || nats.count() != 2 {
		t.Fatalf("routes=%d rabbit=%d webhook=%d websocket=%d nats=%d", recorder.count(), rabbit.count(), webhook.count(), websocket.count(), nats.count())
	}
	if observer.count(event_outbox.TransportRabbitMQ, "accepted") != 0 || observer.count(event_outbox.TransportWebhook, "accepted") != 1 {
		t.Fatalf("unexpected compatibility telemetry: rabbit=%d webhook=%d", observer.count(event_outbox.TransportRabbitMQ, "accepted"), observer.count(event_outbox.TransportWebhook, "accepted"))
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

func TestCompatibilityTelemetryRecordsDirectAdmissionFailures(t *testing.T) {
	observer := newCompatibilityObserver()
	emitter, err := event_emission.NewEmitter(emissionBuilder{}, &emissionRecorder{}, event_emission.Settings{}, observer)
	if err != nil {
		t.Fatal(err)
	}
	settings := &config.Config{LogDirectory: t.TempDir(), LogMaxSize: 1, LogMaxBackups: 1, LogMaxAge: 1}
	service := &whatsmeowService{
		config: settings, rabbitmqProducer: &countingProducer{err: errors.New("rabbit unavailable")},
		webhookProducer: &countingProducer{err: errors.New("webhook queue full")},
		externalEvents:  emitter, loggerWrapper: logger_wrapper.NewLoggerManager(settings),
	}
	instanceID := uuid.NewString()
	service.sendToQueueOrWebhook(&instance_model.Instance{Id: instanceID, RabbitmqEnable: "enabled"}, instanceID+".message", []byte(`{"event":"Message"}`))
	service.sendToQueueOrWebhook(&instance_model.Instance{Id: instanceID, Webhook: "https://instance.example/events"}, instanceID+".message", []byte(`{"event":"Message"}`))

	if observer.count(event_outbox.TransportRabbitMQ, "failed") != 1 || observer.count(event_outbox.TransportWebhook, "failed") != 1 {
		t.Fatalf("unexpected compatibility failures: rabbit=%d webhook=%d", observer.count(event_outbox.TransportRabbitMQ, "failed"), observer.count(event_outbox.TransportWebhook, "failed"))
	}
}
