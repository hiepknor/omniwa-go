package campaign_model

import (
	"encoding/json"
	"time"
)

type CampaignStatus string
type CampaignTargetType string
type RecipientTargetType string

const (
	CampaignStatusDraft     CampaignStatus      = "draft"
	CampaignStatusScheduled CampaignStatus      = "scheduled"
	CampaignStatusRunning   CampaignStatus      = "running"
	CampaignStatusPaused    CampaignStatus      = "paused"
	CampaignStatusCompleted CampaignStatus      = "completed"
	CampaignStatusAborted   CampaignStatus      = "aborted"
	CampaignStatusFailed    CampaignStatus      = "failed"
	CampaignTargetDirect    CampaignTargetType  = "direct"
	CampaignTargetGroupList CampaignTargetType  = "group_list"
	RecipientTargetDirect   RecipientTargetType = "direct"
	RecipientTargetGroup    RecipientTargetType = "group"
)

type RecipientStatus string

const (
	RecipientStatusPending    RecipientStatus = "pending"
	RecipientStatusProcessing RecipientStatus = "processing"
	RecipientStatusSent       RecipientStatus = "sent"
	RecipientStatusDelivered  RecipientStatus = "delivered"
	RecipientStatusRead       RecipientStatus = "read"
	RecipientStatusFailed     RecipientStatus = "failed"
	RecipientStatusSkipped    RecipientStatus = "skipped"
	RecipientStatusAborted    RecipientStatus = "aborted"
)

type Campaign struct {
	ID                    string             `json:"id" gorm:"column:id;type:uuid;primaryKey"`
	InstanceID            string             `json:"instanceId" gorm:"column:instance_id;type:uuid;not null"`
	Name                  string             `json:"name" gorm:"column:name;size:255;not null"`
	Status                CampaignStatus     `json:"status" gorm:"column:status;size:32;not null"`
	ContentType           string             `json:"contentType" gorm:"column:content_type;size:32;not null"`
	TextBody              string             `json:"textBody" gorm:"column:text_body;type:text;not null"`
	TargetType            CampaignTargetType `json:"targetType" gorm:"column:target_type;size:32;not null"`
	GroupListID           *string            `json:"-" gorm:"column:group_list_id;type:uuid"`
	GroupListNameSnapshot *string            `json:"-" gorm:"column:group_list_name_snapshot;size:255"`
	GroupListVersion      *int64             `json:"-" gorm:"column:group_list_version"`
	StartsAt              *time.Time         `json:"startsAt,omitempty" gorm:"column:starts_at"`
	FinishedAt            *time.Time         `json:"finishedAt,omitempty" gorm:"column:finished_at"`
	StatusReason          *string            `json:"statusReason,omitempty" gorm:"column:status_reason;size:64"`
	PauseReason           *string            `json:"pauseReason,omitempty" gorm:"column:pause_reason;size:64"`
	RetryAt               *time.Time         `json:"retryAt,omitempty" gorm:"column:retry_at"`
	NeedsAttention        bool               `json:"needsAttention" gorm:"column:needs_attention;not null"`
	FailureSignalCount    int                `json:"-" gorm:"column:failure_signal_count;not null"`
	RateLimitSignalCount  int                `json:"-" gorm:"column:rate_limit_signal_count;not null"`
	Version               int64              `json:"version" gorm:"column:version;not null"`
	CreatedAt             time.Time          `json:"createdAt"`
	UpdatedAt             time.Time          `json:"updatedAt"`
}

func (Campaign) TableName() string { return "campaigns" }

type Recipient struct {
	ID                 string              `json:"id" gorm:"column:id;type:uuid;primaryKey"`
	CampaignID         string              `json:"campaignId" gorm:"column:campaign_id;type:uuid;not null"`
	InstanceID         string              `json:"instanceId" gorm:"column:instance_id;type:uuid;not null"`
	RecipientJID       string              `json:"recipientJid" gorm:"column:recipient_jid;size:255;not null"`
	TargetType         RecipientTargetType `json:"targetType" gorm:"column:target_type;size:32;not null"`
	TargetLabel        *string             `json:"targetLabel,omitempty" gorm:"column:target_label;size:255"`
	Status             RecipientStatus     `json:"status" gorm:"column:status;size:32;not null"`
	OptInSource        string              `json:"optInSource" gorm:"column:opt_in_source;size:64;not null"`
	OptInReferenceHash string              `json:"-" gorm:"column:opt_in_reference_hash;size:64;not null"`
	OptedInAt          time.Time           `json:"optedInAt" gorm:"column:opted_in_at;not null"`
	NextAttemptAt      time.Time           `json:"nextAttemptAt" gorm:"column:next_attempt_at;not null"`
	ClaimToken         *string             `json:"-" gorm:"column:claim_token;size:64"`
	LeaseUntil         *time.Time          `json:"-" gorm:"column:lease_until"`
	AttemptCount       int                 `json:"attemptCount" gorm:"column:attempt_count;not null"`
	ProviderMessageID  *string             `json:"providerMessageId,omitempty" gorm:"column:provider_message_id;size:255"`
	SentAt             *time.Time          `json:"sentAt,omitempty" gorm:"column:sent_at"`
	DeliveredAt        *time.Time          `json:"deliveredAt,omitempty" gorm:"column:delivered_at"`
	ReadAt             *time.Time          `json:"readAt,omitempty" gorm:"column:read_at"`
	LastErrorCode      *string             `json:"lastErrorCode,omitempty" gorm:"column:last_error_code;size:64"`
	CreatedAt          time.Time           `json:"createdAt"`
	UpdatedAt          time.Time           `json:"updatedAt"`
}

func (Recipient) TableName() string { return "campaign_recipients" }

type AuditEvent struct {
	ID                 string          `json:"id" gorm:"column:id;type:uuid;primaryKey"`
	CampaignID         string          `json:"campaignId" gorm:"column:campaign_id;type:uuid;not null"`
	InstanceID         string          `json:"instanceId" gorm:"column:instance_id;type:uuid;not null"`
	RecipientID        *string         `json:"recipientId,omitempty" gorm:"column:recipient_id;type:uuid"`
	EventType          string          `json:"eventType" gorm:"column:event_type;size:64;not null"`
	ActorType          string          `json:"actorType" gorm:"column:actor_type;size:32;not null"`
	ActorReferenceHash *string         `json:"-" gorm:"column:actor_reference_hash;size:64"`
	FromStatus         *string         `json:"fromStatus,omitempty" gorm:"column:from_status;size:32"`
	ToStatus           *string         `json:"toStatus,omitempty" gorm:"column:to_status;size:32"`
	Metadata           json.RawMessage `json:"metadata" gorm:"column:metadata;type:jsonb;not null"`
	OccurredAt         time.Time       `json:"occurredAt" gorm:"column:occurred_at;not null"`
}

func (AuditEvent) TableName() string { return "campaign_audit_events" }
