package projection_model

import "time"

type ParticipantRole string

const (
	ParticipantRoleMember     ParticipantRole = "member"
	ParticipantRoleAdmin      ParticipantRole = "admin"
	ParticipantRoleSuperAdmin ParticipantRole = "super_admin"
)

type Group struct {
	InstanceID           string     `json:"instanceId" gorm:"column:instance_id;type:uuid;primaryKey"`
	GroupID              string     `json:"groupId" gorm:"column:group_id;size:255;primaryKey"`
	Name                 *string    `json:"name,omitempty" gorm:"column:name"`
	Topic                *string    `json:"topic,omitempty" gorm:"column:topic"`
	OwnerJID             *string    `json:"ownerJid,omitempty" gorm:"column:owner_jid;size:255"`
	OwnerPhoneJID        *string    `json:"ownerPhoneJid,omitempty" gorm:"column:owner_phone_jid;size:255"`
	Locked               *bool      `json:"locked,omitempty" gorm:"column:locked"`
	Announce             *bool      `json:"announce,omitempty" gorm:"column:announce"`
	EphemeralEnabled     *bool      `json:"ephemeralEnabled,omitempty" gorm:"column:ephemeral_enabled"`
	EphemeralTimer       *int64     `json:"ephemeralTimer,omitempty" gorm:"column:ephemeral_timer"`
	JoinApprovalRequired *bool      `json:"joinApprovalRequired,omitempty" gorm:"column:join_approval_required"`
	Suspended            *bool      `json:"suspended,omitempty" gorm:"column:suspended"`
	ParticipantVersion   *string    `json:"participantVersion,omitempty" gorm:"column:participant_version;size:255"`
	AddressingMode       *string    `json:"addressingMode,omitempty" gorm:"column:addressing_mode;size:32"`
	MemberAddMode        *string    `json:"memberAddMode,omitempty" gorm:"column:member_add_mode;size:32"`
	ParentGroupID        *string    `json:"parentGroupId,omitempty" gorm:"column:parent_group_id;size:255"`
	IsParent             *bool      `json:"isParent,omitempty" gorm:"column:is_parent"`
	IsDefaultSubgroup    *bool      `json:"isDefaultSubgroup,omitempty" gorm:"column:is_default_subgroup"`
	InviteLink           *string    `json:"inviteLink,omitempty" gorm:"column:invite_link"`
	InviteLinkUpdatedAt  *time.Time `json:"inviteLinkUpdatedAt,omitempty" gorm:"column:invite_link_updated_at"`
	ProviderCreatedAt    *time.Time `json:"providerCreatedAt,omitempty" gorm:"column:provider_created_at"`
	SourceOccurredAt     time.Time  `json:"sourceOccurredAt" gorm:"column:source_occurred_at;not null"`
	SourceEventKey       string     `json:"-" gorm:"column:source_event_key;size:255;not null"`
	LastSyncedAt         time.Time  `json:"lastSyncedAt" gorm:"column:last_synced_at;not null"`
	TombstonedAt         *time.Time `json:"tombstonedAt,omitempty" gorm:"column:tombstoned_at"`
	CreatedAt            time.Time  `json:"createdAt"`
	UpdatedAt            time.Time  `json:"updatedAt"`
}

func (Group) TableName() string { return "projected_groups" }

type GroupParticipant struct {
	InstanceID       string          `json:"instanceId" gorm:"column:instance_id;type:uuid;primaryKey"`
	GroupID          string          `json:"groupId" gorm:"column:group_id;size:255;primaryKey"`
	ParticipantID    string          `json:"participantId" gorm:"column:participant_id;size:255;primaryKey"`
	PhoneNumberJID   *string         `json:"phoneNumberJid,omitempty" gorm:"column:phone_number_jid;size:255"`
	LID              *string         `json:"lid,omitempty" gorm:"column:lid;size:255"`
	DisplayName      *string         `json:"displayName,omitempty" gorm:"column:display_name"`
	Role             ParticipantRole `json:"role" gorm:"column:role;size:32;not null"`
	SourceOccurredAt time.Time       `json:"sourceOccurredAt" gorm:"column:source_occurred_at;not null"`
	SourceEventKey   string          `json:"-" gorm:"column:source_event_key;size:255;not null"`
	LastSyncedAt     time.Time       `json:"lastSyncedAt" gorm:"column:last_synced_at;not null"`
	TombstonedAt     *time.Time      `json:"tombstonedAt,omitempty" gorm:"column:tombstoned_at"`
	CreatedAt        time.Time       `json:"createdAt"`
	UpdatedAt        time.Time       `json:"updatedAt"`
}

func (GroupParticipant) TableName() string { return "projected_group_participants" }
