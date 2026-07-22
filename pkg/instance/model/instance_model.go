package instance_model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Instance struct {
	Id               string     `json:"-" gorm:"type:uuid;primaryKey"`
	Name             string     `json:"-"`
	Token            string     `json:"-" gorm:"unique"`
	TokenDigest      *string    `json:"-" gorm:"column:token_digest;-:migration"`
	TokenKeyVersion  *int       `json:"-" gorm:"column:token_key_version;-:migration"`
	TokenGeneration  int64      `json:"-" gorm:"column:token_generation;default:1;-:migration"`
	TokenRotatedAt   *time.Time `json:"-" gorm:"column:token_rotated_at;-:migration"`
	Webhook          string     `json:"-"`
	RabbitmqEnable   string     `json:"-"`
	WebSocketEnable  string     `json:"-"`
	NatsEnable       string     `json:"-"`
	Jid              string     `json:"-" gorm:"column:jid"`
	Qrcode           string     `json:"-" gorm:"type:text"`
	Connected        bool       `json:"-"`
	Expiration       int64      `json:"-"`
	DisconnectReason string     `json:"-"`
	Events           string     `json:"-"`
	OsName           string     `json:"-"`
	Proxy            string     `json:"-"`
	ClientName       string     `json:"-"`
	CreatedAt        time.Time  `json:"-" gorm:"autoCreateTime"`

	// Advanced Settings
	AlwaysOnline  bool   `json:"-" gorm:"default:false"`
	RejectCall    bool   `json:"-" gorm:"default:false"`
	MsgRejectCall string `json:"-" gorm:"default:''"`
	ReadMessages  bool   `json:"-" gorm:"default:false"`
	IgnoreGroups  bool   `json:"-" gorm:"default:false"`
	IgnoreStatus  bool   `json:"-" gorm:"default:false"`
}

type TokenRotationAudit struct {
	ID                 string    `json:"-" gorm:"column:id;type:uuid;primaryKey"`
	InstanceID         string    `json:"-" gorm:"column:instance_id;type:uuid;not null"`
	PreviousGeneration int64     `json:"-" gorm:"column:previous_generation;not null"`
	NewGeneration      int64     `json:"-" gorm:"column:new_generation;not null"`
	Reason             string    `json:"-" gorm:"column:reason;size:500;not null"`
	ActorReferenceHash string    `json:"-" gorm:"column:actor_reference_hash;size:64;not null"`
	RequestID          string    `json:"-" gorm:"column:request_id;size:64;not null"`
	OccurredAt         time.Time `json:"-" gorm:"column:occurred_at;not null"`
}

func (TokenRotationAudit) TableName() string { return "instance_token_rotation_audit" }

// AdvancedSettings representa as configurações avançadas de uma instância
type AdvancedSettings struct {
	AlwaysOnline  bool   `json:"alwaysOnline"`
	RejectCall    bool   `json:"rejectCall"`
	MsgRejectCall string `json:"msgRejectCall"`
	ReadMessages  bool   `json:"readMessages"`
	IgnoreGroups  bool   `json:"ignoreGroups"`
	IgnoreStatus  bool   `json:"ignoreStatus"`
}

func (m *Instance) BeforeCreate(tx *gorm.DB) (err error) {
	if m.Id == "" {
		m.Id = uuid.New().String()
	}
	return
}
