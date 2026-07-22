package projection_service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
	"gorm.io/gorm"
)

type chatMessageReadStub struct {
	chatPage      *projection_repository.ChatPage
	messagePage   *projection_repository.MessagePage
	chat          *projection_model.Chat
	message       *projection_model.ProjectedMessage
	receipts      []projection_model.MessageReceipt
	chatCursor    *projection_repository.ChatCursor
	messageCursor *projection_repository.MessageCursor
	messageChatID string
	getMessageErr error
}

func (s *chatMessageReadStub) GetChat(context.Context, string, string) (*projection_model.Chat, error) {
	if s.chat == nil {
		return nil, gorm.ErrRecordNotFound
	}
	return s.chat, nil
}
func (s *chatMessageReadStub) ListChats(_ context.Context, _ string, _ int, cursor *projection_repository.ChatCursor) (*projection_repository.ChatPage, error) {
	s.chatCursor = cursor
	return s.chatPage, nil
}
func (s *chatMessageReadStub) GetMessage(context.Context, string, string) (*projection_model.ProjectedMessage, error) {
	if s.getMessageErr != nil {
		return nil, s.getMessageErr
	}
	if s.message == nil {
		return nil, gorm.ErrRecordNotFound
	}
	return s.message, nil
}
func (s *chatMessageReadStub) ListMessages(_ context.Context, _, chatID string, _ int, cursor *projection_repository.MessageCursor) (*projection_repository.MessagePage, error) {
	s.messageChatID, s.messageCursor = chatID, cursor
	return s.messagePage, nil
}
func (s *chatMessageReadStub) ListReceipts(context.Context, string, string) ([]projection_model.MessageReceipt, error) {
	return s.receipts, nil
}

type chatMessageReadState struct {
	states map[string]*projection_model.State
}

func (s *chatMessageReadState) Get(_ string, resource string) (*projection_model.State, error) {
	state := s.states[resource]
	if state == nil {
		return nil, gorm.ErrRecordNotFound
	}
	copy := *state
	return &copy, nil
}

func readyChatMessageState(resources ...string) *chatMessageReadState {
	reconciledAt := time.Unix(500, 0).UTC()
	states := make(map[string]*projection_model.State, len(resources))
	for _, resource := range resources {
		version := MessagesProjectionSchemaVersion
		if resource == "chats" {
			version = ChatsProjectionSchemaVersion
		}
		states[resource] = &projection_model.State{Resource: resource, SyncStatus: projection_model.SyncStatusReady, SchemaVersion: version, LastReconciledAt: &reconciledAt}
	}
	return &chatMessageReadState{states: states}
}

func TestChatMessageReaderReturnsStableOpaqueCursors(t *testing.T) {
	chatAt, messageAt := time.Unix(400, 0).UTC(), time.Unix(300, 0).UTC()
	repository := &chatMessageReadStub{
		chatPage: &projection_repository.ChatPage{
			Items:      []projection_model.Chat{{ChatID: "chat-a"}},
			NextCursor: &projection_repository.ChatCursor{ChatID: "chat-a", LastActivityAt: &chatAt},
		},
		messagePage: &projection_repository.MessagePage{
			Items:      []projection_model.ProjectedMessage{{MessageID: "message-a"}},
			NextCursor: &projection_repository.MessageCursor{MessageID: "message-a", ProviderTimestamp: messageAt},
		},
	}
	reader := NewChatMessageReader(repository, readyChatMessageState("chats", messageResource))

	_, chatMeta, err := reader.ListChats(context.Background(), "instance-a", 10, "")
	if err != nil || chatMeta.NextCursor == "" {
		t.Fatalf("ListChats() meta = %#v, %v", chatMeta, err)
	}
	if _, _, err := reader.ListChats(context.Background(), "instance-a", 10, chatMeta.NextCursor); err != nil {
		t.Fatal(err)
	}
	if repository.chatCursor == nil || repository.chatCursor.ChatID != "chat-a" || !repository.chatCursor.LastActivityAt.Equal(chatAt) {
		t.Fatalf("decoded chat cursor = %#v", repository.chatCursor)
	}

	_, messageMeta, err := reader.ListMessages(context.Background(), "instance-a", "chat-a", 10, "")
	if err != nil || messageMeta.NextCursor == "" {
		t.Fatalf("ListMessages() meta = %#v, %v", messageMeta, err)
	}
	if _, _, err := reader.ListMessages(context.Background(), "instance-a", "chat-a", 10, messageMeta.NextCursor); err != nil {
		t.Fatal(err)
	}
	if repository.messageCursor == nil || repository.messageCursor.MessageID != "message-a" || !repository.messageCursor.ProviderTimestamp.Equal(messageAt) {
		t.Fatalf("decoded message cursor = %#v", repository.messageCursor)
	}
	if _, _, err := reader.ListMessages(context.Background(), "instance-a", "chat-b", 10, messageMeta.NextCursor); !errors.Is(err, ErrInvalidProjectionCursor) {
		t.Fatalf("cross-chat cursor error = %v", err)
	}
	if _, _, err := reader.ListChats(context.Background(), "instance-a", 10, messageMeta.NextCursor); !errors.Is(err, ErrInvalidProjectionCursor) {
		t.Fatalf("cross-resource cursor error = %v", err)
	}
}

func TestChatMessageReaderDistinguishesNotReadyEmptyAndMissing(t *testing.T) {
	repository := &chatMessageReadStub{
		chatPage: &projection_repository.ChatPage{}, messagePage: &projection_repository.MessagePage{},
		message: &projection_model.ProjectedMessage{MessageID: "message-a"},
	}
	reader := NewChatMessageReader(repository, readyChatMessageState("chats", messageResource))
	chats, _, err := reader.ListChats(context.Background(), "instance-a", 10, "")
	if err != nil || chats == nil || len(chats) != 0 {
		t.Fatalf("empty chats = %#v, %v", chats, err)
	}
	messages, _, err := reader.ListMessages(context.Background(), "instance-a", "chat-a", 10, "")
	if err != nil || messages == nil || len(messages) != 0 {
		t.Fatalf("empty messages = %#v, %v", messages, err)
	}
	receipts, _, err := reader.ListReceipts(context.Background(), "instance-a", "message-a")
	if err != nil || receipts == nil || len(receipts) != 0 {
		t.Fatalf("empty receipts = %#v, %v", receipts, err)
	}

	notReady := NewChatMessageReader(repository, readyChatMessageState())
	if _, _, err := notReady.ListChats(context.Background(), "instance-a", 10, ""); !errors.Is(err, ErrChatsProjectionNotReady) {
		t.Fatalf("chat readiness error = %v", err)
	}
	if _, _, err := notReady.ListMessages(context.Background(), "instance-a", "chat-a", 10, ""); !errors.Is(err, ErrMessagesProjectionNotReady) {
		t.Fatalf("message readiness error = %v", err)
	}
	repository.getMessageErr = gorm.ErrRecordNotFound
	if _, _, err := reader.ListReceipts(context.Background(), "instance-a", "missing"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("missing receipt parent error = %v", err)
	}
}

func TestProjectedMessageViewDoesNotExposeStorageCoordinationFields(t *testing.T) {
	message := &projection_model.ProjectedMessage{
		InstanceID: "private-instance", MessageID: "message-a", ChatID: "chat-a",
		Direction: projection_model.MessageDirectionIncoming, MessageType: "text",
		ProviderTimestamp: time.Unix(100, 0).UTC(), Provenance: projection_model.MessageProvenanceLive,
		SourceEventKey: "private-event", FieldVersions: json.RawMessage(`{"envelope":{}}`),
	}
	view := projectedMessageView(message, 30*24*time.Hour)
	if view.RetentionExpiresAt == nil || !view.RetentionExpiresAt.Equal(message.ProviderTimestamp.Add(30*24*time.Hour)) {
		t.Fatalf("current-policy retention deadline = %v", view.RetentionExpiresAt)
	}
	value, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(value, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"instanceId", "sourceEventKey", "sourceOccurredAt", "fieldVersions", "createdAt", "updatedAt"} {
		if _, exists := decoded[field]; exists {
			t.Fatalf("public message exposes storage field %q: %s", field, value)
		}
	}
}
