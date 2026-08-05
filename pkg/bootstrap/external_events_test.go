package bootstrap

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/evolution-foundation/evolution-go/pkg/config"
	event_outbox "github.com/evolution-foundation/evolution-go/pkg/events/outbox"
	logger_wrapper "github.com/evolution-foundation/evolution-go/pkg/logger"
	"gorm.io/gorm"
)

type stubGlobalQueueCreator struct{ err error }

func (s stubGlobalQueueCreator) CreateGlobalQueues() error    { return s.err }
func (s stubGlobalQueueCreator) Health(context.Context) error { return s.err }

type stubExternalEventsObserver struct{}

func (stubExternalEventsObserver) ObservePhonePayloadPolicy(string) {}
func (stubExternalEventsObserver) ObserveAttempt(event_outbox.Transport, string, string, time.Duration) {
}
func (stubExternalEventsObserver) ObserveClaimed(int)                  {}
func (stubExternalEventsObserver) ObserveHealth(event_outbox.Health)   {}
func (stubExternalEventsObserver) ObserveInfrastructureFailure(string) {}

func TestNewExternalEventsRejectsMissingDependencies(t *testing.T) {
	module, err := NewExternalEvents(ExternalEventsDependencies{})
	if module != nil || err == nil {
		t.Fatalf("expected missing dependencies to fail, got module=%v err=%v", module, err)
	}
	if code := ExternalEventsErrorCode(err); code != ExternalEventsInvalidDependencies {
		t.Fatalf("expected %q, got %q", ExternalEventsInvalidDependencies, code)
	}
}

func TestNewExternalEventsRejectsInvalidSignatureConfiguration(t *testing.T) {
	cfg := &config.Config{}
	cfg.Webhook.SignatureEnabled = true
	cfg.Webhook.SignatureSecret = []byte("too-short")
	cfg.Webhook.SignatureKeyID = "production"

	module, err := NewExternalEvents(ExternalEventsDependencies{
		DB: &gorm.DB{}, Config: cfg, Logger: &logger_wrapper.LoggerManager{}, Observer: stubExternalEventsObserver{},
	})
	if module != nil || err == nil {
		t.Fatalf("expected invalid signature configuration to fail, got module=%v err=%v", module, err)
	}
	if code := ExternalEventsErrorCode(err); code != ExternalEventsInvalidSignatureConfig {
		t.Fatalf("expected %q, got %q", ExternalEventsInvalidSignatureConfig, code)
	}
}

func TestNewWebhookRequesterRejectsInvalidGlobalURL(t *testing.T) {
	requester, err := newWebhookRequester(&config.Config{WebhookUrl: "https://"})
	if requester != nil || err == nil {
		t.Fatalf("expected invalid URL to fail, got requester=%v err=%v", requester, err)
	}
	if code := ExternalEventsErrorCode(err); code != ExternalEventsInvalidWebhookURL {
		t.Fatalf("expected %q, got %q", ExternalEventsInvalidWebhookURL, code)
	}
}

func TestNewWebhookRequesterRejectsInvalidPolicy(t *testing.T) {
	cfg := &config.Config{}
	cfg.Webhook.AllowedHosts = []string{"hooks.example.test"}

	requester, err := newWebhookRequester(cfg)
	if requester != nil || err == nil {
		t.Fatalf("expected invalid policy to fail, got requester=%v err=%v", requester, err)
	}
	if code := ExternalEventsErrorCode(err); code != ExternalEventsInvalidWebhookPolicy {
		t.Fatalf("expected %q, got %q", ExternalEventsInvalidWebhookPolicy, code)
	}
}

func TestNewWebhookRequesterUsesGlobalURLWithoutMutatingConfig(t *testing.T) {
	cfg := &config.Config{WebhookUrl: "https://hooks.example.test:8443/events"}
	cfg.Webhook.Timeout = time.Second
	cfg.Webhook.MaxRequestBytes = 1024
	cfg.Webhook.MaxResponseBytes = 1024

	requester, err := newWebhookRequester(cfg)
	if err != nil {
		t.Fatalf("expected valid requester, got %v", err)
	}
	if requester == nil {
		t.Fatal("expected requester")
	}
	if len(cfg.Webhook.AllowedHosts) != 0 || len(cfg.Webhook.AllowedPorts) != 0 {
		t.Fatalf("configuration slices were mutated: hosts=%v ports=%v", cfg.Webhook.AllowedHosts, cfg.Webhook.AllowedPorts)
	}
}

func TestExternalEventsErrorCodeDoesNotExposeForeignErrors(t *testing.T) {
	if code := ExternalEventsErrorCode(errors.New("secret transport detail")); code != ExternalEventsInitializationFailed {
		t.Fatalf("expected bounded fallback code, got %q", code)
	}
}

func TestExternalEventsNilAccessorsFailClosed(t *testing.T) {
	var module *ExternalEvents
	if module.NATSProducer() != nil || module.OutboxRepository() != nil || module.OutboxWork() != nil {
		t.Fatal("nil module accessors must return nil dependencies")
	}
	if err := module.CreateGlobalRabbitMQQueues(); err == nil {
		t.Fatal("nil module must reject RabbitMQ queue creation")
	}
}

func TestExternalEventsDelegatesGlobalRabbitMQQueueCreation(t *testing.T) {
	expected := errors.New("queue setup failed")
	module := &ExternalEvents{rabbitMQ: stubGlobalQueueCreator{err: expected}}
	if err := module.CreateGlobalRabbitMQQueues(); !errors.Is(err, expected) {
		t.Fatalf("expected delegated error, got %v", err)
	}
}

func TestExternalEventsDelegatesRabbitMQHealthAndFailsClosed(t *testing.T) {
	expected := errors.New("RabbitMQ unavailable")
	module := &ExternalEvents{rabbitMQ: stubGlobalQueueCreator{err: expected}}
	if err := module.RabbitMQHealth(context.Background()); !errors.Is(err, expected) {
		t.Fatalf("expected delegated health error, got %v", err)
	}
	var missing *ExternalEvents
	if err := missing.RabbitMQHealth(context.Background()); err == nil {
		t.Fatal("nil module accepted RabbitMQ health")
	}
}
