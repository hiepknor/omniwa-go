package projection_service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
)

const (
	ChatsProjectionSchemaVersion    int64 = 1
	MessagesProjectionSchemaVersion int64 = 1
	DefaultMessageRetention               = 90 * 24 * time.Hour
)

type chatMessageProjectionWriter interface {
	ApplyChat(context.Context, *projection_model.Chat, ...projection_repository.ChatAspect) (bool, error)
	ApplyMessage(context.Context, *projection_model.ProjectedMessage, ...projection_repository.MessageAspect) (bool, error)
	ApplyReceipt(context.Context, *projection_model.MessageReceipt) (bool, error)
}

type ChatMessageProjector struct {
	repository chatMessageProjectionWriter
	state      projectionEventState
	retention  time.Duration
}

func NewChatMessageProjector(repository chatMessageProjectionWriter, state projectionEventState, retention ...time.Duration) *ChatMessageProjector {
	policy := DefaultMessageRetention
	if len(retention) == 1 {
		policy = retention[0]
	}
	return &ChatMessageProjector{repository: repository, state: state, retention: policy}
}

func (p *ChatMessageProjector) Handle(ctx context.Context, event *projection_model.Event) error {
	if p == nil || p.repository == nil || p.state == nil || p.retention <= 0 {
		return permanentProjectionFailure(errorCodeMisconfigured)
	}
	if event == nil || event.Resource != messageResource || event.InstanceID == "" || event.EventKey == "" {
		return permanentProjectionFailure(errorCodeUnsupportedEvent)
	}
	var payload messageEventPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return permanentProjectionFailure(errorCodeInvalidPayload)
	}
	switch event.EventType {
	case "message", "history_message":
		if err := p.applyMessage(ctx, event, &payload); err != nil {
			return err
		}
		if err := p.state.RecordEvent(event.InstanceID, "chats", ChatsProjectionSchemaVersion, event.OccurredAt); err != nil {
			return err
		}
	case "receipt":
		if err := p.applyReceipts(ctx, event, &payload); err != nil {
			return err
		}
	case "history_chat":
		if err := p.applyHistoryChat(ctx, event, &payload); err != nil {
			return err
		}
		return p.state.RecordEvent(event.InstanceID, "chats", ChatsProjectionSchemaVersion, event.OccurredAt)
	default:
		return permanentProjectionFailure(errorCodeUnsupportedEvent)
	}
	return p.state.RecordEvent(event.InstanceID, messageResource, MessagesProjectionSchemaVersion, event.OccurredAt)
}

func (p *ChatMessageProjector) applyMessage(ctx context.Context, event *projection_model.Event, payload *messageEventPayload) error {
	if payload.ChatID == "" || payload.MessageID == "" || payload.MessageID != event.EntityKey || payload.ProviderTimestamp.IsZero() ||
		payload.MessageType == "" || payload.Direction == "" || payload.Provenance == "" {
		return permanentProjectionFailure(errorCodeIncompletePayload)
	}
	activityAt := payload.ProviderTimestamp.UTC()
	retentionExpiresAt := activityAt.Add(p.retention)
	chat := &projection_model.Chat{
		InstanceID: event.InstanceID, ChatID: payload.ChatID, Type: payload.ChatType,
		LastMessageID: &payload.MessageID, LastMessageAt: &activityAt, LastActivityAt: &activityAt,
		SourceOccurredAt: event.OccurredAt, SourceEventKey: payload.MessageID,
	}
	if _, err := p.repository.ApplyChat(ctx, chat, projection_repository.ChatAspectIdentity, projection_repository.ChatAspectActivity); err != nil {
		return err
	}
	message := &projection_model.ProjectedMessage{
		InstanceID: event.InstanceID, MessageID: payload.MessageID, ChatID: payload.ChatID,
		SenderJID: payload.SenderJID, RecipientJID: payload.RecipientJID, ParticipantJID: payload.ParticipantJID,
		Direction: payload.Direction, MessageType: payload.MessageType, ContentText: payload.ContentText,
		Caption: payload.Caption, ContentSummary: payload.ContentSummary, QuotedMessageID: payload.QuotedMessageID,
		MediaType: payload.MediaType, MediaMIMEType: payload.MediaMIMEType, MediaFileName: payload.MediaFileName,
		MediaSize: payload.MediaSize, MediaDuration: payload.MediaDurationSeconds, MediaWidth: payload.MediaWidth, MediaHeight: payload.MediaHeight,
		Status: payload.Status, ProviderTimestamp: activityAt, SentAt: payload.SentAt, Provenance: payload.Provenance, HistorySyncID: payload.HistorySyncID,
		RetentionExpiresAt: &retentionExpiresAt,
		SourceOccurredAt:   event.OccurredAt, SourceEventKey: event.EventKey,
	}
	_, err := p.repository.ApplyMessage(ctx, message,
		projection_repository.MessageAspectEnvelope, projection_repository.MessageAspectContent,
		projection_repository.MessageAspectMedia, projection_repository.MessageAspectLifecycle,
		projection_repository.MessageAspectRetention,
	)
	return err
}

func (p *ChatMessageProjector) applyHistoryChat(ctx context.Context, event *projection_model.Event, payload *messageEventPayload) error {
	if payload.ChatID == "" || payload.ChatID != event.EntityKey || payload.ChatType == "" {
		return permanentProjectionFailure(errorCodeIncompletePayload)
	}
	chat := &projection_model.Chat{
		InstanceID: event.InstanceID, ChatID: payload.ChatID, Type: payload.ChatType, DisplayName: payload.DisplayName,
		Archived: payload.Archived, Pinned: payload.Pinned, MutedUntil: payload.MutedUntil, DisappearingTimer: payload.DisappearingTimer,
		SourceOccurredAt: event.OccurredAt, SourceEventKey: event.EventKey,
	}
	if payload.UnreadCount != nil {
		chat.UnreadCount = *payload.UnreadCount
	}
	aspects := []projection_repository.ChatAspect{projection_repository.ChatAspectIdentity, projection_repository.ChatAspectSettings}
	if payload.LastActivityAt != nil {
		activityAt := payload.LastActivityAt.UTC()
		chat.LastActivityAt = &activityAt
		aspects = append(aspects, projection_repository.ChatAspectActivity)
	}
	_, err := p.repository.ApplyChat(ctx, chat, aspects...)
	return err
}

func (p *ChatMessageProjector) applyReceipts(ctx context.Context, event *projection_model.Event, payload *messageEventPayload) error {
	if payload.ChatID == "" || len(payload.MessageIDs) == 0 || payload.RecipientJID == nil || *payload.RecipientJID == "" ||
		payload.ReceiptType == "" || payload.ReceiptAt == nil || payload.ReceiptAt.IsZero() || payload.Direction == "" {
		return permanentProjectionFailure(errorCodeIncompletePayload)
	}
	chat := &projection_model.Chat{
		InstanceID: event.InstanceID, ChatID: payload.ChatID, Type: payload.ChatType,
		SourceOccurredAt: event.OccurredAt, SourceEventKey: event.EventKey,
	}
	if _, err := p.repository.ApplyChat(ctx, chat, projection_repository.ChatAspectIdentity); err != nil {
		return err
	}
	for _, messageID := range payload.MessageIDs {
		if messageID == "" {
			return permanentProjectionFailure(errorCodeIdentityMismatch)
		}
		retentionExpiresAt := payload.ReceiptAt.UTC().Add(p.retention)
		placeholder := &projection_model.ProjectedMessage{
			InstanceID: event.InstanceID, MessageID: messageID, ChatID: payload.ChatID,
			Direction: payload.Direction, MessageType: "unknown", ProviderTimestamp: payload.ReceiptAt.UTC(),
			Provenance:         projection_model.MessageProvenanceLive,
			RetentionExpiresAt: &retentionExpiresAt,
			SourceOccurredAt:   time.Unix(0, 0).UTC(), SourceEventKey: projectionChildEventKey("placeholder", event.EventKey, messageID),
		}
		if _, err := p.repository.ApplyMessage(ctx, placeholder, projection_repository.MessageAspectEnvelope, projection_repository.MessageAspectRetention); err != nil {
			return err
		}
		receipt := &projection_model.MessageReceipt{
			InstanceID: event.InstanceID, MessageID: messageID, RecipientJID: *payload.RecipientJID, ReceiptType: payload.ReceiptType,
			ReceiptAt: payload.ReceiptAt.UTC(), SourceOccurredAt: event.OccurredAt,
			SourceEventKey: projectionChildEventKey("receipt", event.EventKey, messageID),
		}
		if _, err := p.repository.ApplyReceipt(ctx, receipt); err != nil {
			return err
		}
	}
	return nil
}

func projectionChildEventKey(kind, parentKey, entityKey string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + parentKey + "\x00" + entityKey))
	return hex.EncodeToString(sum[:])
}
