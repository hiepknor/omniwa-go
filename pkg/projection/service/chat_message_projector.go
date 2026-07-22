package projection_service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
)

const (
	ChatsProjectionSchemaVersion    int64 = 1
	MessagesProjectionSchemaVersion int64 = 1
)

type chatMessageProjectionWriter interface {
	ApplyChat(context.Context, *projection_model.Chat, ...projection_repository.ChatAspect) (bool, error)
	ApplyMessage(context.Context, *projection_model.ProjectedMessage, ...projection_repository.MessageAspect) (bool, error)
	ApplyReceipt(context.Context, *projection_model.MessageReceipt) (bool, error)
}

type ChatMessageProjector struct {
	repository chatMessageProjectionWriter
	state      projectionEventState
}

func NewChatMessageProjector(repository chatMessageProjectionWriter, state projectionEventState) *ChatMessageProjector {
	return &ChatMessageProjector{repository: repository, state: state}
}

func (p *ChatMessageProjector) Handle(ctx context.Context, event *projection_model.Event) error {
	if p == nil || p.repository == nil || p.state == nil {
		return errors.New("chat and message projector dependencies are required")
	}
	if event == nil || event.Resource != messageResource || event.InstanceID == "" || event.EventKey == "" {
		return errors.New("unsupported chat and message projection event")
	}
	var payload messageEventPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return errors.New("invalid normalized message projection payload")
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
		return errors.New("unsupported chat and message projection event")
	}
	return p.state.RecordEvent(event.InstanceID, messageResource, MessagesProjectionSchemaVersion, event.OccurredAt)
}

func (p *ChatMessageProjector) applyMessage(ctx context.Context, event *projection_model.Event, payload *messageEventPayload) error {
	if payload.ChatID == "" || payload.MessageID == "" || payload.MessageID != event.EntityKey || payload.ProviderTimestamp.IsZero() ||
		payload.MessageType == "" || payload.Direction == "" || payload.Provenance == "" {
		return errors.New("normalized message projection payload is incomplete")
	}
	activityAt := payload.ProviderTimestamp.UTC()
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
		SourceOccurredAt: event.OccurredAt, SourceEventKey: event.EventKey,
	}
	_, err := p.repository.ApplyMessage(ctx, message,
		projection_repository.MessageAspectEnvelope, projection_repository.MessageAspectContent,
		projection_repository.MessageAspectMedia, projection_repository.MessageAspectLifecycle,
	)
	return err
}

func (p *ChatMessageProjector) applyHistoryChat(ctx context.Context, event *projection_model.Event, payload *messageEventPayload) error {
	if payload.ChatID == "" || payload.ChatID != event.EntityKey || payload.ChatType == "" {
		return errors.New("normalized history chat projection payload is incomplete")
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
		return errors.New("normalized receipt projection payload is incomplete")
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
			return errors.New("normalized receipt contains an empty message identity")
		}
		placeholder := &projection_model.ProjectedMessage{
			InstanceID: event.InstanceID, MessageID: messageID, ChatID: payload.ChatID,
			Direction: payload.Direction, MessageType: "unknown", ProviderTimestamp: payload.ReceiptAt.UTC(),
			Provenance:       projection_model.MessageProvenanceLive,
			SourceOccurredAt: time.Unix(0, 0).UTC(), SourceEventKey: projectionChildEventKey("placeholder", event.EventKey, messageID),
		}
		if _, err := p.repository.ApplyMessage(ctx, placeholder, projection_repository.MessageAspectEnvelope); err != nil {
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
