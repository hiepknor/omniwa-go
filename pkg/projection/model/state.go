package projection_model

import "time"

type SyncStatus string

const (
	SyncStatusNotStarted SyncStatus = "not_started"
	SyncStatusSyncing    SyncStatus = "syncing"
	SyncStatusReady      SyncStatus = "ready"
	SyncStatusStale      SyncStatus = "stale"
	SyncStatusFailed     SyncStatus = "failed"
)

type State struct {
	InstanceID       string     `json:"instanceId" gorm:"column:instance_id;type:uuid;primaryKey"`
	Resource         string     `json:"resource" gorm:"column:resource;size:64;primaryKey"`
	SyncStatus       SyncStatus `json:"syncStatus" gorm:"column:sync_status;size:32;not null"`
	LastEventAt      *time.Time `json:"lastEventAt,omitempty" gorm:"column:last_event_at"`
	LastReconciledAt *time.Time `json:"lastReconciledAt,omitempty" gorm:"column:last_reconciled_at"`
	StaleSince       *time.Time `json:"staleSince,omitempty" gorm:"column:stale_since"`
	SchemaVersion    int64      `json:"schemaVersion" gorm:"column:schema_version;not null"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
}

func (State) TableName() string { return "projection_states" }
