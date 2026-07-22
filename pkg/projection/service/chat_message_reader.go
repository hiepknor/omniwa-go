package projection_service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
	"gorm.io/gorm"
)

var (
	ErrChatsProjectionNotReady    = errors.New("chats projection is not ready")
	ErrMessagesProjectionNotReady = errors.New("messages projection is not ready")
	ErrInvalidProjectionCursor    = errors.New("invalid projection cursor")
)

const projectionCursorVersion = 1

type chatMessageReadRepository interface {
	GetChat(context.Context, string, string) (*projection_model.Chat, error)
	ListChats(context.Context, string, int, *projection_repository.ChatCursor) (*projection_repository.ChatPage, error)
	GetMessage(context.Context, string, string) (*projection_model.ProjectedMessage, error)
	ListMessages(context.Context, string, string, int, *projection_repository.MessageCursor) (*projection_repository.MessagePage, error)
	ListReceipts(context.Context, string, string) ([]projection_model.MessageReceipt, error)
}

type ChatMessageReader struct {
	repository chatMessageReadRepository
	state      groupReadState
	retention  time.Duration
}

// ProjectedChat is the stable public chat representation. Storage coordination
// fields are intentionally excluded from the API contract.
type ProjectedChat struct {
	ChatID            string                    `json:"chatId"`
	ContactID         *string                   `json:"contactId,omitempty"`
	Type              projection_model.ChatType `json:"type"`
	DisplayName       *string                   `json:"displayName,omitempty"`
	LastMessageID     *string                   `json:"lastMessageId,omitempty"`
	LastMessageAt     *time.Time                `json:"lastMessageAt,omitempty"`
	LastActivityAt    *time.Time                `json:"lastActivityAt,omitempty"`
	UnreadCount       int                       `json:"unreadCount"`
	Archived          *bool                     `json:"archived,omitempty"`
	Pinned            *bool                     `json:"pinned,omitempty"`
	MutedUntil        *time.Time                `json:"mutedUntil,omitempty"`
	DisappearingTimer *uint32                   `json:"disappearingTimer,omitempty"`
}

// ProjectedMessage is the stable public message representation. It contains
// normalized summaries and media metadata, never provider-native payloads.
type ProjectedMessage struct {
	MessageID          string                             `json:"messageId"`
	ChatID             string                             `json:"chatId"`
	SenderJID          *string                            `json:"senderJid,omitempty"`
	RecipientJID       *string                            `json:"recipientJid,omitempty"`
	ParticipantJID     *string                            `json:"participantJid,omitempty"`
	Direction          projection_model.MessageDirection  `json:"direction"`
	MessageType        string                             `json:"messageType"`
	ContentText        *string                            `json:"contentText,omitempty"`
	Caption            *string                            `json:"caption,omitempty"`
	ContentSummary     *string                            `json:"contentSummary,omitempty"`
	QuotedMessageID    *string                            `json:"quotedMessageId,omitempty"`
	MediaType          *string                            `json:"mediaType,omitempty"`
	MediaMIMEType      *string                            `json:"mediaMimeType,omitempty"`
	MediaFileName      *string                            `json:"mediaFileName,omitempty"`
	MediaSize          *int64                             `json:"mediaSize,omitempty"`
	MediaDuration      *uint32                            `json:"mediaDurationSeconds,omitempty"`
	MediaWidth         *uint32                            `json:"mediaWidth,omitempty"`
	MediaHeight        *uint32                            `json:"mediaHeight,omitempty"`
	Status             *string                            `json:"status,omitempty"`
	ProviderTimestamp  time.Time                          `json:"providerTimestamp"`
	SentAt             *time.Time                         `json:"sentAt,omitempty"`
	DeliveredAt        *time.Time                         `json:"deliveredAt,omitempty"`
	ReadAt             *time.Time                         `json:"readAt,omitempty"`
	PlayedAt           *time.Time                         `json:"playedAt,omitempty"`
	Provenance         projection_model.MessageProvenance `json:"provenance"`
	HistorySyncID      *string                            `json:"historySyncId,omitempty"`
	RetentionExpiresAt *time.Time                         `json:"retentionExpiresAt,omitempty"`
}

type ProjectedMessageReceipt struct {
	MessageID    string    `json:"messageId"`
	RecipientJID string    `json:"recipientJid"`
	ReceiptType  string    `json:"receiptType"`
	ReceiptAt    time.Time `json:"receiptAt"`
}

type projectionCursor struct {
	Version           int        `json:"v"`
	Kind              string     `json:"kind"`
	ChatID            string     `json:"chatId,omitempty"`
	LastActivityAt    *time.Time `json:"lastActivityAt,omitempty"`
	MessageID         string     `json:"messageId,omitempty"`
	ProviderTimestamp *time.Time `json:"providerTimestamp,omitempty"`
}

func NewChatMessageReader(repository chatMessageReadRepository, state groupReadState, retention ...time.Duration) *ChatMessageReader {
	policy := DefaultMessageRetention
	if len(retention) == 1 {
		policy = retention[0]
	}
	return &ChatMessageReader{repository: repository, state: state, retention: policy}
}

func (r *ChatMessageReader) ListChats(ctx context.Context, instanceID string, limit int, cursor string) ([]ProjectedChat, *ProjectionReadMeta, error) {
	meta, err := r.readMeta(instanceID, "chats", ChatsProjectionSchemaVersion, ErrChatsProjectionNotReady)
	if err != nil {
		return nil, nil, err
	}
	decoded, err := decodeChatCursor(cursor)
	if err != nil {
		return nil, nil, err
	}
	page, err := r.repository.ListChats(ctx, instanceID, limit, decoded)
	if err != nil {
		return nil, nil, err
	}
	items := make([]ProjectedChat, len(page.Items))
	for index := range page.Items {
		items[index] = projectedChatView(&page.Items[index])
	}
	if page.NextCursor != nil {
		meta.NextCursor, err = encodeProjectionCursor(projectionCursor{
			Version: projectionCursorVersion, Kind: "chats", ChatID: page.NextCursor.ChatID, LastActivityAt: page.NextCursor.LastActivityAt,
		})
		if err != nil {
			return nil, nil, err
		}
	}
	return items, meta, nil
}

func (r *ChatMessageReader) GetChat(ctx context.Context, instanceID, chatID string) (*ProjectedChat, *ProjectionReadMeta, error) {
	if chatID == "" {
		return nil, nil, errors.New("chat identity is required")
	}
	meta, err := r.readMeta(instanceID, "chats", ChatsProjectionSchemaVersion, ErrChatsProjectionNotReady)
	if err != nil {
		return nil, nil, err
	}
	chat, err := r.repository.GetChat(ctx, instanceID, chatID)
	if err != nil {
		return nil, meta, err
	}
	view := projectedChatView(chat)
	return &view, meta, nil
}

func (r *ChatMessageReader) ListMessages(ctx context.Context, instanceID, chatID string, limit int, cursor string) ([]ProjectedMessage, *ProjectionReadMeta, error) {
	if chatID == "" {
		return nil, nil, errors.New("chat identity is required")
	}
	meta, err := r.readMeta(instanceID, messageResource, MessagesProjectionSchemaVersion, ErrMessagesProjectionNotReady)
	if err != nil {
		return nil, nil, err
	}
	decoded, err := decodeMessageCursor(cursor, chatID)
	if err != nil {
		return nil, nil, err
	}
	page, err := r.repository.ListMessages(ctx, instanceID, chatID, limit, decoded)
	if err != nil {
		return nil, nil, err
	}
	items := make([]ProjectedMessage, len(page.Items))
	for index := range page.Items {
		items[index] = projectedMessageView(&page.Items[index], r.retention)
	}
	if page.NextCursor != nil {
		at := page.NextCursor.ProviderTimestamp.UTC()
		meta.NextCursor, err = encodeProjectionCursor(projectionCursor{
			Version: projectionCursorVersion, Kind: "messages", ChatID: chatID, MessageID: page.NextCursor.MessageID, ProviderTimestamp: &at,
		})
		if err != nil {
			return nil, nil, err
		}
	}
	return items, meta, nil
}

func (r *ChatMessageReader) GetMessage(ctx context.Context, instanceID, messageID string) (*ProjectedMessage, *ProjectionReadMeta, error) {
	if messageID == "" {
		return nil, nil, errors.New("message identity is required")
	}
	meta, err := r.readMeta(instanceID, messageResource, MessagesProjectionSchemaVersion, ErrMessagesProjectionNotReady)
	if err != nil {
		return nil, nil, err
	}
	message, err := r.repository.GetMessage(ctx, instanceID, messageID)
	if err != nil {
		return nil, meta, err
	}
	view := projectedMessageView(message, r.retention)
	return &view, meta, nil
}

func (r *ChatMessageReader) ListReceipts(ctx context.Context, instanceID, messageID string) ([]ProjectedMessageReceipt, *ProjectionReadMeta, error) {
	if messageID == "" {
		return nil, nil, errors.New("message identity is required")
	}
	meta, err := r.readMeta(instanceID, messageResource, MessagesProjectionSchemaVersion, ErrMessagesProjectionNotReady)
	if err != nil {
		return nil, nil, err
	}
	if _, err := r.repository.GetMessage(ctx, instanceID, messageID); err != nil {
		return nil, nil, err
	}
	receipts, err := r.repository.ListReceipts(ctx, instanceID, messageID)
	if err != nil {
		return nil, nil, err
	}
	items := make([]ProjectedMessageReceipt, len(receipts))
	for index := range receipts {
		items[index] = ProjectedMessageReceipt{
			MessageID: receipts[index].MessageID, RecipientJID: receipts[index].RecipientJID,
			ReceiptType: receipts[index].ReceiptType, ReceiptAt: receipts[index].ReceiptAt,
		}
	}
	return items, meta, nil
}

func projectedChatView(chat *projection_model.Chat) ProjectedChat {
	return ProjectedChat{
		ChatID: chat.ChatID, ContactID: chat.ContactID, Type: chat.Type, DisplayName: chat.DisplayName,
		LastMessageID: chat.LastMessageID, LastMessageAt: chat.LastMessageAt, LastActivityAt: chat.LastActivityAt,
		UnreadCount: chat.UnreadCount, Archived: chat.Archived, Pinned: chat.Pinned, MutedUntil: chat.MutedUntil,
		DisappearingTimer: chat.DisappearingTimer,
	}
}

func projectedMessageView(message *projection_model.ProjectedMessage, retention time.Duration) ProjectedMessage {
	retentionExpiresAt := message.ProviderTimestamp.UTC().Add(retention)
	return ProjectedMessage{
		MessageID: message.MessageID, ChatID: message.ChatID, SenderJID: message.SenderJID, RecipientJID: message.RecipientJID,
		ParticipantJID: message.ParticipantJID, Direction: message.Direction, MessageType: message.MessageType,
		ContentText: message.ContentText, Caption: message.Caption, ContentSummary: message.ContentSummary,
		QuotedMessageID: message.QuotedMessageID, MediaType: message.MediaType, MediaMIMEType: message.MediaMIMEType,
		MediaFileName: message.MediaFileName, MediaSize: message.MediaSize, MediaDuration: message.MediaDuration,
		MediaWidth: message.MediaWidth, MediaHeight: message.MediaHeight, Status: message.Status,
		ProviderTimestamp: message.ProviderTimestamp, SentAt: message.SentAt, DeliveredAt: message.DeliveredAt,
		ReadAt: message.ReadAt, PlayedAt: message.PlayedAt, Provenance: message.Provenance,
		HistorySyncID: message.HistorySyncID, RetentionExpiresAt: &retentionExpiresAt,
	}
}

func (r *ChatMessageReader) readMeta(instanceID, resource string, version int64, notReady error) (*ProjectionReadMeta, error) {
	if r == nil || r.repository == nil || r.state == nil || r.retention <= 0 || instanceID == "" {
		return nil, errors.New("chat and message projection reader dependencies and instance identity are required")
	}
	state, err := r.state.GetServingState(instanceID, resource)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, notReady
	}
	if err != nil {
		return nil, err
	}
	if state == nil {
		return nil, notReady
	}
	usable := state.SyncStatus == projection_model.SyncStatusReady || state.SyncStatus == projection_model.SyncStatusStale || state.SyncStatus == projection_model.SyncStatusSyncing
	if !usable || state.LastReconciledAt == nil || state.SchemaVersion < version {
		return nil, notReady
	}
	lastSyncedAt := state.LastReconciledAt.UTC()
	return &ProjectionReadMeta{Source: "projection", SyncStatus: state.SyncStatus, LastSyncedAt: &lastSyncedAt}, nil
}

func encodeProjectionCursor(cursor projectionCursor) (string, error) {
	value, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func decodeChatCursor(value string) (*projection_repository.ChatCursor, error) {
	if value == "" {
		return nil, nil
	}
	cursor, err := decodeProjectionCursor(value, "chats")
	if err != nil || cursor.ChatID == "" {
		return nil, ErrInvalidProjectionCursor
	}
	return &projection_repository.ChatCursor{ChatID: cursor.ChatID, LastActivityAt: cursor.LastActivityAt}, nil
}

func decodeMessageCursor(value, chatID string) (*projection_repository.MessageCursor, error) {
	if value == "" {
		return nil, nil
	}
	cursor, err := decodeProjectionCursor(value, "messages")
	if err != nil || cursor.ChatID != chatID || cursor.MessageID == "" || cursor.ProviderTimestamp == nil || cursor.ProviderTimestamp.IsZero() {
		return nil, ErrInvalidProjectionCursor
	}
	return &projection_repository.MessageCursor{MessageID: cursor.MessageID, ProviderTimestamp: cursor.ProviderTimestamp.UTC()}, nil
}

func decodeProjectionCursor(value, kind string) (*projectionCursor, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, ErrInvalidProjectionCursor
	}
	var cursor projectionCursor
	if err := json.Unmarshal(decoded, &cursor); err != nil || cursor.Version != projectionCursorVersion || cursor.Kind != kind {
		return nil, ErrInvalidProjectionCursor
	}
	return &cursor, nil
}
