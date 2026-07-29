package projection_model

import (
	"encoding/json"
	"time"
)

type Conversation struct {
	InstanceID           string          `json:"-" gorm:"column:instance_id;type:uuid;primaryKey"`
	ConversationID       string          `json:"conversationId" gorm:"column:conversation_id;type:uuid;primaryKey"`
	ContactID            *string         `json:"contactId,omitempty" gorm:"column:contact_id;type:uuid"`
	Type                 ChatType        `json:"type" gorm:"column:conversation_type;size:32;not null"`
	AddressingJID        *string         `json:"addressingJid,omitempty" gorm:"column:addressing_jid;size:255"`
	DisplayName          *string         `json:"displayName,omitempty" gorm:"column:display_name"`
	DisplayNameSource    *string         `json:"displayNameSource,omitempty" gorm:"column:display_name_source;size:32"`
	DisplayNameUpdatedAt *time.Time      `json:"displayNameUpdatedAt,omitempty" gorm:"column:display_name_updated_at"`
	LastMessageID        *string         `json:"lastMessageId,omitempty" gorm:"column:last_message_id;size:255"`
	LastMessageAt        *time.Time      `json:"lastMessageAt,omitempty" gorm:"column:last_message_at"`
	LastActivityAt       *time.Time      `json:"lastActivityAt,omitempty" gorm:"column:last_activity_at"`
	UnreadCount          int             `json:"unreadCount" gorm:"column:unread_count;not null"`
	UnreadAuthoritative  bool            `json:"-" gorm:"column:unread_authoritative;not null"`
	Archived             *bool           `json:"archived,omitempty" gorm:"column:archived"`
	Pinned               *bool           `json:"pinned,omitempty" gorm:"column:pinned"`
	MutedUntil           *time.Time      `json:"mutedUntil,omitempty" gorm:"column:muted_until"`
	DisappearingTimer    *uint32         `json:"disappearingTimer,omitempty" gorm:"column:disappearing_timer"`
	FieldVersions        json.RawMessage `json:"-" gorm:"column:field_versions;type:jsonb;not null"`
	LastSyncedAt         time.Time       `json:"lastSyncedAt" gorm:"column:last_synced_at;not null"`
	TombstonedAt         *time.Time      `json:"tombstonedAt,omitempty" gorm:"column:tombstoned_at"`
	CreatedAt            time.Time       `json:"createdAt"`
	UpdatedAt            time.Time       `json:"updatedAt"`
}

func (Conversation) TableName() string { return "projected_conversations" }

type ChatAlias struct {
	InstanceID     string    `json:"-" gorm:"column:instance_id;type:uuid;primaryKey"`
	ChatID         string    `json:"chatId" gorm:"column:chat_id;size:255;primaryKey"`
	ConversationID string    `json:"conversationId" gorm:"column:conversation_id;type:uuid;not null"`
	AliasKind      string    `json:"aliasKind" gorm:"column:alias_kind;size:32;not null"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

func (ChatAlias) TableName() string { return "projected_chat_aliases" }

type ConversationRedirect struct {
	InstanceID              string    `json:"-" gorm:"column:instance_id;type:uuid;primaryKey"`
	AbsorbedConversationID  string    `json:"absorbedConversationId" gorm:"column:absorbed_conversation_id;type:uuid;primaryKey"`
	CanonicalConversationID string    `json:"canonicalConversationId" gorm:"column:canonical_conversation_id;type:uuid;not null"`
	CreatedAt               time.Time `json:"createdAt"`
	UpdatedAt               time.Time `json:"updatedAt"`
}

func (ConversationRedirect) TableName() string { return "projected_conversation_redirects" }

type ConversationBackfillStatus string

const (
	ConversationBackfillPending  ConversationBackfillStatus = "pending"
	ConversationBackfillRunning  ConversationBackfillStatus = "running"
	ConversationBackfillFailed   ConversationBackfillStatus = "failed"
	ConversationBackfillComplete ConversationBackfillStatus = "complete"
)

type ConversationBackfill struct {
	InstanceID      string                     `json:"instanceId" gorm:"column:instance_id;type:uuid;primaryKey"`
	Version         int                        `json:"version" gorm:"column:version;not null"`
	Status          ConversationBackfillStatus `json:"status" gorm:"column:status;size:16;not null"`
	CursorChatID    *string                    `json:"cursorChatId,omitempty" gorm:"column:cursor_chat_id;size:255"`
	LeaseOwner      *string                    `json:"-" gorm:"column:lease_owner;type:uuid"`
	LeaseExpiresAt  *time.Time                 `json:"-" gorm:"column:lease_expires_at"`
	ScannedCount    int64                      `json:"scannedCount" gorm:"column:scanned_count;not null"`
	AssociatedCount int64                      `json:"associatedCount" gorm:"column:associated_count;not null"`
	AbsorbedCount   int64                      `json:"absorbedCount" gorm:"column:absorbed_count;not null"`
	MessageCount    int64                      `json:"messageCount" gorm:"column:message_count;not null"`
	ConflictCount   int64                      `json:"conflictCount" gorm:"column:conflict_count;not null"`
	FailureCount    int64                      `json:"failureCount" gorm:"column:failure_count;not null"`
	LastErrorCode   *string                    `json:"lastErrorCode,omitempty" gorm:"column:last_error_code;size:64"`
	CompletedAt     *time.Time                 `json:"completedAt,omitempty" gorm:"column:completed_at"`
	CreatedAt       time.Time                  `json:"createdAt"`
	UpdatedAt       time.Time                  `json:"updatedAt"`
}

func (ConversationBackfill) TableName() string { return "projected_conversation_backfills" }
