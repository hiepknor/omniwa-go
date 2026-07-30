package outbox

import (
	"encoding/json"
	"time"
)

type Transport string

const (
	TransportWebhook  Transport = "webhook"
	TransportRabbitMQ Transport = "rabbitmq"
	TransportNATS     Transport = "nats"
)

type Destination string

const (
	DestinationInstance Destination = "instance"
	DestinationGlobal   Destination = "global"
)

type Status string

const (
	StatusPending    Status = "pending"
	StatusProcessing Status = "processing"
	StatusDelivered  Status = "delivered"
	StatusDeadLetter Status = "dead_letter"
)

const (
	RetryPolicyVersion = 1
	DefaultMaxAttempts = 12
	MaxPayloadBytes    = 1 << 20
)

// Delivery is an internal, retention-bound external-delivery record. Payload
// can contain message content required by an existing event consumer and must
// never be serialized by a public API handler or written to logs.
type Delivery struct {
	ID                 string          `json:"-" gorm:"column:id;type:uuid;primaryKey"`
	DurableEventID     string          `json:"-" gorm:"column:durable_event_id;type:uuid;not null"`
	InstanceID         string          `json:"-" gorm:"column:instance_id;type:uuid;not null"`
	Transport          Transport       `json:"-" gorm:"column:transport;size:16;not null"`
	Destination        Destination     `json:"-" gorm:"column:destination;size:16;not null"`
	RoutingKey         string          `json:"-" gorm:"column:routing_key;size:255;not null"`
	Payload            json.RawMessage `json:"-" gorm:"column:payload;type:jsonb;not null"`
	Status             Status          `json:"-" gorm:"column:status;size:32;not null"`
	AvailableAt        time.Time       `json:"-" gorm:"column:available_at;not null"`
	ClaimToken         *string         `json:"-" gorm:"column:claim_token;type:uuid"`
	LeaseUntil         *time.Time      `json:"-" gorm:"column:lease_until"`
	AttemptCount       int             `json:"-" gorm:"column:attempt_count;not null"`
	RetryPolicyVersion int             `json:"-" gorm:"column:retry_policy_version;not null"`
	MaxAttempts        int             `json:"-" gorm:"column:max_attempts;not null"`
	LastAttemptAt      *time.Time      `json:"-" gorm:"column:last_attempt_at"`
	LastErrorCode      *string         `json:"-" gorm:"column:last_error_code;size:64"`
	DeliveredAt        *time.Time      `json:"-" gorm:"column:delivered_at"`
	DeadLetteredAt     *time.Time      `json:"-" gorm:"column:dead_lettered_at"`
	CreatedAt          time.Time       `json:"-" gorm:"column:created_at;not null"`
	UpdatedAt          time.Time       `json:"-" gorm:"column:updated_at;not null"`
}

func (Delivery) TableName() string { return "external_event_outbox" }
