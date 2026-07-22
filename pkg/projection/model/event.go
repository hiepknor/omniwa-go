package projection_model

import (
	"encoding/json"
	"time"
)

type EventStatus string

const (
	EventStatusPending    EventStatus = "pending"
	EventStatusProcessing EventStatus = "processing"
	EventStatusProcessed  EventStatus = "processed"
	EventStatusFailed     EventStatus = "failed"
)

// Event is an internal durable inbox record. Payload and worker coordination
// fields must never be serialized by public API handlers.
type Event struct {
	InstanceID    string          `json:"-" gorm:"column:instance_id;type:uuid;primaryKey"`
	Resource      string          `json:"-" gorm:"column:resource;size:64;primaryKey"`
	EventKey      string          `json:"-" gorm:"column:event_key;size:255;primaryKey"`
	EntityKey     string          `json:"-" gorm:"column:entity_key;size:255;not null"`
	EventType     string          `json:"-" gorm:"column:event_type;size:64;not null"`
	OccurredAt    time.Time       `json:"-" gorm:"column:occurred_at;not null"`
	IngestedAt    time.Time       `json:"-" gorm:"column:ingested_at;not null"`
	AvailableAt   time.Time       `json:"-" gorm:"column:available_at;not null"`
	Status        EventStatus     `json:"-" gorm:"column:status;size:32;not null"`
	Payload       json.RawMessage `json:"-" gorm:"column:payload;type:jsonb;not null"`
	ClaimToken    *string         `json:"-" gorm:"column:claim_token;size:64"`
	LeaseUntil    *time.Time      `json:"-" gorm:"column:lease_until"`
	ProcessedAt   *time.Time      `json:"-" gorm:"column:processed_at"`
	RetryCount    int             `json:"-" gorm:"column:retry_count;not null"`
	LastErrorCode *string         `json:"-" gorm:"column:last_error_code;size:64"`
}

func (Event) TableName() string { return "projection_event_inbox" }
