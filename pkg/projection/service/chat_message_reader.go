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

type chatMessageReadRepository interface {
	GetMessage(context.Context, string, string) (*projection_model.ProjectedMessage, error)
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
	phoneNumbers   *PhoneNumberResolver
}

type ProjectedMessageReceipt struct {
	MessageID            string    `json:"messageId"`
	RecipientJID         string    `json:"recipientJid"`
	RecipientPhoneNumber *string   `json:"recipientPhoneNumber,omitempty"`
	ReceiptType          string    `json:"receiptType"`
	ReceiptAt            time.Time `json:"receiptAt"`
}

func (r *ChatMessageReader) WithPhoneNumberResolver(resolver *PhoneNumberResolver) *ChatMessageReader {
	r.phoneNumbers = resolver
	return r
}

type projectionCursor struct {
	Version           int        `json:"v"`
	Kind              string     `json:"kind"`
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
	identities := make([]string, len(items))
	for index := range items {
		identities[index] = items[index].RecipientJID
	}
	phones := r.phoneNumbers.Resolve(ctx, instanceID, identities)
	for index := range items {
		items[index].RecipientPhoneNumber = stringValuePointer(phones[items[index].RecipientJID])
	}
	return items, meta, nil
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
