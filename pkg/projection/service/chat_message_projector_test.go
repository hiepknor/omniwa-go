package projection_service

import (
	"context"
	"testing"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
	waE2E "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

type captureChatMessageWrites struct {
	chats        []*projection_model.Chat
	chatAspects  [][]projection_repository.ChatAspect
	messages     []*projection_model.ProjectedMessage
	messageParts [][]projection_repository.MessageAspect
	receipts     []*projection_model.MessageReceipt
}

func (c *captureChatMessageWrites) ApplyChat(_ context.Context, chat *projection_model.Chat, aspects ...projection_repository.ChatAspect) (bool, error) {
	copy := *chat
	c.chats = append(c.chats, &copy)
	c.chatAspects = append(c.chatAspects, append([]projection_repository.ChatAspect(nil), aspects...))
	return true, nil
}

func (c *captureChatMessageWrites) ApplyMessage(_ context.Context, message *projection_model.ProjectedMessage, aspects ...projection_repository.MessageAspect) (bool, error) {
	copy := *message
	c.messages = append(c.messages, &copy)
	c.messageParts = append(c.messageParts, append([]projection_repository.MessageAspect(nil), aspects...))
	return true, nil
}

func (c *captureChatMessageWrites) ApplyReceipt(_ context.Context, receipt *projection_model.MessageReceipt) (bool, error) {
	copy := *receipt
	c.receipts = append(c.receipts, &copy)
	return true, nil
}

type projectionStateRecord struct {
	resource string
	version  int64
}

type captureChatMessageState struct{ records []projectionStateRecord }

func (c *captureChatMessageState) RecordEvent(_ string, resource string, version int64, _ time.Time) error {
	c.records = append(c.records, projectionStateRecord{resource: resource, version: version})
	return nil
}

func TestChatMessageProjectorAppliesMessageAndBothResourceStates(t *testing.T) {
	occurredAt := time.Unix(700, 0).UTC()
	raw := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: types.NewJID("15550001", types.DefaultUserServer), Sender: types.NewJID("self", types.DefaultUserServer), IsFromMe: true},
			ID:            "message-700", Type: "text", Timestamp: occurredAt,
		},
		Message: &waE2E.Message{Conversation: proto.String("Hello")},
	}
	event, _, err := NormalizeChatMessageEvent("instance-a", raw)
	if err != nil {
		t.Fatal(err)
	}
	writes, state := &captureChatMessageWrites{}, &captureChatMessageState{}
	retention := 24 * time.Hour
	if err := NewChatMessageProjector(writes, state, retention).Handle(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if len(writes.chats) != 1 || writes.chats[0].LastMessageID == nil || *writes.chats[0].LastMessageID != "message-700" || len(writes.chatAspects[0]) != 2 {
		t.Fatalf("projected chat writes = %#v %#v", writes.chats, writes.chatAspects)
	}
	if len(writes.messages) != 1 || writes.messages[0].Direction != projection_model.MessageDirectionOutgoing ||
		writes.messages[0].ContentText == nil || *writes.messages[0].ContentText != "Hello" || len(writes.messageParts[0]) != 5 ||
		writes.messages[0].RetentionExpiresAt == nil || !writes.messages[0].RetentionExpiresAt.Equal(occurredAt.Add(retention)) {
		t.Fatalf("projected message writes = %#v %#v", writes.messages, writes.messageParts)
	}
	if len(state.records) != 2 || state.records[0].resource != "chats" || state.records[0].version != ChatsProjectionSchemaVersion ||
		state.records[1].resource != messageResource || state.records[1].version != MessagesProjectionSchemaVersion {
		t.Fatalf("projection state records = %#v", state.records)
	}
}

func TestChatMessageProjectorCreatesIdempotentReceiptPlaceholders(t *testing.T) {
	raw := &events.Receipt{
		MessageSource: types.MessageSource{Chat: types.NewJID("group", types.GroupServer), Sender: types.NewJID("reader", types.DefaultUserServer), IsGroup: true},
		MessageIDs:    []types.MessageID{"message-b", "message-a"}, Timestamp: time.Unix(800, 0), Type: types.ReceiptTypeRead,
	}
	event, _, err := NormalizeChatMessageEvent("instance-a", raw)
	if err != nil {
		t.Fatal(err)
	}
	writes, state := &captureChatMessageWrites{}, &captureChatMessageState{}
	if err := NewChatMessageProjector(writes, state).Handle(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if len(writes.chats) != 1 || len(writes.messages) != 2 || len(writes.receipts) != 2 || len(state.records) != 1 {
		t.Fatalf("receipt projection counts: chats=%d messages=%d receipts=%d states=%d", len(writes.chats), len(writes.messages), len(writes.receipts), len(state.records))
	}
	for index := range writes.messages {
		if !writes.messages[index].SourceOccurredAt.Equal(time.Unix(0, 0)) || writes.messages[index].MessageType != "unknown" ||
			writes.receipts[index].MessageID != writes.messages[index].MessageID || writes.receipts[index].ReceiptType != "read" ||
			len(writes.messages[index].SourceEventKey) != 64 || len(writes.receipts[index].SourceEventKey) != 64 ||
			writes.messages[index].RetentionExpiresAt == nil || !writes.messages[index].RetentionExpiresAt.Equal(time.Unix(800, 0).Add(DefaultMessageRetention)) {
			t.Fatalf("receipt placeholder %d = %#v receipt=%#v", index, writes.messages[index], writes.receipts[index])
		}
	}
}
