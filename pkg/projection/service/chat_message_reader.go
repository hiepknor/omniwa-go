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
	GetConversation(context.Context, string, string) (*projection_repository.ConversationRecord, error)
	ListConversations(context.Context, string, int, *projection_repository.ConversationCursor) (*projection_repository.ConversationPage, error)
	ListConversationMessages(context.Context, string, string, int, *projection_repository.MessageCursor) (*projection_repository.MessagePage, error)
}

type ChatMessageReader struct {
	repository     chatMessageReadRepository
	state          groupReadState
	retention      time.Duration
	canonicalReady func(string) (bool, error)
}

// ProjectedChat is the stable public chat representation. Storage coordination
// fields are intentionally excluded from the API contract.
type ProjectedChat struct {
	ChatID               string                    `json:"chatId" binding:"required"`
	ConversationID       *string                   `json:"conversationId,omitempty"`
	ChatAliases          []string                  `json:"chatAliases,omitempty"`
	AddressingJID        *string                   `json:"addressingJid,omitempty"`
	ContactID            *string                   `json:"contactId,omitempty"`
	Type                 projection_model.ChatType `json:"type" binding:"required"`
	DisplayName          *string                   `json:"displayName,omitempty"`
	DisplayNameSource    *string                   `json:"displayNameSource,omitempty" enums:"full_name,business_name,push_name,first_name,username,provider_chat,group_subject,newsletter_name,broadcast_name"`
	DisplayNameUpdatedAt *time.Time                `json:"displayNameUpdatedAt,omitempty"`
	LastMessageID        *string                   `json:"lastMessageId,omitempty"`
	LastMessageAt        *time.Time                `json:"lastMessageAt,omitempty"`
	LastActivityAt       *time.Time                `json:"lastActivityAt,omitempty"`
	UnreadCount          int                       `json:"unreadCount" binding:"required"`
	Archived             *bool                     `json:"archived,omitempty"`
	Pinned               *bool                     `json:"pinned,omitempty"`
	MutedUntil           *time.Time                `json:"mutedUntil,omitempty"`
	DisappearingTimer    *uint32                   `json:"disappearingTimer,omitempty"`
}

// ProjectedMessage is the stable public message representation. It contains
// normalized summaries and media metadata, never provider-native payloads.
type ProjectedMessage struct {
	MessageID          string                             `json:"messageId" binding:"required"`
	ChatID             string                             `json:"chatId" binding:"required"`
	ConversationID     *string                            `json:"conversationId,omitempty"`
	SenderJID          *string                            `json:"senderJid,omitempty"`
	RecipientJID       *string                            `json:"recipientJid,omitempty"`
	ParticipantJID     *string                            `json:"participantJid,omitempty"`
	Direction          projection_model.MessageDirection  `json:"direction" binding:"required"`
	MessageType        string                             `json:"messageType" binding:"required"`
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
	MediaAssetID       *string                            `json:"mediaAssetId,omitempty"`
	Status             *string                            `json:"status,omitempty"`
	ProviderTimestamp  time.Time                          `json:"providerTimestamp" binding:"required"`
	SentAt             *time.Time                         `json:"sentAt,omitempty"`
	DeliveredAt        *time.Time                         `json:"deliveredAt,omitempty"`
	ReadAt             *time.Time                         `json:"readAt,omitempty"`
	PlayedAt           *time.Time                         `json:"playedAt,omitempty"`
	Provenance         projection_model.MessageProvenance `json:"provenance" binding:"required"`
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
	ConversationID    string     `json:"conversationId,omitempty"`
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

func (r *ChatMessageReader) EnableCanonicalConversations(readiness func(string) (bool, error)) *ChatMessageReader {
	r.canonicalReady = readiness
	return r
}

func (r *ChatMessageReader) ListChats(ctx context.Context, instanceID string, limit int, cursor string) ([]ProjectedChat, *ProjectionReadMeta, error) {
	meta, err := r.readMeta(instanceID, "chats", ChatsProjectionSchemaVersion, ErrChatsProjectionNotReady)
	if err != nil {
		return nil, nil, err
	}
	canonical, err := r.canonicalServing(instanceID)
	if err != nil {
		return nil, nil, err
	}
	if canonical {
		return r.listCanonicalChats(ctx, instanceID, limit, cursor, meta)
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
	meta.Total = &page.Total
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
	canonical, err := r.canonicalServing(instanceID)
	if err != nil {
		return nil, meta, err
	}
	if canonical {
		record, err := r.repository.GetConversation(ctx, instanceID, chatID)
		if err != nil {
			return nil, meta, err
		}
		view := projectedConversationView(record)
		return &view, meta, nil
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
	canonical, err := r.canonicalServing(instanceID)
	if err != nil {
		return nil, meta, err
	}
	if canonical {
		record, err := r.repository.GetConversation(ctx, instanceID, chatID)
		if err != nil {
			return nil, meta, err
		}
		conversationID := record.Conversation.ConversationID
		decoded, err := decodeConversationMessageCursor(cursor, conversationID)
		if err != nil {
			return nil, nil, err
		}
		page, err := r.repository.ListConversationMessages(ctx, instanceID, conversationID, limit, decoded)
		if err != nil {
			return nil, nil, err
		}
		items := make([]ProjectedMessage, len(page.Items))
		for index := range page.Items {
			items[index] = projectedMessageView(&page.Items[index], r.retention, true)
		}
		if page.NextCursor != nil {
			at := page.NextCursor.ProviderTimestamp.UTC()
			meta.NextCursor, err = encodeProjectionCursor(projectionCursor{Version: 2, Kind: "conversation_messages", ConversationID: conversationID, MessageID: page.NextCursor.MessageID, ProviderTimestamp: &at})
			if err != nil {
				return nil, nil, err
			}
		}
		return items, meta, nil
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
		items[index] = projectedMessageView(&page.Items[index], r.retention, false)
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
	canonical, readyErr := r.canonicalServing(instanceID)
	if readyErr != nil {
		return nil, meta, readyErr
	}
	view := projectedMessageView(message, r.retention, canonical)
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
		DisplayNameSource: chat.DisplayNameSource, DisplayNameUpdatedAt: chat.DisplayNameUpdatedAt,
		LastMessageID: chat.LastMessageID, LastMessageAt: chat.LastMessageAt, LastActivityAt: chat.LastActivityAt,
		UnreadCount: chat.UnreadCount, Archived: chat.Archived, Pinned: chat.Pinned, MutedUntil: chat.MutedUntil,
		DisappearingTimer: chat.DisappearingTimer,
	}
}

func (r *ChatMessageReader) listCanonicalChats(ctx context.Context, instanceID string, limit int, cursor string, meta *ProjectionReadMeta) ([]ProjectedChat, *ProjectionReadMeta, error) {
	decoded, err := decodeConversationCursor(cursor)
	if err != nil {
		return nil, nil, err
	}
	page, err := r.repository.ListConversations(ctx, instanceID, limit, decoded)
	if err != nil {
		return nil, nil, err
	}
	items := make([]ProjectedChat, len(page.Items))
	for index := range page.Items {
		items[index] = projectedConversationView(&page.Items[index])
	}
	if page.NextCursor != nil {
		meta.NextCursor, err = encodeProjectionCursor(projectionCursor{
			Version: 2, Kind: "conversations", ConversationID: page.NextCursor.ConversationID,
			LastActivityAt: page.NextCursor.LastActivityAt,
		})
		if err != nil {
			return nil, nil, err
		}
	}
	meta.Total = &page.Total
	return items, meta, nil
}

func projectedConversationView(record *projection_repository.ConversationRecord) ProjectedChat {
	conversation := &record.Conversation
	aliases := make([]string, 0, len(record.Aliases))
	primaryChatID := ""
	for _, alias := range record.Aliases {
		aliases = append(aliases, alias.ChatID)
		if conversation.AddressingJID != nil && alias.ChatID == *conversation.AddressingJID {
			primaryChatID = alias.ChatID
		}
	}
	if primaryChatID == "" && len(aliases) > 0 {
		primaryChatID = aliases[0]
	}
	conversationID := conversation.ConversationID
	return ProjectedChat{
		ChatID: primaryChatID, ConversationID: &conversationID, ChatAliases: aliases, AddressingJID: conversation.AddressingJID,
		ContactID: conversation.ContactID, Type: conversation.Type, DisplayName: conversation.DisplayName,
		DisplayNameSource: conversation.DisplayNameSource, DisplayNameUpdatedAt: conversation.DisplayNameUpdatedAt,
		LastMessageID: conversation.LastMessageID, LastMessageAt: conversation.LastMessageAt, LastActivityAt: conversation.LastActivityAt,
		UnreadCount: conversation.UnreadCount, Archived: conversation.Archived, Pinned: conversation.Pinned,
		MutedUntil: conversation.MutedUntil, DisappearingTimer: conversation.DisappearingTimer,
	}
}

func projectedMessageView(message *projection_model.ProjectedMessage, retention time.Duration, canonicalMode ...bool) ProjectedMessage {
	canonical := len(canonicalMode) == 1 && canonicalMode[0]
	retentionExpiresAt := message.ProviderTimestamp.UTC().Add(retention)
	view := ProjectedMessage{
		MessageID: message.MessageID, ChatID: message.ChatID, SenderJID: message.SenderJID, RecipientJID: message.RecipientJID,
		ParticipantJID: message.ParticipantJID, Direction: message.Direction, MessageType: message.MessageType,
		ContentText: message.ContentText, Caption: message.Caption, ContentSummary: message.ContentSummary,
		QuotedMessageID: message.QuotedMessageID, MediaType: message.MediaType, MediaMIMEType: message.MediaMIMEType,
		MediaFileName: message.MediaFileName, MediaSize: message.MediaSize, MediaDuration: message.MediaDuration,
		MediaWidth: message.MediaWidth, MediaHeight: message.MediaHeight, MediaAssetID: message.MediaAssetID, Status: message.Status,
		ProviderTimestamp: message.ProviderTimestamp, SentAt: message.SentAt, DeliveredAt: message.DeliveredAt,
		ReadAt: message.ReadAt, PlayedAt: message.PlayedAt, Provenance: message.Provenance,
		HistorySyncID: message.HistorySyncID, RetentionExpiresAt: &retentionExpiresAt,
	}
	if canonical {
		view.ConversationID = message.ConversationID
	}
	return view
}

func (r *ChatMessageReader) canonicalServing(instanceID string) (bool, error) {
	if r == nil || r.canonicalReady == nil {
		return false, nil
	}
	return r.canonicalReady(instanceID)
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

func decodeConversationCursor(value string) (*projection_repository.ConversationCursor, error) {
	if value == "" {
		return nil, nil
	}
	cursor, err := decodeProjectionCursorVersion(value, "conversations", 2)
	if err != nil || cursor.ConversationID == "" {
		return nil, ErrInvalidProjectionCursor
	}
	return &projection_repository.ConversationCursor{ConversationID: cursor.ConversationID, LastActivityAt: cursor.LastActivityAt}, nil
}

func decodeConversationMessageCursor(value, conversationID string) (*projection_repository.MessageCursor, error) {
	if value == "" {
		return nil, nil
	}
	cursor, err := decodeProjectionCursorVersion(value, "conversation_messages", 2)
	if err != nil || cursor.ConversationID != conversationID || cursor.MessageID == "" || cursor.ProviderTimestamp == nil || cursor.ProviderTimestamp.IsZero() {
		return nil, ErrInvalidProjectionCursor
	}
	return &projection_repository.MessageCursor{MessageID: cursor.MessageID, ProviderTimestamp: cursor.ProviderTimestamp.UTC()}, nil
}

func decodeProjectionCursor(value, kind string) (*projectionCursor, error) {
	return decodeProjectionCursorVersion(value, kind, projectionCursorVersion)
}

func decodeProjectionCursorVersion(value, kind string, version int) (*projectionCursor, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, ErrInvalidProjectionCursor
	}
	var cursor projectionCursor
	if err := json.Unmarshal(decoded, &cursor); err != nil || cursor.Version != version || cursor.Kind != kind {
		return nil, ErrInvalidProjectionCursor
	}
	return &cursor, nil
}
