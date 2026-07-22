package projection_model

import "time"

type Label struct {
	InstanceID       string     `json:"instanceId" gorm:"column:instance_id;type:uuid;primaryKey"`
	LabelID          string     `json:"labelId" gorm:"column:label_id;size:255;primaryKey"`
	Name             *string    `json:"name,omitempty" gorm:"column:name"`
	Color            *int32     `json:"color,omitempty" gorm:"column:color"`
	PredefinedID     *int32     `json:"predefinedId,omitempty" gorm:"column:predefined_id"`
	OrderIndex       *int32     `json:"orderIndex,omitempty" gorm:"column:order_index"`
	Active           *bool      `json:"active,omitempty" gorm:"column:active"`
	Immutable        *bool      `json:"immutable,omitempty" gorm:"column:immutable"`
	Kind             *string    `json:"kind,omitempty" gorm:"column:kind;size:64"`
	SourceOccurredAt time.Time  `json:"sourceOccurredAt" gorm:"column:source_occurred_at;not null"`
	SourceEventKey   string     `json:"-" gorm:"column:source_event_key;size:255;not null"`
	LastSyncedAt     time.Time  `json:"lastSyncedAt" gorm:"column:last_synced_at;not null"`
	TombstonedAt     *time.Time `json:"tombstonedAt,omitempty" gorm:"column:tombstoned_at"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
}

func (Label) TableName() string { return "projected_labels" }

type LabelChatAssociation struct {
	InstanceID       string     `json:"instanceId" gorm:"column:instance_id;type:uuid;primaryKey"`
	LabelID          string     `json:"labelId" gorm:"column:label_id;size:255;primaryKey"`
	ChatID           string     `json:"chatId" gorm:"column:chat_id;size:255;primaryKey"`
	SourceOccurredAt time.Time  `json:"sourceOccurredAt" gorm:"column:source_occurred_at;not null"`
	SourceEventKey   string     `json:"-" gorm:"column:source_event_key;size:255;not null"`
	LastSyncedAt     time.Time  `json:"lastSyncedAt" gorm:"column:last_synced_at;not null"`
	TombstonedAt     *time.Time `json:"tombstonedAt,omitempty" gorm:"column:tombstoned_at"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
}

func (LabelChatAssociation) TableName() string { return "projected_label_chat_associations" }

type LabelMessageAssociation struct {
	InstanceID       string     `json:"instanceId" gorm:"column:instance_id;type:uuid;primaryKey"`
	LabelID          string     `json:"labelId" gorm:"column:label_id;size:255;primaryKey"`
	ChatID           string     `json:"chatId" gorm:"column:chat_id;size:255;primaryKey"`
	MessageID        string     `json:"messageId" gorm:"column:message_id;size:255;primaryKey"`
	SourceOccurredAt time.Time  `json:"sourceOccurredAt" gorm:"column:source_occurred_at;not null"`
	SourceEventKey   string     `json:"-" gorm:"column:source_event_key;size:255;not null"`
	LastSyncedAt     time.Time  `json:"lastSyncedAt" gorm:"column:last_synced_at;not null"`
	TombstonedAt     *time.Time `json:"tombstonedAt,omitempty" gorm:"column:tombstoned_at"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
}

func (LabelMessageAssociation) TableName() string {
	return "projected_label_message_associations"
}
