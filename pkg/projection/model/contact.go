package projection_model

import (
	"encoding/json"
	"time"
)

type ContactIdentityKind string

const (
	ContactIdentityKindJID      ContactIdentityKind = "jid"
	ContactIdentityKindPhoneJID ContactIdentityKind = "phone_jid"
	ContactIdentityKindLID      ContactIdentityKind = "lid"
	ContactIdentityKindUsername ContactIdentityKind = "username"
)

// Contact is the instance-scoped, provider-normalized contact read model.
// ContactID is an internal stable identity; provider identifiers are aliases
// stored in ContactIdentity and may change or become linked over time.
type Contact struct {
	InstanceID               string          `json:"instanceId" gorm:"column:instance_id;type:uuid;primaryKey"`
	ContactID                string          `json:"contactId" gorm:"column:contact_id;type:uuid;primaryKey"`
	PreferredJID             string          `json:"jid" gorm:"column:preferred_jid;size:255;not null"`
	PhoneJID                 *string         `json:"phoneJid,omitempty" gorm:"column:phone_jid;size:255"`
	LID                      *string         `json:"lid,omitempty" gorm:"column:lid;size:255"`
	Username                 *string         `json:"username,omitempty" gorm:"column:username;size:255"`
	Found                    bool            `json:"found" gorm:"column:found;not null"`
	FirstName                *string         `json:"firstName,omitempty" gorm:"column:first_name"`
	FullName                 *string         `json:"fullName,omitempty" gorm:"column:full_name"`
	PushName                 *string         `json:"pushName,omitempty" gorm:"column:push_name"`
	BusinessName             *string         `json:"businessName,omitempty" gorm:"column:business_name"`
	RedactedPhone            *string         `json:"redactedPhone,omitempty" gorm:"column:redacted_phone"`
	SaveOnPrimaryAddressbook *bool           `json:"saveOnPrimaryAddressbook,omitempty" gorm:"column:save_on_primary_addressbook"`
	PictureID                *string         `json:"pictureId,omitempty" gorm:"column:picture_id;size:255"`
	PictureAuthorJID         *string         `json:"pictureAuthorJid,omitempty" gorm:"column:picture_author_jid;size:255"`
	PictureRemoved           *bool           `json:"pictureRemoved,omitempty" gorm:"column:picture_removed"`
	PictureUpdatedAt         *time.Time      `json:"pictureUpdatedAt,omitempty" gorm:"column:picture_updated_at"`
	About                    *string         `json:"about,omitempty" gorm:"column:about"`
	AboutUpdatedAt           *time.Time      `json:"aboutUpdatedAt,omitempty" gorm:"column:about_updated_at"`
	SourceOccurredAt         time.Time       `json:"sourceOccurredAt" gorm:"column:source_occurred_at;not null"`
	SourceEventKey           string          `json:"-" gorm:"column:source_event_key;size:255;not null"`
	FieldVersions            json.RawMessage `json:"-" gorm:"column:field_versions;type:jsonb;not null"`
	LastSyncedAt             time.Time       `json:"lastSyncedAt" gorm:"column:last_synced_at;not null"`
	TombstonedAt             *time.Time      `json:"tombstonedAt,omitempty" gorm:"column:tombstoned_at"`
	CreatedAt                time.Time       `json:"createdAt"`
	UpdatedAt                time.Time       `json:"updatedAt"`
}

func (Contact) TableName() string { return "projected_contacts" }

type ContactIdentity struct {
	InstanceID       string              `json:"instanceId" gorm:"column:instance_id;type:uuid;primaryKey"`
	Kind             ContactIdentityKind `json:"kind" gorm:"column:identity_kind;size:32;primaryKey"`
	Value            string              `json:"value" gorm:"column:identity_value;size:255;primaryKey"`
	ContactID        string              `json:"contactId" gorm:"column:contact_id;type:uuid;not null"`
	SourceOccurredAt time.Time           `json:"sourceOccurredAt" gorm:"column:source_occurred_at;not null"`
	SourceEventKey   string              `json:"-" gorm:"column:source_event_key;size:255;not null"`
	LastSyncedAt     time.Time           `json:"lastSyncedAt" gorm:"column:last_synced_at;not null"`
	TombstonedAt     *time.Time          `json:"tombstonedAt,omitempty" gorm:"column:tombstoned_at"`
	CreatedAt        time.Time           `json:"createdAt"`
	UpdatedAt        time.Time           `json:"updatedAt"`
}

func (ContactIdentity) TableName() string { return "projected_contact_identities" }
