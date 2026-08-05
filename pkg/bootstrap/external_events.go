package bootstrap

import (
	"context"
	"errors"
	"net/url"

	"github.com/evolution-foundation/evolution-go/pkg/config"
	producer_interfaces "github.com/evolution-foundation/evolution-go/pkg/events/interfaces"
	nats_producer "github.com/evolution-foundation/evolution-go/pkg/events/nats"
	event_outbox "github.com/evolution-foundation/evolution-go/pkg/events/outbox"
	event_payload "github.com/evolution-foundation/evolution-go/pkg/events/payload"
	rabbitmq_producer "github.com/evolution-foundation/evolution-go/pkg/events/rabbitmq"
	webhook_producer "github.com/evolution-foundation/evolution-go/pkg/events/webhook"
	logger_wrapper "github.com/evolution-foundation/evolution-go/pkg/logger"
	"github.com/evolution-foundation/evolution-go/pkg/netguard"
	amqp "github.com/rabbitmq/amqp091-go"
	"gorm.io/gorm"
)

const (
	ExternalEventsInvalidDependencies        = "invalid_dependencies"
	ExternalEventsInvalidWebhookURL          = "invalid_webhook_url"
	ExternalEventsInvalidWebhookPolicy       = "invalid_webhook_policy"
	ExternalEventsInvalidSignatureConfig     = "invalid_signature_config"
	ExternalEventsTargetResolverUnavailable  = "target_resolver_unavailable"
	ExternalEventsDispatcherUnavailable      = "dispatcher_unavailable"
	ExternalEventsInvalidWorkerConfiguration = "invalid_worker_configuration"
	ExternalEventsInitializationFailed       = "initialization_failed"
)

type externalEventsInitError struct {
	code  string
	cause error
}

func (e *externalEventsInitError) Error() string {
	return "external events initialization failed: " + e.code
}
func (e *externalEventsInitError) Unwrap() error { return e.cause }

// ExternalEventsErrorCode returns a bounded startup error code without
// disclosing webhook URLs, credentials, or transport error details.
func ExternalEventsErrorCode(err error) string {
	var initErr *externalEventsInitError
	if errors.As(err, &initErr) {
		return initErr.code
	}
	return ExternalEventsInitializationFailed
}

type globalQueueCreator interface {
	CreateGlobalQueues() error
	Health(context.Context) error
}

type ExternalEventsObserver interface {
	event_payload.PhonePayloadObserver
	event_outbox.Observer
}

// ExternalEvents is the process-composition result for outbound event
// transports. Domain event behavior remains owned by pkg/events.
type ExternalEvents struct {
	nats           producer_interfaces.Producer
	outbox         event_outbox.Repository
	outboxFailures event_outbox.FailureRepository
	outboxWorker   Work
	rabbitMQ       globalQueueCreator
}

type ExternalEventsDependencies struct {
	DB             *gorm.DB
	Config         *config.Config
	AMQPConnection *amqp.Connection
	Logger         *logger_wrapper.LoggerManager
	Observer       ExternalEventsObserver
}

func NewExternalEvents(deps ExternalEventsDependencies) (*ExternalEvents, error) {
	if deps.DB == nil || deps.Config == nil || deps.Logger == nil || deps.Observer == nil {
		return nil, &externalEventsInitError{code: ExternalEventsInvalidDependencies}
	}

	// Keep the URL even if the initial connection failed so the durable worker
	// can reconnect on a later delivery attempt.
	rabbitMQProducer := rabbitmq_producer.NewRabbitMQProducer(
		deps.AMQPConnection,
		deps.Config.AmqpGlobalEnabled,
		deps.Config.AmqpGlobalEvents,
		deps.Config.AmqpSpecificEvents,
		deps.Config.AmqpUrl,
		deps.Logger,
	)
	natsProducer := nats_producer.NewNatsProducer(
		deps.Config.NatsUrl,
		deps.Config.NatsGlobalEnabled,
		deps.Config.NatsGlobalEvents,
		deps.Logger,
	)

	requester, err := newWebhookRequester(deps.Config)
	if err != nil {
		return nil, err
	}
	webhookProducer := webhook_producer.NewWebhookProducer(requester)
	if deps.Config.Webhook.SignatureEnabled {
		webhookProducer, err = webhook_producer.NewSignedWebhookProducer(
			requester,
			deps.Config.Webhook.SignatureSecret,
			deps.Config.Webhook.SignatureKeyID,
		)
		if err != nil {
			return nil, &externalEventsInitError{code: ExternalEventsInvalidSignatureConfig, cause: err}
		}
	}

	outboxRepository := event_outbox.NewRepository(deps.DB)
	targetResolver, err := event_outbox.NewDatabaseTargetResolver(
		deps.DB,
		deps.Config.WebhookUrl,
		deps.Config.AmqpGlobalEnabled,
	)
	if err != nil {
		return nil, &externalEventsInitError{code: ExternalEventsTargetResolverUnavailable, cause: err}
	}
	phonePayloadPolicy := event_payload.NewPhonePayloadPolicy(deps.Config.PhoneNumberExposureEnabled, deps.Observer)
	dispatcher, err := event_outbox.NewTransportDispatcher(webhookProducer, rabbitMQProducer, targetResolver, phonePayloadPolicy)
	if err != nil {
		return nil, &externalEventsInitError{code: ExternalEventsDispatcherUnavailable, cause: err}
	}
	outboxWorker, err := event_outbox.NewWorker(
		outboxRepository,
		dispatcher,
		event_outbox.Settings{
			BatchSize:      deps.Config.ExternalEventOutbox.BatchSize,
			LeaseDuration:  deps.Config.ExternalEventOutbox.LeaseDuration,
			PollInterval:   deps.Config.ExternalEventOutbox.PollInterval,
			AttemptTimeout: deps.Config.ExternalEventOutbox.AttemptTimeout,
			StateTimeout:   deps.Config.ExternalEventOutbox.StateTimeout,
			RetryBase:      deps.Config.ExternalEventOutbox.RetryBase,
			RetryMax:       deps.Config.ExternalEventOutbox.RetryMax,
		},
		deps.Observer,
	)
	if err != nil {
		return nil, &externalEventsInitError{code: ExternalEventsInvalidWorkerConfiguration, cause: err}
	}

	return &ExternalEvents{
		nats:           natsProducer,
		outbox:         outboxRepository,
		outboxFailures: outboxRepository,
		outboxWorker:   outboxWorker.Run,
		rabbitMQ:       rabbitMQProducer,
	}, nil
}

func (e *ExternalEvents) NATSProducer() producer_interfaces.Producer {
	if e == nil {
		return nil
	}
	return e.nats
}

func (e *ExternalEvents) OutboxRepository() event_outbox.Repository {
	if e == nil {
		return nil
	}
	return e.outbox
}

func (e *ExternalEvents) OutboxFailureRepository() event_outbox.FailureRepository {
	if e == nil {
		return nil
	}
	return e.outboxFailures
}

func (e *ExternalEvents) OutboxWork() Work {
	if e == nil {
		return nil
	}
	return e.outboxWorker
}

func (e *ExternalEvents) RabbitMQHealth(ctx context.Context) error {
	if e == nil || e.rabbitMQ == nil {
		return errors.New("external events RabbitMQ producer is unavailable")
	}
	return e.rabbitMQ.Health(ctx)
}

func newWebhookRequester(cfg *config.Config) (netguard.Requester, error) {
	if cfg == nil {
		return nil, &externalEventsInitError{code: ExternalEventsInvalidDependencies}
	}
	webhookHosts := append([]string(nil), cfg.Webhook.AllowedHosts...)
	webhookPorts := append([]string(nil), cfg.Webhook.AllowedPorts...)
	if cfg.WebhookUrl != "" {
		globalWebhookURL, err := url.Parse(cfg.WebhookUrl)
		if err != nil || globalWebhookURL.Hostname() == "" {
			return nil, &externalEventsInitError{code: ExternalEventsInvalidWebhookURL, cause: err}
		}
		webhookHosts = append(webhookHosts, globalWebhookURL.Hostname())
		if globalWebhookURL.Port() != "" {
			webhookPorts = append(webhookPorts, globalWebhookURL.Port())
		}
	}
	if len(webhookHosts) == 0 {
		return nil, nil
	}
	requester, err := netguard.NewRequester(netguard.RequestSettings{
		AllowedHosts:     webhookHosts,
		AllowedPorts:     webhookPorts,
		AllowPrivate:     cfg.Webhook.AllowPrivate,
		Timeout:          cfg.Webhook.Timeout,
		MaxRequestBytes:  cfg.Webhook.MaxRequestBytes,
		MaxResponseBytes: cfg.Webhook.MaxResponseBytes,
	})
	if err != nil {
		return nil, &externalEventsInitError{code: ExternalEventsInvalidWebhookPolicy, cause: err}
	}
	return requester, nil
}

func (e *ExternalEvents) CreateGlobalRabbitMQQueues() error {
	if e == nil || e.rabbitMQ == nil {
		return errors.New("external events RabbitMQ producer is unavailable")
	}
	return e.rabbitMQ.CreateGlobalQueues()
}
