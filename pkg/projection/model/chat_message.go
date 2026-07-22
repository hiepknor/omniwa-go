package projection_model

import (
	"encoding/json"
	"time"
)

type ChatType string

const (
	ChatTypeDirect     ChatType = "direct"
	ChatTypeGroup      ChatType = "group"
	ChatTypeNewsletter ChatType = "newsletter"
	ChatTypeBroadcast  ChatType = "broadcast"
	ChatTypeUnknown    ChatType = "unknown"
)

type MessageDirection string

const (
	MessageDirectionIncoming MessageDirection = "incoming"
	MessageDirectionOutgoing MessageDirection = "outgoing"
	MessageDirectionSystem   MessageDirection = "system"
)

type MessageProvenance string

const (
	MessageProvenanceLive         MessageProvenance = "live"
	MessageProvenanceHistorySync  MessageProvenance = "history_sync"
	MessageProvenanceWriteThrough MessageProvenance = "write_through"
)

type Chat struct {
	InstanceID        string          `json:"instanceId" gorm:"column:instance_id;type:uuid;primaryKey"`
	ChatID            string          `json:"chatId" gorm:"column:chat_id;size:255;primaryKey"`
	ContactID         *string         `json:"contactId,omitempty" gorm:"column:contact_id;type:uuid"`
	Type              ChatType        `json:"type" gorm:"column:chat_type;size:32;not null"`
	DisplayName       *string         `json:"displayName,omitempty" gorm:"column:display_name"`
	LastMessageID     *string         `json:"lastMessageId,omitempty" gorm:"column:last_message_id;size:255"`
	LastMessageAt     *time.Time      `json:"lastMessageAt,omitempty" gorm:"column:last_message_at"`
	LastActivityAt    *time.Time      `json:"lastActivityAt,omitempty" gorm:"column:last_activity_at"`
	UnreadCount       int             `json:"unreadCount" gorm:"column:unread_count;not null"`
	Archived          *bool           `json:"archived,omitempty" gorm:"column:archived"`
	Pinned            *bool           `json:"pinned,omitempty" gorm:"column:pinned"`
	MutedUntil        *time.Time      `json:"mutedUntil,omitempty" gorm:"column:muted_until"`
	DisappearingTimer *uint32         `json:"disappearingTimer,omitempty" gorm:"column:disappearing_timer"`
	SourceOccurredAt  time.Time       `json:"sourceOccurredAt" gorm:"column:source_occurred_at;not null"`
	SourceEventKey    string          `json:"-" gorm:"column:source_event_key;size:255;not null"`
	FieldVersions     json.RawMessage `json:"-" gorm:"column:field_versions;type:jsonb;not null"`
	LastSyncedAt      time.Time       `json:"lastSyncedAt" gorm:"column:last_synced_at;not null"`
	TombstonedAt      *time.Time      `json:"tombstonedAt,omitempty" gorm:"column:tombstoned_at"`
	CreatedAt         time.Time       `json:"createdAt"`
	UpdatedAt         time.Time       `json:"updatedAt"`
}

func (Chat) TableName() string { return "projected_chats" }

type ProjectedMessage struct {
	InstanceID         string            `json:"instanceId" gorm:"column:instance_id;type:uuid;primaryKey"`
	MessageID          string            `json:"messageId" gorm:"column:message_id;size:255;primaryKey"`
	ChatID             string            `json:"chatId" gorm:"column:chat_id;size:255;not null"`
	SenderJID          *string           `json:"senderJid,omitempty" gorm:"column:sender_jid;size:255"`
	RecipientJID       *string           `json:"recipientJid,omitempty" gorm:"column:recipient_jid;size:255"`
	ParticipantJID     *string           `json:"participantJid,omitempty" gorm:"column:participant_jid;size:255"`
	Direction          MessageDirection  `json:"direction" gorm:"column:direction;size:32;not null"`
	MessageType        string            `json:"messageType" gorm:"column:message_type;size:64;not null"`
	ContentText        *string           `json:"contentText,omitempty" gorm:"column:content_text"`
	Caption            *string           `json:"caption,omitempty" gorm:"column:caption"`
	ContentSummary     *string           `json:"contentSummary,omitempty" gorm:"column:content_summary"`
	QuotedMessageID    *string           `json:"quotedMessageId,omitempty" gorm:"column:quoted_message_id;size:255"`
	MediaType          *string           `json:"mediaType,omitempty" gorm:"column:media_type;size:64"`
	MediaMIMEType      *string           `json:"mediaMimeType,omitempty" gorm:"column:media_mime_type;size:255"`
	MediaFileName      *string           `json:"mediaFileName,omitempty" gorm:"column:media_file_name"`
	MediaSize          *int64            `json:"mediaSize,omitempty" gorm:"column:media_size"`
	MediaDuration      *uint32           `json:"mediaDurationSeconds,omitempty" gorm:"column:media_duration_seconds"`
	MediaWidth         *uint32           `json:"mediaWidth,omitempty" gorm:"column:media_width"`
	MediaHeight        *uint32           `json:"mediaHeight,omitempty" gorm:"column:media_height"`
	MediaObjectKey     *string           `json:"mediaObjectKey,omitempty" gorm:"column:media_object_key"`
	Status             *string           `json:"status,omitempty" gorm:"column:status;size:32"`
	ProviderTimestamp  time.Time         `json:"providerTimestamp" gorm:"column:provider_timestamp;not null"`
	SentAt             *time.Time        `json:"sentAt,omitempty" gorm:"column:sent_at"`
	DeliveredAt        *time.Time        `json:"deliveredAt,omitempty" gorm:"column:delivered_at"`
	ReadAt             *time.Time        `json:"readAt,omitempty" gorm:"column:read_at"`
	PlayedAt           *time.Time        `json:"playedAt,omitempty" gorm:"column:played_at"`
	Provenance         MessageProvenance `json:"provenance" gorm:"column:provenance;size:32;not null"`
	HistorySyncID      *string           `json:"historySyncId,omitempty" gorm:"column:history_sync_id;size:255"`
	RetentionExpiresAt *time.Time        `json:"retentionExpiresAt,omitempty" gorm:"column:retention_expires_at"`
	DeletedAt          *time.Time        `json:"deletedAt,omitempty" gorm:"column:deleted_at"`
	SourceOccurredAt   time.Time         `json:"sourceOccurredAt" gorm:"column:source_occurred_at;not null"`
	SourceEventKey     string            `json:"-" gorm:"column:source_event_key;size:255;not null"`
	FieldVersions      json.RawMessage   `json:"-" gorm:"column:field_versions;type:jsonb;not null"`
	LastSyncedAt       time.Time         `json:"lastSyncedAt" gorm:"column:last_synced_at;not null"`
	CreatedAt          time.Time         `json:"createdAt"`
	UpdatedAt          time.Time         `json:"updatedAt"`
}

func (ProjectedMessage) TableName() string { return "projected_messages" }

type MessageReceipt struct {
	InstanceID       string    `json:"instanceId" gorm:"column:instance_id;type:uuid;primaryKey"`
	MessageID        string    `json:"messageId" gorm:"column:message_id;size:255;primaryKey"`
	RecipientJID     string    `json:"recipientJid" gorm:"column:recipient_jid;size:255;primaryKey"`
	ReceiptType      string    `json:"receiptType" gorm:"column:receipt_type;size:32;primaryKey"`
	ReceiptAt        time.Time `json:"receiptAt" gorm:"column:receipt_at;not null"`
	SourceOccurredAt time.Time `json:"sourceOccurredAt" gorm:"column:source_occurred_at;not null"`
	SourceEventKey   string    `json:"-" gorm:"column:source_event_key;size:255;not null"`
	LastSyncedAt     time.Time `json:"lastSyncedAt" gorm:"column:last_synced_at;not null"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

func (MessageReceipt) TableName() string { return "projected_message_receipts" }
