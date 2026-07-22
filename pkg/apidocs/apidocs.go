// Package apidocs holds documentation-only response models used by the Swagger
// annotations (swaggo). Handlers return `gin.H` maps at runtime; these structs
// simply give the generated OpenAPI spec a concrete, typed schema for the
// success/error envelopes so a WebUI client can see the real payload shape.
//
// This package is fork-specific (it does not exist upstream), so adding or
// changing it never conflicts when syncing from evolution-go. Nothing imports
// it at runtime — swaggo resolves the types by name from the annotations.
package apidocs

import (
	"time"

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
)

// SuccessResponse is the standard success envelope returned by most endpoints:
//
//	{ "message": "success", "data": { ... } }
//
// `data` is present only on endpoints that return a payload; action endpoints
// (disconnect, logout, delete, reconnect...) return just `message`.
type SuccessResponse struct {
	Message string          `json:"message" example:"success"`
	Data    interface{}     `json:"data,omitempty"`
	Meta    *ProjectionMeta `json:"meta,omitempty"`
}

type ProjectionMeta struct {
	Source       string     `json:"source" example:"projection"`
	SyncStatus   string     `json:"syncStatus" example:"ready"`
	LastSyncedAt *time.Time `json:"lastSyncedAt,omitempty"`
	NextCursor   string     `json:"nextCursor,omitempty"`
}

// ErrorResponse is the standard error envelope returned on 4xx/5xx:
//
//	{ "error": "<human readable reason>" }
type ErrorResponse struct {
	Error string `json:"error" example:"phone number is required"`
}

type CapabilitiesData struct {
	Version      string   `json:"version" example:"1.2.3"`
	Capabilities []string `json:"capabilities" example:"rate_limit_retry_after,groups_projection"`
}

type CapabilitiesResponse struct {
	Message string           `json:"message" example:"success"`
	Data    CapabilitiesData `json:"data"`
}

// RateLimitResponse is returned when an information query cannot safely reach
// WhatsApp. Code and RetryAfter are additive fields; Error remains a string for
// compatibility with existing clients.
type RateLimitResponse struct {
	Error      string `json:"error" example:"rate_limited"`
	Code       string `json:"code,omitempty" example:"rate_limited"`
	RetryAfter int    `json:"retryAfter,omitempty" example:"90"`
}

// OutboundRateLimitResponse is returned when a message mutation exceeds the
// operator-configured outbound safety limit.
type OutboundRateLimitResponse struct {
	Error      string `json:"error" example:"outbound_rate_limited"`
	Code       string `json:"code,omitempty" example:"outbound_rate_limited"`
	RetryAfter int    `json:"retryAfter,omitempty" example:"5"`
}

// CreateInstanceResponse is returned by POST /instance/create. `data` is the
// full instance record, including the per-instance `token` that becomes the
// `apikey` header for every per-instance route.
type CreateInstanceResponse struct {
	Message string                  `json:"message" example:"success"`
	Data    instance_model.Instance `json:"data"`
}

// InstanceResponse is returned by endpoints that echo a single instance
// (e.g. GET /instance/info/{instanceId}).
type InstanceResponse struct {
	Message string                  `json:"message" example:"success"`
	Data    instance_model.Instance `json:"data"`
}

// InstanceListResponse is returned by GET /instance/all.
type InstanceListResponse struct {
	Message string                    `json:"message" example:"success"`
	Data    []instance_model.Instance `json:"data"`
}

// ConnectData is the payload of POST /instance/connect.
type ConnectData struct {
	Jid         string `json:"jid" example:"5511999999999:12@s.whatsapp.net"`
	WebhookURL  string `json:"webhookUrl" example:"https://example.com/webhook"`
	EventString string `json:"eventString" example:"MESSAGE,CONNECTION,QRCODE"`
}

// ConnectResponse is returned by POST /instance/connect.
type ConnectResponse struct {
	Message string      `json:"message" example:"success"`
	Data    ConnectData `json:"data"`
}

// QRCodeData mirrors the QR/pairing payload used while linking a device. When
// the account requires a WebAuthn passkey to finish linking there is no QR to
// scan and the Passkey* fields drive the UI instead.
type QRCodeData struct {
	Qrcode         string `json:"qrcode" example:"data:image/png;base64,iVBORw0KGgo..."`
	Code           string `json:"code" example:"2@abc123..."`
	PasskeyStage   string `json:"passkeyStage,omitempty"`
	PasskeyOpenURL string `json:"passkeyOpenUrl,omitempty"`
	PasskeyCode    string `json:"passkeyCode,omitempty"`
}

// QRCodeResponse is returned by GET /instance/qr.
type QRCodeResponse struct {
	Message string     `json:"message" example:"success"`
	Data    QRCodeData `json:"data"`
}

// StatusData is the payload of GET /instance/status.
type StatusData struct {
	Connected bool   `json:"Connected" example:"true"`
	LoggedIn  bool   `json:"LoggedIn" example:"true"`
	Name      string `json:"Name" example:"My WhatsApp"`
}

// StatusResponse is returned by GET /instance/status.
type StatusResponse struct {
	Message string     `json:"message" example:"success"`
	Data    StatusData `json:"data"`
}

// PairData is the payload of POST /instance/pair.
type PairData struct {
	PairingCode string `json:"PairingCode" example:"ABCD-EFGH"`
}

// PairResponse is returned by POST /instance/pair.
type PairResponse struct {
	Message string   `json:"message" example:"success"`
	Data    PairData `json:"data"`
}

// SendMessageData describes the stable, WebUI-relevant fields of a sent
// message. The runtime `data` also carries the raw whatsmeow message envelope;
// only the fields documented here are considered part of the public contract.
type SendMessageData struct {
	ID        string `json:"ID" example:"3EB0C767D26A8D4E2A1B"`
	Timestamp string `json:"Timestamp" example:"2026-07-21T10:30:00Z"`
}

// SendMessageResponse is returned by the POST /send/* endpoints.
type SendMessageResponse struct {
	Message string          `json:"message" example:"success"`
	Data    SendMessageData `json:"data"`
}

// TimestampData is the payload of action endpoints that only report when the
// action was applied (chat pin/mute/archive, message presence/markread/…).
type TimestampData struct {
	Timestamp string `json:"timestamp" example:"2026-07-21T10:30:00Z"`
}

// MessageIdData is the payload of message delete/edit — the affected message id
// and when the change took effect.
type MessageIdData struct {
	MessageId string `json:"messageId" example:"3EB0C767D26A8D4E2A1B"`
	Timestamp string `json:"timestamp" example:"2026-07-21T10:30:00Z"`
}

// DownloadMediaData is the payload of POST /message/downloadmedia — the media as
// a base64 data URL plus the timestamp.
type DownloadMediaData struct {
	Base64    string `json:"base64" example:"data:image/jpeg;base64,/9j/4AAQ..."`
	Timestamp string `json:"timestamp" example:"2026-07-21T10:30:00Z"`
}

// MessageStatusData is the payload of POST /message/status — the resolved
// message plus the timestamp. `result` mirrors the stored message object.
type MessageStatusData struct {
	Result    interface{} `json:"result"`
	Timestamp string      `json:"timestamp" example:"2026-07-21T10:30:00Z"`
}

// LabelItem mirrors a WhatsApp label. GET /label/list returns a bare JSON array
// of these (no envelope). For projection-backed reads, id is the stable label_id.
type LabelItem struct {
	Id           string `json:"id"`
	InstanceID   string `json:"instance_id"`
	LabelID      string `json:"label_id"`
	LabelName    string `json:"label_name"`
	LabelColor   string `json:"label_color"`
	PredefinedId string `json:"predefined_id"`
}

// PollVoteItem mirrors a single poll vote (model.PollVote).
type PollVoteItem struct {
	Id              string   `json:"id"`
	InstanceID      string   `json:"instanceId"`
	PollMessageID   string   `json:"pollMessageId"`
	PollChatJid     string   `json:"pollChatJid"`
	VoteMessageID   string   `json:"voteMessageId"`
	VoterJid        string   `json:"voterJid"`
	VoterPhone      string   `json:"voterPhone,omitempty"`
	VoterName       string   `json:"voterName,omitempty"`
	SelectedOptions []string `json:"selectedOptions"`
	VotedAt         string   `json:"votedAt" example:"2026-07-21T10:30:00Z"`
	ReceivedAt      string   `json:"receivedAt" example:"2026-07-21T10:30:00Z"`
}

// VoterItem mirrors aggregated voter info (model.VoterInfo).
type VoterItem struct {
	Jid             string   `json:"jid"`
	Phone           string   `json:"phone,omitempty"`
	Name            string   `json:"name,omitempty"`
	SelectedOptions []string `json:"selectedOptions"`
	VotedAt         string   `json:"votedAt" example:"2026-07-21T10:30:00Z"`
}

// PollResultsData mirrors model.PollResults. GET /polls/{pollMessageId}/results
// returns this object directly (no envelope).
type PollResultsData struct {
	PollMessageID string         `json:"pollMessageId"`
	PollChatJid   string         `json:"pollChatJid"`
	TotalVotes    int            `json:"totalVotes" example:"42"`
	Votes         []PollVoteItem `json:"votes"`
	OptionCounts  map[string]int `json:"optionCounts"`
	Voters        []VoterItem    `json:"voters"`
}

// LogEntry mirrors a logger LogEntry. GET /instance/logs/{instanceId} returns a
// bare JSON array of these (no envelope).
type LogEntry struct {
	Timestamp  string      `json:"timestamp" example:"2026-07-21T10:30:00Z"`
	Level      string      `json:"level" example:"INFO"`
	InstanceId string      `json:"instance_id"`
	Message    string      `json:"message"`
	Metadata   interface{} `json:"metadata,omitempty"`
}

// SetProxyData is the payload of POST /instance/proxy/{instanceId}.
type SetProxyData struct {
	Protocol string `json:"protocol" example:"http"`
	Host     string `json:"host" example:"proxy.example.com"`
	Port     string `json:"port" example:"8080"`
	HasAuth  bool   `json:"hasAuth" example:"true"`
}
