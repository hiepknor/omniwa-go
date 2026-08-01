package projection_model

import "time"

type PhoneIdentityEvidenceKind string

const (
	PhoneIdentityEvidenceDirectPhone PhoneIdentityEvidenceKind = "direct_phone"
	PhoneIdentityEvidencePairedAlt   PhoneIdentityEvidenceKind = "paired_alt"
)

// PhoneIdentityEvidence records only identities directly observed by one
// instance. It must not be populated from the global whatsmeow LID map.
type PhoneIdentityEvidence struct {
	InstanceID      string                    `json:"-" gorm:"column:instance_id;type:uuid;primaryKey"`
	PhoneJID        string                    `json:"-" gorm:"column:phone_jid;size:255;primaryKey"`
	LIDJID          *string                   `json:"-" gorm:"column:lid_jid;size:255"`
	EvidenceKind    PhoneIdentityEvidenceKind `json:"-" gorm:"column:evidence_kind;size:32;not null"`
	FirstObservedAt time.Time                 `json:"-" gorm:"column:first_observed_at;not null"`
	LastObservedAt  time.Time                 `json:"-" gorm:"column:last_observed_at;not null"`
}

func (PhoneIdentityEvidence) TableName() string { return "projection_phone_identity_evidence" }
