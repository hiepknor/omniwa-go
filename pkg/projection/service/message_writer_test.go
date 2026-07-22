package projection_service

import (
	"context"
	"testing"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	waE2E "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

func TestMessageWriteThroughPersistsConfirmedOutboundMessage(t *testing.T) {
	writes, state := &captureChatMessageWrites{}, &captureChatMessageState{}
	writer := NewMessageWriteThrough(NewChatMessageProjector(writes, state))
	sentAt := time.Unix(1_000, 0).UTC()
	info := types.MessageInfo{
		MessageSource: types.MessageSource{
			Chat: types.NewJID("15550001", types.DefaultUserServer), Sender: types.NewJID("self", types.DefaultUserServer), IsFromMe: true,
		},
		ID: "message-write-through", Type: "ExtendedTextMessage", Timestamp: sentAt,
	}
	message := &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("confirmed")}}
	if err := writer.WriteSent(context.Background(), "instance-a", info, message); err != nil {
		t.Fatal(err)
	}
	if len(writes.messages) != 1 || writes.messages[0].Provenance != projection_model.MessageProvenanceWriteThrough ||
		writes.messages[0].MessageType != "text" || writes.messages[0].ContentText == nil || *writes.messages[0].ContentText != "confirmed" ||
		writes.messages[0].SentAt == nil || !writes.messages[0].SentAt.Equal(sentAt) {
		t.Fatalf("write-through projected message = %#v", writes.messages)
	}
	if len(writes.chats) != 1 || len(state.records) != 2 {
		t.Fatalf("write-through chat/state counts = %d/%d", len(writes.chats), len(state.records))
	}
}

func TestMessageWriteThroughRejectsUnconfirmedIncomingMessage(t *testing.T) {
	writer := NewMessageWriteThrough(NewChatMessageProjector(&captureChatMessageWrites{}, &captureChatMessageState{}))
	err := writer.WriteSent(context.Background(), "instance-a", types.MessageInfo{
		MessageSource: types.MessageSource{Chat: types.NewJID("15550001", types.DefaultUserServer)},
		ID:            "incoming", Type: "text", Timestamp: time.Now(),
	}, &waE2E.Message{Conversation: proto.String("incoming")})
	if err == nil {
		t.Fatal("incoming message was accepted as confirmed outbound write-through")
	}
}
