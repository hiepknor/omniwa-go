package projection_service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	waE2E "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

const messageResource = "messages"

type messageEventPayload struct {
	ChatID               string                             `json:"chatId"`
	ChatType             projection_model.ChatType          `json:"chatType"`
	MessageID            string                             `json:"messageId,omitempty"`
	MessageIDs           []string                           `json:"messageIds,omitempty"`
	SenderJID            *string                            `json:"senderJid,omitempty"`
	RecipientJID         *string                            `json:"recipientJid,omitempty"`
	ParticipantJID       *string                            `json:"participantJid,omitempty"`
	Direction            projection_model.MessageDirection  `json:"direction"`
	MessageType          string                             `json:"messageType,omitempty"`
	ContentText          *string                            `json:"contentText,omitempty"`
	Caption              *string                            `json:"caption,omitempty"`
	ContentSummary       *string                            `json:"contentSummary,omitempty"`
	QuotedMessageID      *string                            `json:"quotedMessageId,omitempty"`
	MediaType            *string                            `json:"mediaType,omitempty"`
	MediaMIMEType        *string                            `json:"mediaMimeType,omitempty"`
	MediaFileName        *string                            `json:"mediaFileName,omitempty"`
	MediaSize            *int64                             `json:"mediaSize,omitempty"`
	MediaDurationSeconds *uint32                            `json:"mediaDurationSeconds,omitempty"`
	MediaWidth           *uint32                            `json:"mediaWidth,omitempty"`
	MediaHeight          *uint32                            `json:"mediaHeight,omitempty"`
	ProviderTimestamp    time.Time                          `json:"providerTimestamp,omitempty"`
	Provenance           projection_model.MessageProvenance `json:"provenance,omitempty"`
	Status               *string                            `json:"status,omitempty"`
	SentAt               *time.Time                         `json:"sentAt,omitempty"`
	ReceiptType          string                             `json:"receiptType,omitempty"`
	ReceiptAt            *time.Time                         `json:"receiptAt,omitempty"`
}

func NormalizeChatMessageEvent(instanceID string, rawEvent any) (*projection_model.Event, bool, error) {
	if instanceID == "" {
		return nil, true, errors.New("message projection event has no instance identity")
	}
	switch event := rawEvent.(type) {
	case *events.Message:
		return normalizeLiveMessageEvent(instanceID, event)
	case *events.Receipt:
		return normalizeReceiptEvent(instanceID, event)
	default:
		return nil, false, nil
	}
}

func normalizeLiveMessageEvent(instanceID string, event *events.Message) (*projection_model.Event, bool, error) {
	if event == nil || event.Info.ID == "" || len(event.Info.ID) > 255 || event.Info.Chat.IsEmpty() || event.Info.Timestamp.IsZero() || event.Message == nil {
		return nil, true, errors.New("message event is incomplete")
	}
	chatID := event.Info.Chat.ToNonAD().String()
	if len(chatID) > 255 {
		return nil, true, errors.New("message event chat identity exceeds storage limits")
	}
	payload := messageEventPayload{
		ChatID: chatID, ChatType: projectedChatType(event.Info.Chat), MessageID: string(event.Info.ID),
		Direction: projectedMessageDirection(event.Info.IsFromMe), ProviderTimestamp: event.Info.Timestamp.UTC(),
		Provenance: projection_model.MessageProvenanceLive,
	}
	if !event.Info.Sender.IsEmpty() {
		payload.SenderJID = boundedStringPointer(event.Info.Sender.ToNonAD().String(), 255)
	}
	if event.Info.IsFromMe {
		payload.RecipientJID = boundedStringPointer(chatID, 255)
	}
	if event.Info.IsGroup && !event.Info.Sender.IsEmpty() {
		payload.ParticipantJID = boundedStringPointer(event.Info.Sender.ToNonAD().String(), 255)
	}
	populateNormalizedMessage(&payload, event.Info.Type, event.Message)
	if payload.MessageType == "" {
		payload.MessageType = "unknown"
	}
	if event.Info.IsFromMe {
		status, sentAt := "sent", event.Info.Timestamp.UTC()
		payload.Status, payload.SentAt = &status, &sentAt
	} else {
		status := "received"
		payload.Status = &status
	}
	return newMessageProjectionEvent(instanceID, "message", payload.MessageID, event.Info.Timestamp.UTC(), payload)
}

func normalizeReceiptEvent(instanceID string, event *events.Receipt) (*projection_model.Event, bool, error) {
	if event == nil {
		return nil, true, errors.New("receipt event is incomplete")
	}
	receiptType, supported := normalizedReceiptType(event.Type)
	if !supported {
		return nil, false, nil
	}
	if event.Chat.IsEmpty() || event.Timestamp.IsZero() || len(event.MessageIDs) == 0 {
		return nil, true, errors.New("receipt event is incomplete")
	}
	messageIDs := make([]string, 0, len(event.MessageIDs))
	seen := make(map[string]struct{}, len(event.MessageIDs))
	for _, messageID := range event.MessageIDs {
		value := string(messageID)
		if value == "" || len(value) > 255 {
			return nil, true, errors.New("receipt event contains an invalid message identity")
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		messageIDs = append(messageIDs, value)
	}
	sort.Strings(messageIDs)
	recipient := event.Sender
	if recipient.IsEmpty() {
		recipient = event.Chat
	}
	if recipient.IsEmpty() {
		return nil, true, errors.New("receipt event has no recipient identity")
	}
	receiptAt := event.Timestamp.UTC()
	direction := projection_model.MessageDirectionOutgoing
	if event.Type == types.ReceiptTypeReadSelf || event.Type == types.ReceiptTypePlayedSelf {
		direction = projection_model.MessageDirectionIncoming
	}
	recipientID := boundedStringPointer(recipient.ToNonAD().String(), 255)
	chatID := event.Chat.ToNonAD().String()
	if recipientID == nil || len(chatID) > 255 {
		return nil, true, errors.New("receipt event identity exceeds storage limits")
	}
	payload := messageEventPayload{
		ChatID: chatID, ChatType: projectedChatType(event.Chat), MessageIDs: messageIDs,
		RecipientJID: recipientID, Direction: direction,
		ReceiptType: receiptType, ReceiptAt: &receiptAt,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, true, err
	}
	sum := sha256.Sum256(encoded)
	entityKey := "receipt:" + hex.EncodeToString(sum[:])
	return newMessageProjectionEvent(instanceID, "receipt", entityKey, receiptAt, payload)
}

func newMessageProjectionEvent(instanceID, eventType, entityKey string, occurredAt time.Time, payload messageEventPayload) (*projection_model.Event, bool, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, true, err
	}
	sum := sha256.Sum256([]byte(eventType + "\x00" + entityKey + "\x00" + occurredAt.Format(time.RFC3339Nano) + "\x00" + string(encoded)))
	return &projection_model.Event{
		InstanceID: instanceID, Resource: messageResource, EventKey: hex.EncodeToString(sum[:]),
		EntityKey: entityKey, EventType: eventType, OccurredAt: occurredAt, Payload: encoded,
	}, true, nil
}

func populateNormalizedMessage(payload *messageEventPayload, infoType string, message *waE2E.Message) {
	payload.MessageType = boundedString(infoType, 64)
	var contextInfo *waE2E.ContextInfo
	switch {
	case message.GetConversation() != "":
		payload.MessageType = "text"
		payload.ContentText = boundedTextPointer(message.GetConversation(), 32*1024)
	case message.GetExtendedTextMessage() != nil:
		item := message.GetExtendedTextMessage()
		payload.MessageType = "text"
		payload.ContentText = boundedTextPointer(item.GetText(), 32*1024)
		contextInfo = item.GetContextInfo()
	case message.GetImageMessage() != nil:
		item := message.GetImageMessage()
		setMedia(payload, "image", item.GetMimetype(), "", item.FileLength, nil, item.Width, item.Height, item.GetCaption())
		contextInfo = item.GetContextInfo()
	case message.GetVideoMessage() != nil:
		item := message.GetVideoMessage()
		setMedia(payload, "video", item.GetMimetype(), "", item.FileLength, item.Seconds, item.Width, item.Height, item.GetCaption())
		contextInfo = item.GetContextInfo()
	case message.GetAudioMessage() != nil:
		item := message.GetAudioMessage()
		setMedia(payload, "audio", item.GetMimetype(), "", item.FileLength, item.Seconds, nil, nil, "")
		contextInfo = item.GetContextInfo()
	case message.GetDocumentMessage() != nil:
		item := message.GetDocumentMessage()
		setMedia(payload, "document", item.GetMimetype(), item.GetFileName(), item.FileLength, nil, nil, nil, item.GetCaption())
		contextInfo = item.GetContextInfo()
	}
	if contextInfo != nil {
		payload.QuotedMessageID = boundedStringPointer(contextInfo.GetStanzaID(), 255)
	}
	summary := ""
	if payload.ContentText != nil {
		summary = *payload.ContentText
	} else if payload.Caption != nil {
		summary = *payload.Caption
	} else if payload.MediaType != nil {
		summary = "[" + *payload.MediaType + "]"
	} else if payload.MessageType != "" {
		summary = "[" + payload.MessageType + "]"
	}
	payload.ContentSummary = boundedTextPointer(summary, 512)
}

func setMedia(payload *messageEventPayload, mediaType, mimeType, fileName string, size *uint64, duration, width, height *uint32, caption string) {
	payload.MessageType = mediaType
	payload.MediaType = boundedStringPointer(mediaType, 64)
	payload.MediaMIMEType = boundedStringPointer(mimeType, 255)
	payload.MediaFileName = boundedStringPointer(fileName, 1024)
	if size != nil && *size <= math.MaxInt64 {
		value := int64(*size)
		payload.MediaSize = &value
	}
	if duration != nil {
		payload.MediaDurationSeconds = duration
	}
	if width != nil {
		payload.MediaWidth = width
	}
	if height != nil {
		payload.MediaHeight = height
	}
	payload.Caption = boundedTextPointer(caption, 8*1024)
}

func projectedChatType(jid types.JID) projection_model.ChatType {
	switch jid.ToNonAD().Server {
	case types.GroupServer:
		return projection_model.ChatTypeGroup
	case types.BroadcastServer:
		return projection_model.ChatTypeBroadcast
	case types.NewsletterServer:
		return projection_model.ChatTypeNewsletter
	case types.DefaultUserServer, types.LegacyUserServer, types.HiddenUserServer, types.HostedLIDServer:
		return projection_model.ChatTypeDirect
	default:
		return projection_model.ChatTypeUnknown
	}
}

func projectedMessageDirection(fromMe bool) projection_model.MessageDirection {
	if fromMe {
		return projection_model.MessageDirectionOutgoing
	}
	return projection_model.MessageDirectionIncoming
}

func normalizedReceiptType(value types.ReceiptType) (string, bool) {
	switch value {
	case types.ReceiptTypeDelivered:
		return "delivered", true
	case types.ReceiptTypeSender:
		return "sent", true
	case types.ReceiptTypeRead, types.ReceiptTypeReadSelf:
		return "read", true
	case types.ReceiptTypePlayed, types.ReceiptTypePlayedSelf:
		return "played", true
	case types.ReceiptTypeRetry, types.ReceiptTypeServerError:
		return "error", true
	default:
		return "", false
	}
}

func boundedStringPointer(value string, limit int) *string {
	value = strings.TrimSpace(boundedString(value, limit))
	if value == "" {
		return nil
	}
	return &value
}

func boundedTextPointer(value string, limit int) *string {
	value = boundedString(value, limit)
	if value == "" {
		return nil
	}
	return &value
}

func boundedString(value string, limit int) string {
	if limit < 1 || len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
