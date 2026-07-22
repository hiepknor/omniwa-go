package instance_handler

import (
	"time"

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
)

// InstanceView is the compatibility response for instance create/list/info.
// Token remains temporary until the credential expand-migrate-contract rollout
// completes. Proxy credentials and QR ceremony material are never exposed.
type InstanceView struct {
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	Token             string    `json:"token"`
	CredentialVersion int64     `json:"credentialVersion"`
	Webhook           string    `json:"webhook"`
	RabbitMQEnable    string    `json:"rabbitmqEnable"`
	WebSocketEnable   string    `json:"websocketEnable"`
	NATSEnable        string    `json:"natsEnable"`
	JID               string    `json:"jid"`
	QRCode            string    `json:"qrcode"`
	Connected         bool      `json:"connected"`
	Expiration        int64     `json:"expiration"`
	DisconnectReason  string    `json:"disconnect_reason"`
	Events            string    `json:"events"`
	OSName            string    `json:"os_name"`
	Proxy             string    `json:"proxy"`
	ClientName        string    `json:"client_name"`
	CreatedAt         time.Time `json:"createdAt"`
	AlwaysOnline      bool      `json:"alwaysOnline"`
	RejectCall        bool      `json:"rejectCall"`
	MsgRejectCall     string    `json:"msgRejectCall"`
	ReadMessages      bool      `json:"readMessages"`
	IgnoreGroups      bool      `json:"ignoreGroups"`
	IgnoreStatus      bool      `json:"ignoreStatus"`
}

func instanceView(instance *instance_model.Instance) InstanceView {
	if instance == nil {
		return InstanceView{}
	}
	return InstanceView{
		ID: instance.Id, Name: instance.Name, Token: instance.Token, CredentialVersion: instance.TokenGeneration, Webhook: instance.Webhook,
		RabbitMQEnable: instance.RabbitmqEnable, WebSocketEnable: instance.WebSocketEnable, NATSEnable: instance.NatsEnable,
		JID: instance.Jid, QRCode: "", Connected: instance.Connected, Expiration: instance.Expiration,
		DisconnectReason: instance.DisconnectReason, Events: instance.Events, OSName: instance.OsName,
		Proxy: "", ClientName: instance.ClientName, CreatedAt: instance.CreatedAt,
		AlwaysOnline: instance.AlwaysOnline, RejectCall: instance.RejectCall, MsgRejectCall: instance.MsgRejectCall,
		ReadMessages: instance.ReadMessages, IgnoreGroups: instance.IgnoreGroups, IgnoreStatus: instance.IgnoreStatus,
	}
}

func instanceViewList(instances []*instance_model.Instance) []InstanceView {
	result := make([]InstanceView, len(instances))
	for index := range instances {
		result[index] = instanceView(instances[index])
	}
	return result
}
