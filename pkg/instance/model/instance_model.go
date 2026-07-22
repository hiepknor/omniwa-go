package instance_model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Instance struct {
	Id               string    `json:"-" gorm:"type:uuid;primaryKey"`
	Name             string    `json:"-"`
	Token            string    `json:"-" gorm:"unique"`
	TokenDigest      *string   `json:"-" gorm:"column:token_digest;-:migration"`
	TokenKeyVersion  *int      `json:"-" gorm:"column:token_key_version;-:migration"`
	Webhook          string    `json:"-"`
	RabbitmqEnable   string    `json:"-"`
	WebSocketEnable  string    `json:"-"`
	NatsEnable       string    `json:"-"`
	Jid              string    `json:"-" gorm:"column:jid"`
	Qrcode           string    `json:"-" gorm:"type:text"`
	Connected        bool      `json:"-"`
	Expiration       int64     `json:"-"`
	DisconnectReason string    `json:"-"`
	Events           string    `json:"-"`
	OsName           string    `json:"-"`
	Proxy            string    `json:"-"`
	ClientName       string    `json:"-"`
	CreatedAt        time.Time `json:"-" gorm:"autoCreateTime"`

	// Advanced Settings
	AlwaysOnline  bool   `json:"-" gorm:"default:false"`
	RejectCall    bool   `json:"-" gorm:"default:false"`
	MsgRejectCall string `json:"-" gorm:"default:''"`
	ReadMessages  bool   `json:"-" gorm:"default:false"`
	IgnoreGroups  bool   `json:"-" gorm:"default:false"`
	IgnoreStatus  bool   `json:"-" gorm:"default:false"`
}

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
