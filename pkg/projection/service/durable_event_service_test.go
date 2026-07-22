package projection_service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	waE2E "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

type durableEventWriterStub struct {
	events []*projection_model.DurableEvent
}

func (s *durableEventWriterStub) Append(_ context.Context, event *projection_model.DurableEvent) error {
	copy := *event
	copy.Summary = append(json.RawMessage(nil), event.Summary...)
	s.events = append(s.events, &copy)
	return nil
}

func TestDurableEventServicePersistsSafeMessageSummary(t *testing.T) {
	writer := &durableEventWriterStub{}
	service := NewDurableEventService(writer, 24*time.Hour)
	service.now = func() time.Time { return time.Unix(1_000, 0).UTC() }
	messageAt := time.Unix(900, 0).UTC()
	raw := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: types.NewJID("15550001", types.DefaultUserServer), IsFromMe: false},
			ID:            "message-a", Type: "text", Timestamp: messageAt,
		},
		Message: &waE2E.Message{Conversation: proto.String("secret message content")},
	}
	event, err := service.Record(context.Background(), "instance-a", "Message", raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(writer.events) != 1 || event.ID == "" || !event.OccurredAt.Equal(messageAt) || !event.ExpiresAt.Equal(time.Unix(1_000, 0).Add(24*time.Hour)) {
		t.Fatalf("durable event = %#v", event)
	}
	encoded := string(event.Summary)
	if strings.Contains(encoded, "secret message content") || strings.Contains(encoded, "instance-a") {
		t.Fatalf("durable summary leaked content or instance identity: %s", encoded)
	}
	var summary durableEventSummary
	if err := json.Unmarshal(event.Summary, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.MessageID != "message-a" || summary.ChatID != "15550001@s.whatsapp.net" || summary.Direction != "incoming" || summary.MessageType != "text" {
		t.Fatalf("message summary = %#v", summary)
	}
}

func TestDurableEventServiceBoundsReceiptIdentifiersAndUsesSafeDefault(t *testing.T) {
	messageIDs := make([]types.MessageID, 120)
	for index := range messageIDs {
		messageIDs[index] = types.MessageID("message")
	}
	_, receiptSummary := normalizeDurableEvent(&events.Receipt{
		MessageSource: types.MessageSource{Chat: types.NewJID("group", types.GroupServer)},
		MessageIDs:    messageIDs, Timestamp: time.Unix(500, 0), Type: types.ReceiptTypeRead,
	}, time.Unix(600, 0))
	if receiptSummary.Count != 120 || len(receiptSummary.MessageIDs) != 100 {
		t.Fatalf("receipt summary = %#v", receiptSummary)
	}
	_, fallback := normalizeDurableEvent(struct{ Token string }{Token: "must-not-persist"}, time.Unix(600, 0))
	value, err := json.Marshal(fallback)
	if err != nil {
		t.Fatal(err)
	}
	if string(value) != "{}" {
		t.Fatalf("unknown event summary = %s", value)
	}
}

func TestDurableEventServiceNormalizesOutboundMessageInfoWithoutContent(t *testing.T) {
	occurredAt := time.Unix(700, 0).UTC()
	_, summary := normalizeDurableEvent(types.MessageInfo{
		MessageSource: types.MessageSource{Chat: types.NewJID("15550002", types.DefaultUserServer), IsFromMe: true},
		ID:            "outbound-message", Type: "image", Timestamp: occurredAt,
	}, time.Unix(800, 0))
	if summary.MessageID != "outbound-message" || summary.ChatID != "15550002@s.whatsapp.net" || summary.Direction != "outgoing" || summary.MessageType != "image" {
		t.Fatalf("outbound message summary = %#v", summary)
	}
}
