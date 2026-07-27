package group_list_model

import (
	"encoding/json"
	"time"
)

type GroupList struct {
	ID                         string     `json:"id" gorm:"column:id;type:uuid;primaryKey"`
	InstanceID                 string     `json:"instanceId" gorm:"column:instance_id;type:uuid;not null"`
	Name                       string     `json:"name" gorm:"column:name;size:255;not null"`
	NormalizedName             string     `json:"-" gorm:"column:normalized_name;size:255;not null"`
	Description                *string    `json:"description" gorm:"column:description"`
	Version                    int64      `json:"version" gorm:"column:version;not null"`
	AuthorizationSource        string     `json:"authorizationSource" gorm:"column:authorization_source;size:64;not null"`
	AuthorizationReferenceHash string     `json:"-" gorm:"column:authorization_reference_hash;size:64;not null"`
	AuthorizedAt               time.Time  `json:"authorizedAt" gorm:"column:authorized_at;not null"`
	CreatedAt                  time.Time  `json:"createdAt" gorm:"column:created_at;not null"`
	UpdatedAt                  time.Time  `json:"updatedAt" gorm:"column:updated_at;not null"`
	DeletedAt                  *time.Time `json:"-" gorm:"column:deleted_at"`
}

func (GroupList) TableName() string { return "group_lists" }

type Entry struct {
	GroupListID       string    `json:"-" gorm:"column:group_list_id;type:uuid;primaryKey"`
	InstanceID        string    `json:"-" gorm:"column:instance_id;type:uuid;not null"`
	GroupJID          string    `json:"groupJid" gorm:"column:group_jid;size:255;primaryKey"`
	GroupNameSnapshot string    `json:"snapshotName" gorm:"column:group_name_snapshot;size:255;not null"`
	CreatedAt         time.Time `json:"createdAt" gorm:"column:created_at;not null"`
}

func (Entry) TableName() string { return "group_list_entries" }

type AuditEvent struct {
	ID                 string          `json:"id" gorm:"column:id;type:uuid;primaryKey"`
	GroupListID        string          `json:"groupListId" gorm:"column:group_list_id;type:uuid;not null"`
	InstanceID         string          `json:"-" gorm:"column:instance_id;type:uuid;not null"`
	EventType          string          `json:"eventType" gorm:"column:event_type;size:32;not null"`
	ActorType          string          `json:"actorType" gorm:"column:actor_type;size:32;not null"`
	ActorReferenceHash string          `json:"-" gorm:"column:actor_reference_hash;size:64;not null"`
	FromVersion        *int64          `json:"fromVersion,omitempty" gorm:"column:from_version"`
	ToVersion          int64           `json:"toVersion" gorm:"column:to_version;not null"`
	Metadata           json.RawMessage `json:"metadata" gorm:"column:metadata;type:jsonb;not null"`
	OccurredAt         time.Time       `json:"occurredAt" gorm:"column:occurred_at;not null"`
}

func (AuditEvent) TableName() string { return "group_list_audit_events" }
