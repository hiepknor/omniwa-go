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
	ChatsProjectionSchemaVersion    int64 = 2
	MessagesProjectionSchemaVersion int64 = 3
	DefaultMessageRetention               = 90 * 24 * time.Hour
)

type chatMessageProjectionWriter interface {
	ApplyChat(context.Context, *projection_model.Chat, ...projection_repository.ChatAspect) (bool, error)
	ApplyMessage(context.Context, *projection_model.ProjectedMessage, ...projection_repository.MessageAspect) (bool, error)
	ApplyReceipt(context.Context, *projection_model.MessageReceipt) (bool, error)
	MarkMessageRead(context.Context, string, string, time.Time) (bool, error)
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
	case "chat_archived", "chat_pinned", "chat_muted":
		if err := p.applyChatSetting(ctx, event, &payload); err != nil {
			return err
		}
		return p.state.RecordEvent(event.InstanceID, "chats", ChatsProjectionSchemaVersion, event.OccurredAt)
	default:
		return permanentProjectionFailure(errorCodeUnsupportedEvent)
	}
	return p.state.RecordEvent(event.InstanceID, messageResource, MessagesProjectionSchemaVersion, event.OccurredAt)
}

func (p *ChatMessageProjector) applyChatSetting(ctx context.Context, event *projection_model.Event, payload *messageEventPayload) error {
	if payload.ChatID == "" || payload.ChatID != event.EntityKey || payload.ChatType == "" {
		return permanentProjectionFailure(errorCodeIncompletePayload)
	}
	chat := &projection_model.Chat{
		InstanceID: event.InstanceID, ChatID: payload.ChatID, Type: payload.ChatType,
		SourceOccurredAt: event.OccurredAt, SourceEventKey: event.EventKey,
	}
	if _, err := p.repository.ApplyChat(ctx, chat, projection_repository.ChatAspectIdentity); err != nil {
		return err
	}
	var aspect projection_repository.ChatAspect
	switch event.EventType {
	case "chat_archived":
		if payload.Archived == nil {
			return permanentProjectionFailure(errorCodeIncompletePayload)
		}
		chat.Archived, aspect = payload.Archived, projection_repository.ChatAspectArchived
	case "chat_pinned":
		if payload.Pinned == nil {
			return permanentProjectionFailure(errorCodeIncompletePayload)
		}
		chat.Pinned, aspect = payload.Pinned, projection_repository.ChatAspectPinned
	case "chat_muted":
		chat.MutedUntil, aspect = payload.MutedUntil, projection_repository.ChatAspectMuted
	default:
		return permanentProjectionFailure(errorCodeUnsupportedEvent)
	}
	_, err := p.repository.ApplyChat(ctx, chat, aspect)
	return err
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
		MediaAssetID: payload.MediaAssetID,
		Status:       payload.Status, ProviderTimestamp: activityAt, SentAt: payload.SentAt, Provenance: payload.Provenance, HistorySyncID: payload.HistorySyncID,
		RetentionExpiresAt: &retentionExpiresAt,
		SourceOccurredAt:   event.OccurredAt, SourceEventKey: event.EventKey,
	}
	messageAspects := []projection_repository.MessageAspect{
		projection_repository.MessageAspectEnvelope, projection_repository.MessageAspectContent,
		projection_repository.MessageAspectMedia, projection_repository.MessageAspectLifecycle,
		projection_repository.MessageAspectRetention,
	}
	if payload.Direction == projection_model.MessageDirectionOutgoing || payload.Provenance == projection_model.MessageProvenanceLive {
		unread := payload.Direction == projection_model.MessageDirectionIncoming
		message.IsUnread = &unread
		messageAspects = append(messageAspects, projection_repository.MessageAspectUnread)
	}
	_, err := p.repository.ApplyMessage(ctx, message, messageAspects...)
	return err
}

func (p *ChatMessageProjector) applyHistoryChat(ctx context.Context, event *projection_model.Event, payload *messageEventPayload) error {
	if payload.ChatID == "" || payload.ChatID != event.EntityKey || payload.ChatType == "" {
		return permanentProjectionFailure(errorCodeIncompletePayload)
	}
	observedAt := event.IngestedAt.UTC()
	if observedAt.IsZero() {
		observedAt = event.OccurredAt.UTC()
	}
	chat := &projection_model.Chat{
		InstanceID: event.InstanceID, ChatID: payload.ChatID, Type: payload.ChatType, DisplayName: payload.DisplayName,
		Archived: payload.Archived, Pinned: payload.Pinned, MutedUntil: payload.MutedUntil, DisappearingTimer: payload.DisappearingTimer,
		UnreadSnapshotSyncID: payload.HistorySyncID,
		SourceOccurredAt:     observedAt, SourceEventKey: event.EventKey,
	}
	if payload.UnreadCount != nil {
		chat.UnreadCount = *payload.UnreadCount
	}
	aspects := []projection_repository.ChatAspect{
		projection_repository.ChatAspectIdentity,
		projection_repository.ChatAspectSettings,
	}
	if payload.HistorySyncID != nil {
		aspects = append(aspects, projection_repository.ChatAspectUnreadSnapshot)
	}
	if _, err := p.repository.ApplyChat(ctx, chat, aspects...); err != nil {
		return err
	}
	if payload.LastActivityAt != nil {
		activityAt := payload.LastActivityAt.UTC()
		activity := &projection_model.Chat{
			InstanceID: event.InstanceID, ChatID: payload.ChatID, LastActivityAt: &activityAt,
			SourceOccurredAt: event.OccurredAt, SourceEventKey: event.EventKey,
		}
		_, err := p.repository.ApplyChat(ctx, activity, projection_repository.ChatAspectActivity)
		return err
	}
	return nil
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
		messageAspects := []projection_repository.MessageAspect{projection_repository.MessageAspectEnvelope, projection_repository.MessageAspectRetention}
		if payload.Direction == projection_model.MessageDirectionIncoming {
			unread := false
			placeholder.IsUnread = &unread
			messageAspects = append(messageAspects, projection_repository.MessageAspectUnread)
		}
		if _, err := p.repository.ApplyMessage(ctx, placeholder, messageAspects...); err != nil {
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
		if payload.Direction == projection_model.MessageDirectionIncoming && (payload.ReceiptType == "read" || payload.ReceiptType == "played") {
			if _, err := p.repository.MarkMessageRead(ctx, event.InstanceID, messageID, payload.ReceiptAt.UTC()); err != nil {
				return err
			}
		}
	}
	return nil
}

func projectionChildEventKey(kind, parentKey, entityKey string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + parentKey + "\x00" + entityKey))
	return hex.EncodeToString(sum[:])
}
