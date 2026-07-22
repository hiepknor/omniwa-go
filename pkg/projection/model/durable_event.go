package projection_model

import (
	"encoding/json"
	"time"
)

// DurableEvent is a normalized, retention-bound event history record. Summary
// must never contain raw provider payloads, credentials, or message content.
type DurableEvent struct {
	ID         string          `json:"id" gorm:"column:id;type:uuid;primaryKey"`
	InstanceID string          `json:"-" gorm:"column:instance_id;type:uuid;not null"`
	Type       string          `json:"type" gorm:"column:event_type;size:64;not null"`
	OccurredAt time.Time       `json:"occurredAt" gorm:"column:occurred_at;not null"`
	IngestedAt time.Time       `json:"ingestedAt" gorm:"column:ingested_at;not null"`
	ExpiresAt  time.Time       `json:"expiresAt" gorm:"column:expires_at;not null"`
	Summary    json.RawMessage `json:"summary" gorm:"column:summary;type:jsonb;not null"`
}

func (DurableEvent) TableName() string { return "durable_events" }
