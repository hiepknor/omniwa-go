package group_model

import (
	"encoding/json"
	"time"
)

type ManagementCommandStatus string

const (
	ManagementCommandRequested          ManagementCommandStatus = "requested"
	ManagementCommandExecuting          ManagementCommandStatus = "executing"
	ManagementCommandCompleted          ManagementCommandStatus = "completed"
	ManagementCommandPartiallyCompleted ManagementCommandStatus = "partially_completed"
	ManagementCommandFailed             ManagementCommandStatus = "failed"
	ManagementCommandUnknown            ManagementCommandStatus = "unknown"
)

type ManagementCommand struct {
	ID                 string                  `json:"id" gorm:"column:id;type:uuid;primaryKey"`
	InstanceID         string                  `json:"-" gorm:"column:instance_id;type:uuid;not null"`
	GroupJID           string                  `json:"groupJid" gorm:"column:group_jid;size:255;not null"`
	CommandType        string                  `json:"command" gorm:"column:command_type;size:64;not null"`
	Status             ManagementCommandStatus `json:"commandStatus" gorm:"column:status;size:32;not null"`
	IdempotencyKeyHash *string                 `json:"-" gorm:"column:idempotency_key_hash;size:64"`
	RequestFingerprint string                  `json:"-" gorm:"column:request_fingerprint;size:64;not null"`
	RequestID          *string                 `json:"requestId,omitempty" gorm:"column:request_id;size:255"`
	ActorType          string                  `json:"-" gorm:"column:actor_type;size:32;not null"`
	ActorReferenceHash string                  `json:"-" gorm:"column:actor_reference_hash;size:64;not null"`
	SafeOutcome        json.RawMessage         `json:"outcome" gorm:"column:safe_outcome;type:jsonb;not null"`
	ExecutionStartedAt *time.Time              `json:"-" gorm:"column:execution_started_at"`
	CompletedAt        *time.Time              `json:"completedAt,omitempty" gorm:"column:completed_at"`
	CreatedAt          time.Time               `json:"createdAt" gorm:"column:created_at;not null"`
	UpdatedAt          time.Time               `json:"updatedAt" gorm:"column:updated_at;not null"`
}

func (ManagementCommand) TableName() string { return "group_management_commands" }

type ManagementAuditEvent struct {
	ID                 string          `json:"id" gorm:"column:id;type:uuid;primaryKey"`
	CommandID          string          `json:"commandId" gorm:"column:command_id;type:uuid;not null"`
	InstanceID         string          `json:"-" gorm:"column:instance_id;type:uuid;not null"`
	GroupJID           string          `json:"-" gorm:"column:group_jid;size:255;not null"`
	EventType          string          `json:"eventType" gorm:"column:event_type;size:32;not null"`
	ActorType          string          `json:"actorType" gorm:"column:actor_type;size:32;not null"`
	ActorReferenceHash string          `json:"-" gorm:"column:actor_reference_hash;size:64;not null"`
	Summary            json.RawMessage `json:"summary" gorm:"column:summary;type:jsonb;not null"`
	OccurredAt         time.Time       `json:"occurredAt" gorm:"column:occurred_at;not null"`
}

func (ManagementAuditEvent) TableName() string { return "group_management_audit_events" }
