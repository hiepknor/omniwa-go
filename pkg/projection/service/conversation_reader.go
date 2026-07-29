package projection_service

import (
	"context"
	"errors"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
)

// ConversationType is the public canonical conversation classification. The
// persisted provider Chat type remains an implementation detail.
type ConversationType string

const (
	ConversationTypeDirect     ConversationType = "direct"
	ConversationTypeGroup      ConversationType = "group"
	ConversationTypeNewsletter ConversationType = "newsletter"
	ConversationTypeBroadcast  ConversationType = "broadcast"
	ConversationTypeUnknown    ConversationType = "unknown"
)

// ProjectedConversation is the canonical public conversation representation.
// ConversationID is the entity identity; AddressingJID and Aliases are
// provider-addressing metadata and must not be used as entity identifiers.
type ProjectedConversation struct {
	ConversationID       string           `json:"conversationId" binding:"required" format:"uuid"`
	Type                 ConversationType `json:"type" binding:"required" enums:"direct,group,newsletter,broadcast,unknown"`
	UnreadCount          int              `json:"unreadCount" binding:"required"`
	AddressingJID        *string          `json:"addressingJid,omitempty"`
	Aliases              []string         `json:"aliases,omitempty"`
	ContactID            *string          `json:"contactId,omitempty" format:"uuid"`
	DisplayName          *string          `json:"displayName,omitempty"`
	DisplayNameSource    *string          `json:"displayNameSource,omitempty" enums:"full_name,business_name,push_name,first_name,username,provider_chat,group_subject,newsletter_name,broadcast_name"`
	DisplayNameUpdatedAt *time.Time       `json:"displayNameUpdatedAt,omitempty"`
	LastMessageID        *string          `json:"lastMessageId,omitempty"`
	LastMessageAt        *time.Time       `json:"lastMessageAt,omitempty"`
	LastActivityAt       *time.Time       `json:"lastActivityAt,omitempty"`
	Archived             *bool            `json:"archived,omitempty"`
	Pinned               *bool            `json:"pinned,omitempty"`
	MutedUntil           *time.Time       `json:"mutedUntil,omitempty"`
	DisappearingTimer    *uint32          `json:"disappearingTimer,omitempty"`
}

// ProjectedConversationMessage is a message in a canonical conversation.
// ProviderChatID records the provider alias on which the message arrived; it is
// not the conversation identity or cursor scope.
type ProjectedConversationMessage struct {
	MessageID          string                             `json:"messageId" binding:"required"`
	ConversationID     string                             `json:"conversationId" binding:"required" format:"uuid"`
	ProviderChatID     string                             `json:"providerChatId,omitempty"`
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
	MediaAssetID       *string                            `json:"mediaAssetId,omitempty" format:"uuid"`
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

func (r *ChatMessageReader) ListConversations(ctx context.Context, instanceID string, limit int, cursor string) ([]ProjectedConversation, *ProjectionReadMeta, error) {
	meta, err := r.canonicalConversationMeta(instanceID, "chats", ChatsProjectionSchemaVersion, ErrChatsProjectionNotReady)
	if err != nil {
		return nil, nil, err
	}
	records, meta, err := r.listCanonicalConversationRecords(ctx, instanceID, limit, cursor, meta)
	if err != nil {
		return nil, nil, err
	}
	items := make([]ProjectedConversation, len(records))
	for index := range records {
		items[index] = projectedCanonicalConversationView(&records[index])
	}
	return items, meta, nil
}

func (r *ChatMessageReader) GetConversation(ctx context.Context, instanceID, conversationRef string) (*ProjectedConversation, *ProjectionReadMeta, error) {
	if conversationRef == "" {
		return nil, nil, errors.New("conversation reference is required")
	}
	meta, err := r.canonicalConversationMeta(instanceID, "chats", ChatsProjectionSchemaVersion, ErrChatsProjectionNotReady)
	if err != nil {
		return nil, nil, err
	}
	record, err := r.getCanonicalConversation(ctx, instanceID, conversationRef)
	if err != nil {
		return nil, meta, err
	}
	view := projectedCanonicalConversationView(record)
	return &view, meta, nil
}

func (r *ChatMessageReader) ListConversationMessages(ctx context.Context, instanceID, conversationRef string, limit int, cursor string) ([]ProjectedConversationMessage, *ProjectionReadMeta, error) {
	if conversationRef == "" {
		return nil, nil, errors.New("conversation reference is required")
	}
	meta, err := r.canonicalConversationMeta(instanceID, messageResource, MessagesProjectionSchemaVersion, ErrMessagesProjectionNotReady)
	if err != nil {
		return nil, nil, err
	}
	messages, meta, err := r.listCanonicalConversationMessageRecords(ctx, instanceID, conversationRef, limit, cursor, meta)
	if err != nil {
		return nil, nil, err
	}
	items := make([]ProjectedConversationMessage, len(messages))
	for index := range messages {
		items[index], err = projectedCanonicalConversationMessageView(&messages[index], r.retention)
		if err != nil {
			return nil, nil, err
		}
	}
	return items, meta, nil
}

func (r *ChatMessageReader) canonicalConversationMeta(instanceID, resource string, version int64, notReady error) (*ProjectionReadMeta, error) {
	meta, err := r.readMeta(instanceID, resource, version, notReady)
	if err != nil {
		return nil, err
	}
	canonical, err := r.canonicalServing(instanceID)
	if err != nil {
		return nil, err
	}
	if !canonical {
		return nil, notReady
	}
	return meta, nil
}

func (r *ChatMessageReader) getCanonicalConversation(ctx context.Context, instanceID, conversationRef string) (*projection_repository.ConversationRecord, error) {
	return r.repository.GetConversation(ctx, instanceID, conversationRef)
}

func (r *ChatMessageReader) listCanonicalConversationRecords(ctx context.Context, instanceID string, limit int, cursor string, meta *ProjectionReadMeta) ([]projection_repository.ConversationRecord, *ProjectionReadMeta, error) {
	decoded, err := decodeConversationCursor(cursor)
	if err != nil {
		return nil, nil, err
	}
	page, err := r.repository.ListConversations(ctx, instanceID, limit, decoded)
	if err != nil {
		return nil, nil, err
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
	return page.Items, meta, nil
}

func (r *ChatMessageReader) listCanonicalConversationMessageRecords(ctx context.Context, instanceID, conversationRef string, limit int, cursor string, meta *ProjectionReadMeta) ([]projection_model.ProjectedMessage, *ProjectionReadMeta, error) {
	record, err := r.getCanonicalConversation(ctx, instanceID, conversationRef)
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
	if page.NextCursor != nil {
		at := page.NextCursor.ProviderTimestamp.UTC()
		meta.NextCursor, err = encodeProjectionCursor(projectionCursor{
			Version: 2, Kind: "conversation_messages", ConversationID: conversationID,
			MessageID: page.NextCursor.MessageID, ProviderTimestamp: &at,
		})
		if err != nil {
			return nil, nil, err
		}
	}
	return page.Items, meta, nil
}

func projectedCanonicalConversationView(record *projection_repository.ConversationRecord) ProjectedConversation {
	conversation := &record.Conversation
	aliases := make([]string, len(record.Aliases))
	for index := range record.Aliases {
		aliases[index] = record.Aliases[index].ChatID
	}
	return ProjectedConversation{
		ConversationID: conversation.ConversationID, Type: conversationType(conversation.Type), UnreadCount: conversation.UnreadCount,
		AddressingJID: conversation.AddressingJID, Aliases: aliases, ContactID: conversation.ContactID,
		DisplayName: conversation.DisplayName, DisplayNameSource: conversation.DisplayNameSource,
		DisplayNameUpdatedAt: conversation.DisplayNameUpdatedAt, LastMessageID: conversation.LastMessageID,
		LastMessageAt: conversation.LastMessageAt, LastActivityAt: conversation.LastActivityAt,
		Archived: conversation.Archived, Pinned: conversation.Pinned, MutedUntil: conversation.MutedUntil,
		DisappearingTimer: conversation.DisappearingTimer,
	}
}

func projectedCanonicalConversationMessageView(message *projection_model.ProjectedMessage, retention time.Duration) (ProjectedConversationMessage, error) {
	if message == nil || message.ConversationID == nil || *message.ConversationID == "" {
		return ProjectedConversationMessage{}, errors.New("canonical conversation message identity is missing")
	}
	retentionExpiresAt := message.ProviderTimestamp.UTC().Add(retention)
	return ProjectedConversationMessage{
		MessageID: message.MessageID, ConversationID: *message.ConversationID, ProviderChatID: message.ChatID,
		SenderJID: message.SenderJID, RecipientJID: message.RecipientJID, ParticipantJID: message.ParticipantJID,
		Direction: message.Direction, MessageType: message.MessageType, ContentText: message.ContentText,
		Caption: message.Caption, ContentSummary: message.ContentSummary, QuotedMessageID: message.QuotedMessageID,
		MediaType: message.MediaType, MediaMIMEType: message.MediaMIMEType, MediaFileName: message.MediaFileName,
		MediaSize: message.MediaSize, MediaDuration: message.MediaDuration, MediaWidth: message.MediaWidth,
		MediaHeight: message.MediaHeight, MediaAssetID: message.MediaAssetID, Status: message.Status,
		ProviderTimestamp: message.ProviderTimestamp, SentAt: message.SentAt, DeliveredAt: message.DeliveredAt,
		ReadAt: message.ReadAt, PlayedAt: message.PlayedAt, Provenance: message.Provenance,
		HistorySyncID: message.HistorySyncID, RetentionExpiresAt: &retentionExpiresAt,
	}, nil
}

func conversationType(value projection_model.ChatType) ConversationType {
	switch value {
	case projection_model.ChatTypeDirect:
		return ConversationTypeDirect
	case projection_model.ChatTypeGroup:
		return ConversationTypeGroup
	case projection_model.ChatTypeNewsletter:
		return ConversationTypeNewsletter
	case projection_model.ChatTypeBroadcast:
		return ConversationTypeBroadcast
	default:
		return ConversationTypeUnknown
	}
}
