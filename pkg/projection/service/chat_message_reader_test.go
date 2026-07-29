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
	chatPage           *projection_repository.ChatPage
	messagePage        *projection_repository.MessagePage
	chat               *projection_model.Chat
	message            *projection_model.ProjectedMessage
	receipts           []projection_model.MessageReceipt
	chatCursor         *projection_repository.ChatCursor
	messageCursor      *projection_repository.MessageCursor
	messageChatID      string
	getMessageErr      error
	conversationPage   *projection_repository.ConversationPage
	conversation       *projection_repository.ConversationRecord
	conversationCursor *projection_repository.ConversationCursor
	conversationRef    string
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
func (s *chatMessageReadStub) GetConversation(_ context.Context, _, conversationRef string) (*projection_repository.ConversationRecord, error) {
	s.conversationRef = conversationRef
	if s.conversation == nil {
		return nil, gorm.ErrRecordNotFound
	}
	return s.conversation, nil
}
func (s *chatMessageReadStub) ListConversations(_ context.Context, _ string, _ int, cursor *projection_repository.ConversationCursor) (*projection_repository.ConversationPage, error) {
	s.conversationCursor = cursor
	return s.conversationPage, nil
}
func (s *chatMessageReadStub) ListConversationMessages(_ context.Context, _, conversationID string, _ int, cursor *projection_repository.MessageCursor) (*projection_repository.MessagePage, error) {
	s.messageChatID, s.messageCursor = conversationID, cursor
	return s.messagePage, nil
}

type chatMessageReadState struct {
	states map[string]*projection_model.State
}

func (s *chatMessageReadState) GetServingState(_ string, resource string) (*projection_model.State, error) {
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
			Total:      42,
		},
		messagePage: &projection_repository.MessagePage{
			Items:      []projection_model.ProjectedMessage{{MessageID: "message-a"}},
			NextCursor: &projection_repository.MessageCursor{MessageID: "message-a", ProviderTimestamp: messageAt},
		},
	}
	reader := NewChatMessageReader(repository, readyChatMessageState("chats", messageResource))

	_, chatMeta, err := reader.ListChats(context.Background(), "instance-a", 10, "")
	if err != nil || chatMeta.NextCursor == "" || chatMeta.Total == nil || *chatMeta.Total != 42 {
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

func TestChatMessageReaderServesCanonicalConversationsAndRejectsLegacyCursors(t *testing.T) {
	activityAt := time.Unix(900, 0).UTC()
	conversationID := "43fa28fa-5412-5490-9879-f847dcfd1120"
	contactID := "845c98ac-89b4-46be-9b83-1120c812cec3"
	addressingJID := "84977450514@s.whatsapp.net"
	record := projection_repository.ConversationRecord{
		Conversation: projection_model.Conversation{
			ConversationID: conversationID, ContactID: &contactID, Type: projection_model.ChatTypeDirect,
			AddressingJID: &addressingJID, LastActivityAt: &activityAt, UnreadCount: 1, UnreadAuthoritative: true,
		},
		Aliases: []projection_model.ChatAlias{
			{ChatID: "36232981651679@lid", ConversationID: conversationID},
			{ChatID: addressingJID, ConversationID: conversationID},
		},
	}
	repository := &chatMessageReadStub{
		conversation: &record,
		conversationPage: &projection_repository.ConversationPage{
			Items: []projection_repository.ConversationRecord{record}, Total: 1,
			NextCursor: &projection_repository.ConversationCursor{ConversationID: conversationID, LastActivityAt: &activityAt},
		},
		messagePage: &projection_repository.MessagePage{Items: []projection_model.ProjectedMessage{{
			MessageID: "message-a", ChatID: "36232981651679@lid", ConversationID: &conversationID,
			Direction: projection_model.MessageDirectionIncoming, MessageType: "text", ProviderTimestamp: activityAt,
			Provenance: projection_model.MessageProvenanceHistorySync,
		}}},
	}
	reader := NewChatMessageReader(repository, readyChatMessageState("chats", messageResource)).
		EnableCanonicalConversations(func(string) (bool, error) { return true, nil })

	items, meta, err := reader.ListChats(context.Background(), "instance-a", 1, "")
	if err != nil || len(items) != 1 || items[0].ConversationID == nil || *items[0].ConversationID != conversationID ||
		items[0].ChatID != addressingJID || len(items[0].ChatAliases) != 2 || meta.Total == nil || *meta.Total != 1 {
		t.Fatalf("canonical chats = %#v meta=%#v err=%v", items, meta, err)
	}
	if _, _, err := reader.ListChats(context.Background(), "instance-a", 1, meta.NextCursor); err != nil {
		t.Fatal(err)
	}
	if repository.conversationCursor == nil || repository.conversationCursor.ConversationID != conversationID {
		t.Fatalf("canonical cursor = %#v", repository.conversationCursor)
	}
	legacyCursor, err := encodeProjectionCursor(projectionCursor{Version: 1, Kind: "chats", ChatID: addressingJID})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := reader.ListChats(context.Background(), "instance-a", 1, legacyCursor); !errors.Is(err, ErrInvalidProjectionCursor) {
		t.Fatalf("legacy cursor in canonical mode = %v", err)
	}
	messages, _, err := reader.ListMessages(context.Background(), "instance-a", "36232981651679@lid", 10, "")
	if err != nil || len(messages) != 1 || messages[0].ConversationID == nil || *messages[0].ConversationID != conversationID || repository.messageChatID != conversationID {
		t.Fatalf("canonical messages = %#v scope=%q err=%v", messages, repository.messageChatID, err)
	}
}

func TestConversationContractSharesCanonicalReadsAndRequiresCanonicalIdentity(t *testing.T) {
	activityAt := time.Unix(900, 0).UTC()
	conversationID := "43fa28fa-5412-5490-9879-f847dcfd1120"
	addressingJID := "84977450514@s.whatsapp.net"
	record := projection_repository.ConversationRecord{
		Conversation: projection_model.Conversation{
			ConversationID: conversationID, Type: projection_model.ChatTypeDirect, AddressingJID: &addressingJID,
			LastActivityAt: &activityAt, UnreadCount: 2, UnreadAuthoritative: true,
		},
		Aliases: []projection_model.ChatAlias{
			{ChatID: "36232981651679@lid", ConversationID: conversationID},
			{ChatID: addressingJID, ConversationID: conversationID},
		},
	}
	repository := &chatMessageReadStub{
		conversation: &record,
		conversationPage: &projection_repository.ConversationPage{
			Items: []projection_repository.ConversationRecord{record}, Total: 1,
		},
		messagePage: &projection_repository.MessagePage{Items: []projection_model.ProjectedMessage{{
			MessageID: "message-a", ChatID: "36232981651679@lid", ConversationID: &conversationID,
			Direction: projection_model.MessageDirectionIncoming, MessageType: "text", ProviderTimestamp: activityAt,
			Provenance: projection_model.MessageProvenanceHistorySync,
		}}},
	}
	reader := NewChatMessageReader(repository, readyChatMessageState("chats", messageResource)).
		EnableCanonicalConversations(func(string) (bool, error) { return true, nil })

	legacy, legacyMeta, err := reader.ListChats(context.Background(), "instance-a", 10, "")
	if err != nil {
		t.Fatal(err)
	}
	conversations, conversationMeta, err := reader.ListConversations(context.Background(), "instance-a", 10, "")
	if err != nil || len(conversations) != 1 || conversations[0].ConversationID != conversationID ||
		conversations[0].Type != ConversationTypeDirect || conversations[0].UnreadCount != 2 ||
		conversationMeta.Total == nil || legacyMeta.Total == nil || *conversationMeta.Total != *legacyMeta.Total {
		t.Fatalf("conversation contract = %#v meta=%#v legacy=%#v err=%v", conversations, conversationMeta, legacy, err)
	}
	if len(legacy) != 1 || legacy[0].ConversationID == nil || *legacy[0].ConversationID != conversations[0].ConversationID {
		t.Fatalf("legacy and canonical views diverged: legacy=%#v canonical=%#v", legacy, conversations)
	}

	absorbedProviderID := "36232981651679@lid"
	conversation, _, err := reader.GetConversation(context.Background(), "instance-a", absorbedProviderID)
	if err != nil || conversation.ConversationID != conversationID || repository.conversationRef != absorbedProviderID {
		t.Fatalf("absorbed provider identity resolution = %#v ref=%q err=%v", conversation, repository.conversationRef, err)
	}
	messages, _, err := reader.ListConversationMessages(context.Background(), "instance-a", absorbedProviderID, 10, "")
	if err != nil || len(messages) != 1 || messages[0].ConversationID != conversationID ||
		messages[0].ProviderChatID != absorbedProviderID || repository.messageChatID != conversationID {
		t.Fatalf("canonical conversation messages = %#v scope=%q err=%v", messages, repository.messageChatID, err)
	}

	value, err := json.Marshal(conversations[0])
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(value, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"conversationId", "type", "unreadCount"} {
		if _, ok := decoded[required]; !ok {
			t.Fatalf("canonical conversation missing %q: %s", required, value)
		}
	}
	for _, forbidden := range []string{"chatId", "chatAliases"} {
		if _, ok := decoded[forbidden]; ok {
			t.Fatalf("canonical conversation leaked legacy identity %q: %s", forbidden, value)
		}
	}
	if decoded["conversationId"] == decoded["addressingJid"] {
		t.Fatalf("addressing JID became entity identity: %s", value)
	}
}

func TestConversationContractFailsClosedAndScopesCursors(t *testing.T) {
	conversationA := "43fa28fa-5412-5490-9879-f847dcfd1120"
	conversationB := "6fe91b47-8b98-5b03-aedd-66cfc43a120e"
	at := time.Unix(900, 0).UTC()
	repository := &chatMessageReadStub{
		chatPage:         &projection_repository.ChatPage{},
		conversation:     &projection_repository.ConversationRecord{Conversation: projection_model.Conversation{ConversationID: conversationA}},
		conversationPage: &projection_repository.ConversationPage{},
		messagePage: &projection_repository.MessagePage{NextCursor: &projection_repository.MessageCursor{
			MessageID: "message-a", ProviderTimestamp: at,
		}},
	}
	legacyOnly := NewChatMessageReader(repository, readyChatMessageState("chats", messageResource)).
		EnableCanonicalConversations(func(string) (bool, error) { return false, nil })
	if _, _, err := legacyOnly.ListConversations(context.Background(), "instance-a", 10, ""); !errors.Is(err, ErrChatsProjectionNotReady) {
		t.Fatalf("canonical list without capability = %v", err)
	}
	if _, _, err := legacyOnly.ListConversationMessages(context.Background(), "instance-a", conversationA, 10, ""); !errors.Is(err, ErrMessagesProjectionNotReady) {
		t.Fatalf("canonical messages without capability = %v", err)
	}
	if _, _, err := legacyOnly.ListChats(context.Background(), "instance-a", 10, ""); err != nil {
		t.Fatalf("legacy chat read failed while canonical capability was absent: %v", err)
	}

	reader := NewChatMessageReader(repository, readyChatMessageState("chats", messageResource)).
		EnableCanonicalConversations(func(string) (bool, error) { return true, nil })
	_, meta, err := reader.ListConversationMessages(context.Background(), "instance-a", conversationA, 10, "")
	if err != nil || meta.NextCursor == "" {
		t.Fatalf("canonical cursor = %#v, %v", meta, err)
	}
	repository.conversation = &projection_repository.ConversationRecord{Conversation: projection_model.Conversation{ConversationID: conversationB}}
	if _, _, err := reader.ListConversationMessages(context.Background(), "instance-a", conversationB, 10, meta.NextCursor); !errors.Is(err, ErrInvalidProjectionCursor) {
		t.Fatalf("cross-conversation cursor error = %v", err)
	}
	legacyCursor, err := encodeProjectionCursor(projectionCursor{Version: 1, Kind: "messages", ChatID: "provider@s.whatsapp.net", MessageID: "message-a", ProviderTimestamp: &at})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := reader.ListConversationMessages(context.Background(), "instance-a", conversationB, 10, legacyCursor); !errors.Is(err, ErrInvalidProjectionCursor) {
		t.Fatalf("legacy cursor accepted by conversation contract: %v", err)
	}
}

func TestConversationMessageDetailIsCanonicalAndConversationScoped(t *testing.T) {
	conversationID := "43fa28fa-5412-5490-9879-f847dcfd1120"
	otherConversationID := "6fe91b47-8b98-5b03-aedd-66cfc43a120e"
	providerChatID := "36232981651679@lid"
	providerTimestamp := time.Unix(900, 0).UTC()
	repository := &chatMessageReadStub{
		conversation: &projection_repository.ConversationRecord{Conversation: projection_model.Conversation{ConversationID: conversationID}},
		message: &projection_model.ProjectedMessage{
			MessageID: "message-a", ChatID: providerChatID, ConversationID: &conversationID,
			Direction: projection_model.MessageDirectionIncoming, MessageType: "text", ProviderTimestamp: providerTimestamp,
			Provenance: projection_model.MessageProvenanceLive,
		},
	}
	reader := NewChatMessageReader(repository, readyChatMessageState(messageResource)).
		EnableCanonicalConversations(func(string) (bool, error) { return true, nil })

	message, _, err := reader.GetConversationMessage(context.Background(), "instance-a", providerChatID, "message-a")
	if err != nil || message.ConversationID != conversationID || message.ProviderChatID != providerChatID || repository.conversationRef != providerChatID {
		t.Fatalf("canonical message detail = %#v ref=%q err=%v", message, repository.conversationRef, err)
	}
	value, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(value, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, exists := decoded["chatId"]; exists {
		t.Fatalf("canonical detail exposed legacy identity: %s", value)
	}

	repository.conversation = &projection_repository.ConversationRecord{Conversation: projection_model.Conversation{ConversationID: otherConversationID}}
	if _, _, err := reader.GetConversationMessage(context.Background(), "instance-a", otherConversationID, "message-a"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("cross-conversation message detail error = %v", err)
	}

	legacyOnly := NewChatMessageReader(repository, readyChatMessageState(messageResource)).
		EnableCanonicalConversations(func(string) (bool, error) { return false, nil })
	if _, _, err := legacyOnly.GetConversationMessage(context.Background(), "instance-a", otherConversationID, "message-a"); !errors.Is(err, ErrMessagesProjectionNotReady) {
		t.Fatalf("message detail without canonical readiness = %v", err)
	}
}

func TestConversationTypePreservesProviderClassifications(t *testing.T) {
	for providerType, canonicalType := range map[projection_model.ChatType]ConversationType{
		projection_model.ChatTypeDirect: ConversationTypeDirect, projection_model.ChatTypeGroup: ConversationTypeGroup,
		projection_model.ChatTypeNewsletter: ConversationTypeNewsletter, projection_model.ChatTypeBroadcast: ConversationTypeBroadcast,
		projection_model.ChatTypeUnknown: ConversationTypeUnknown,
	} {
		if got := conversationType(providerType); got != canonicalType {
			t.Fatalf("conversationType(%q)=%q want=%q", providerType, got, canonicalType)
		}
	}
}
