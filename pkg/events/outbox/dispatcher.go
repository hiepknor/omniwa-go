package outbox

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
	gorm_logger "gorm.io/gorm/logger"
)

type WebhookDeliverer interface {
	DeliverConfirmed(context.Context, string, []byte, string) error
}

type RabbitMQDeliverer interface {
	DeliverConfirmed(context.Context, string, []byte, string, string) error
}

type TargetResolver interface {
	WebhookTarget(context.Context, Destination, string) (string, error)
	RabbitMQMode(context.Context, Destination, string) (string, error)
}

type TransportDispatcher struct {
	webhook WebhookDeliverer
	rabbit  RabbitMQDeliverer
	targets TargetResolver
}

func NewTransportDispatcher(webhook WebhookDeliverer, rabbit RabbitMQDeliverer, targets TargetResolver) (*TransportDispatcher, error) {
	if webhook == nil || rabbit == nil || targets == nil {
		return nil, errors.New("external event outbox transport dependencies are required")
	}
	return &TransportDispatcher{webhook: webhook, rabbit: rabbit, targets: targets}, nil
}

func (d *TransportDispatcher) Deliver(ctx context.Context, delivery Delivery) error {
	if d == nil || ctx == nil || uuid.Validate(delivery.ID) != nil || uuid.Validate(delivery.InstanceID) != nil {
		return &DeliveryError{Code: "invalid_delivery", Retryable: false}
	}
	switch delivery.Transport {
	case TransportWebhook:
		target, err := d.targets.WebhookTarget(ctx, delivery.Destination, delivery.InstanceID)
		if err != nil {
			return err
		}
		if err := d.webhook.DeliverConfirmed(ctx, target, delivery.Payload, delivery.ID); err != nil {
			return translateTransportError(err)
		}
		return nil
	case TransportRabbitMQ:
		mode, err := d.targets.RabbitMQMode(ctx, delivery.Destination, delivery.InstanceID)
		if err != nil {
			return err
		}
		if err := d.rabbit.DeliverConfirmed(ctx, delivery.RoutingKey, delivery.Payload, mode, delivery.ID); err != nil {
			return translateTransportError(err)
		}
		return nil
	case TransportNATS:
		return &DeliveryError{Code: "transport_not_supported", Retryable: false}
	default:
		return &DeliveryError{Code: "transport_not_supported", Retryable: false}
	}
}

type classifiedTransportError interface {
	DeliveryCode() string
	DeliveryRetryable() bool
}

func translateTransportError(err error) error {
	var classified classifiedTransportError
	if errors.As(err, &classified) && safeDeliveryCode.MatchString(classified.DeliveryCode()) {
		return &DeliveryError{Code: classified.DeliveryCode(), Retryable: classified.DeliveryRetryable(), Cause: err}
	}
	return &DeliveryError{Code: "delivery_failed", Retryable: true, Cause: err}
}

type DatabaseTargetResolver struct {
	db                    *gorm.DB
	globalWebhook         string
	globalRabbitMQEnabled bool
}

func NewDatabaseTargetResolver(db *gorm.DB, globalWebhook string, globalRabbitMQEnabled bool) (*DatabaseTargetResolver, error) {
	if db == nil {
		return nil, errors.New("external event outbox target database is required")
	}
	safeDB := db.Session(&gorm.Session{Logger: db.Logger.LogMode(gorm_logger.Silent)})
	return &DatabaseTargetResolver{db: safeDB, globalWebhook: strings.TrimSpace(globalWebhook), globalRabbitMQEnabled: globalRabbitMQEnabled}, nil
}

func (r *DatabaseTargetResolver) WebhookTarget(ctx context.Context, destination Destination, instanceID string) (string, error) {
	if r == nil || r.db == nil || ctx == nil || uuid.Validate(instanceID) != nil {
		return "", &DeliveryError{Code: "invalid_destination", Retryable: false}
	}
	if destination == DestinationGlobal {
		if r.globalWebhook == "" {
			return "", &DeliveryError{Code: "destination_disabled", Retryable: false}
		}
		return r.globalWebhook, nil
	}
	if destination != DestinationInstance {
		return "", &DeliveryError{Code: "invalid_destination", Retryable: false}
	}
	var row struct{ Webhook string }
	result := r.db.WithContext(ctx).Table("instances").Select("webhook").Where("id = ?", instanceID).Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return "", &DeliveryError{Code: "instance_not_found", Retryable: false}
	}
	if result.Error != nil {
		return "", &DeliveryError{Code: "target_lookup_failed", Retryable: true, Cause: result.Error}
	}
	if strings.TrimSpace(row.Webhook) == "" {
		return "", &DeliveryError{Code: "destination_disabled", Retryable: false}
	}
	return strings.TrimSpace(row.Webhook), nil
}

func (r *DatabaseTargetResolver) RabbitMQMode(ctx context.Context, destination Destination, instanceID string) (string, error) {
	if r == nil || r.db == nil || ctx == nil || uuid.Validate(instanceID) != nil {
		return "", &DeliveryError{Code: "invalid_destination", Retryable: false}
	}
	if destination == DestinationGlobal {
		if !r.globalRabbitMQEnabled {
			return "", &DeliveryError{Code: "destination_disabled", Retryable: false}
		}
		return "global", nil
	}
	if destination != DestinationInstance {
		return "", &DeliveryError{Code: "invalid_destination", Retryable: false}
	}
	var row struct{ RabbitmqEnable string }
	result := r.db.WithContext(ctx).Table("instances").Select("rabbitmq_enable").Where("id = ?", instanceID).Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return "", &DeliveryError{Code: "instance_not_found", Retryable: false}
	}
	if result.Error != nil {
		return "", &DeliveryError{Code: "target_lookup_failed", Retryable: true, Cause: result.Error}
	}
	if row.RabbitmqEnable != "enabled" {
		return "", &DeliveryError{Code: "destination_disabled", Retryable: false}
	}
	return "enabled", nil
}
