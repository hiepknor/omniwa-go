package projection_service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	waCommon "go.mau.fi/whatsmeow/proto/waCommon"
	waE2E "go.mau.fi/whatsmeow/proto/waE2E"
	waHistorySync "go.mau.fi/whatsmeow/proto/waHistorySync"
	waWeb "go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

type captureHistoryEvents struct{ events []*projection_model.Event }

func (c *captureHistoryEvents) Ingest(_ context.Context, event *projection_model.Event) (bool, error) {
	copy := *event
	c.events = append(c.events, &copy)
	return true, nil
}

type failHistoryContactEvents struct{ captureHistoryEvents }

func (c *failHistoryContactEvents) Ingest(ctx context.Context, event *projection_model.Event) (bool, error) {
	if event.EventType == "push_name" {
		return false, errors.New("contact inbox unavailable")
	}
	return c.captureHistoryEvents.Ingest(ctx, event)
}

type historyStateStub struct {
	states map[string]*projection_model.State
	failed []string
	stale  []string
}

func newHistoryStateStub() *historyStateStub {
	return &historyStateStub{states: make(map[string]*projection_model.State)}
}

func (s *historyStateStub) Get(_ string, resource string) (*projection_model.State, error) {
	state := s.states[resource]
	if state == nil {
		return nil, gorm.ErrRecordNotFound
	}
	copy := *state
	return &copy, nil
}

func (s *historyStateStub) MarkSyncing(instanceID, resource string, version int64) error {
	s.states[resource] = &projection_model.State{InstanceID: instanceID, Resource: resource, SyncStatus: projection_model.SyncStatusSyncing, SchemaVersion: version}
	return nil
}

func (s *historyStateStub) MarkStale(_ string, resource string, _ int64) error {
	s.stale = append(s.stale, resource)
	return nil
}

func (s *historyStateStub) MarkFailed(_ string, resource string, _ int64) error {
	s.failed = append(s.failed, resource)
	return nil
}

func TestHistorySyncerFansOutNormalizedEventsAndCompletionBarrier(t *testing.T) {
	eventsCapture, state := &captureHistoryEvents{}, newHistoryStateStub()
	syncer := NewHistorySyncer(eventsCapture, state)
	syncer.now = func() time.Time { return time.Unix(900, 0).UTC() }
	raw := testHistorySync(waHistorySync.HistorySync_RECENT, 100)
	if err := syncer.Sync(context.Background(), "instance-a", raw, testHistoryMessageParser); err != nil {
		t.Fatal(err)
	}
	if len(eventsCapture.events) != 3 || eventsCapture.events[0].EventType != "history_chat" ||
		eventsCapture.events[1].EventType != "history_message" || eventsCapture.events[2].EventType != "history_sync_complete" {
		t.Fatalf("history events = %#v", eventsCapture.events)
	}
	if state.states["chats"].SyncStatus != projection_model.SyncStatusSyncing || state.states[messageResource].SyncStatus != projection_model.SyncStatusSyncing {
		t.Fatalf("history sync states = %#v", state.states)
	}
	var messagePayload messageEventPayload
	if err := json.Unmarshal(eventsCapture.events[1].Payload, &messagePayload); err != nil {
		t.Fatal(err)
	}
	if messagePayload.Provenance != projection_model.MessageProvenanceHistorySync || messagePayload.HistorySyncID == nil ||
		messagePayload.Status == nil || *messagePayload.Status != "delivered" || messagePayload.ContentText == nil || *messagePayload.ContentText != "historical" {
		t.Fatalf("history message payload = %#v", messagePayload)
	}
	var completion messageEventPayload
	if err := json.Unmarshal(eventsCapture.events[2].Payload, &completion); err != nil {
		t.Fatal(err)
	}
	if !completion.ChatsReady || !completion.MessagesReady || completion.CompletedAt == nil || !completion.CompletedAt.Equal(time.Unix(900, 0)) {
		t.Fatalf("history completion payload = %#v", completion)
	}
}

func TestHistorySyncerUsesConservativeReadinessAndMarksFailure(t *testing.T) {
	bootstrapEvents, state := &captureHistoryEvents{}, newHistoryStateStub()
	syncer := NewHistorySyncer(bootstrapEvents, state)
	if err := syncer.Sync(context.Background(), "instance-a", testHistorySync(waHistorySync.HistorySync_INITIAL_BOOTSTRAP, 100), testHistoryMessageParser); err != nil {
		t.Fatal(err)
	}
	var completion messageEventPayload
	if err := json.Unmarshal(bootstrapEvents.events[len(bootstrapEvents.events)-1].Payload, &completion); err != nil {
		t.Fatal(err)
	}
	if !completion.ChatsReady || completion.MessagesReady || state.states[messageResource] != nil {
		t.Fatalf("bootstrap readiness = %#v states=%#v", completion, state.states)
	}
	var bootstrapChat messageEventPayload
	if err := json.Unmarshal(bootstrapEvents.events[0].Payload, &bootstrapChat); err != nil {
		t.Fatal(err)
	}
	if bootstrapChat.HistorySyncID != nil {
		t.Fatalf("bootstrap chat unexpectedly carries authoritative unread snapshot: %#v", bootstrapChat)
	}

	failedState := newHistoryStateStub()
	err := NewHistorySyncer(&captureHistoryEvents{}, failedState).Sync(context.Background(), "instance-a", testHistorySync(waHistorySync.HistorySync_RECENT, 50), func(types.JID, *waWeb.WebMessageInfo) (*events.Message, error) {
		return nil, errors.New("sensitive provider parse detail")
	})
	if err == nil || len(failedState.failed) != 2 {
		t.Fatalf("failed history sync = %v failed=%#v", err, failedState.failed)
	}
	details := DescribeHistorySyncFailure(err)
	if details.Stage != "message_parse" || details.Code != "history_sync_message_parse_failed" ||
		details.ConversationIndex != 0 || details.MessageIndex != 0 {
		t.Fatalf("failure details = %#v", details)
	}
	if strings.Contains(err.Error(), "sensitive provider parse detail") {
		t.Fatalf("history sync error leaked provider detail: %v", err)
	}
}

func TestHistorySyncerDoesNotEmitCompletionForOnDemandChunks(t *testing.T) {
	eventsCapture := &captureHistoryEvents{}
	if err := NewHistorySyncer(eventsCapture, newHistoryStateStub()).Sync(
		context.Background(), "instance-a", testHistorySync(waHistorySync.HistorySync_ON_DEMAND, 100), testHistoryMessageParser,
	); err != nil {
		t.Fatal(err)
	}
	for _, event := range eventsCapture.events {
		if event.EventType == "history_sync_complete" {
			t.Fatal("on-demand history emitted a readiness completion")
		}
	}
}

func TestHistorySyncerSkipsMetadataOnlyMessageStubsAndCompletes(t *testing.T) {
	raw := testHistorySync(waHistorySync.HistorySync_RECENT, 100)
	raw.Data.Conversations[0].Messages = append([]*waHistorySync.HistorySyncMsg{{
		Message: &waWeb.WebMessageInfo{MessageStubType: waWeb.WebMessageInfo_UNKNOWN.Enum()},
	}}, raw.Data.Conversations[0].Messages...)

	eventsCapture := &captureHistoryEvents{}
	parserCalls := 0
	err := NewHistorySyncer(eventsCapture, newHistoryStateStub()).Sync(
		context.Background(), "instance-a", raw,
		func(chat types.JID, source *waWeb.WebMessageInfo) (*events.Message, error) {
			parserCalls++
			return testHistoryMessageParser(chat, source)
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if parserCalls != 1 {
		t.Fatalf("parser calls = %d, want only the payload-bearing message", parserCalls)
	}
	if len(eventsCapture.events) != 3 || eventsCapture.events[0].EventType != "history_chat" ||
		eventsCapture.events[1].EventType != "history_message" || eventsCapture.events[2].EventType != "history_sync_complete" {
		t.Fatalf("history events = %#v", eventsCapture.events)
	}
}

func TestHistorySyncerDeduplicatesAuthoritativeMessagePushNames(t *testing.T) {
	raw := testHistorySync(waHistorySync.HistorySync_RECENT, 100)
	first := raw.Data.Conversations[0].Messages[0].Message
	first.PushName = proto.String("Older Name")
	first.MessageTimestamp = proto.Uint64(700)
	newer := proto.Clone(first).(*waWeb.WebMessageInfo)
	newer.Key.ID = proto.String("history-message-newer")
	newer.MessageTimestamp = proto.Uint64(900)
	newer.PushName = proto.String("  Newer Name  ")
	raw.Data.Conversations[0].Messages = append(raw.Data.Conversations[0].Messages, &waHistorySync.HistorySyncMsg{Message: newer})

	eventsCapture := &captureHistoryEvents{}
	if err := NewHistorySyncer(eventsCapture, newHistoryStateStub()).Sync(context.Background(), "instance-a", raw, testHistoryMessageParser); err != nil {
		t.Fatal(err)
	}
	var pushEvents []*projection_model.Event
	for _, event := range eventsCapture.events {
		if event.EventType == "push_name" {
			pushEvents = append(pushEvents, event)
		}
	}
	if len(pushEvents) != 1 {
		t.Fatalf("push-name events = %d, want one newest event", len(pushEvents))
	}
	var payload contactEventPayload
	if err := json.Unmarshal(pushEvents[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.PushName == nil || *payload.PushName != "Newer Name" || !pushEvents[0].OccurredAt.Equal(time.Unix(900, 0)) {
		t.Fatalf("push-name event = %#v payload=%#v", pushEvents[0], payload)
	}
	if pushEvents[0].InstanceID != "instance-a" || payload.PreferredJID != "sender@s.whatsapp.net" {
		t.Fatalf("push-name identity escaped instance/sender scope: %#v payload=%#v", pushEvents[0], payload)
	}
}

func TestHistorySyncerSkipsUnusableOrSelfPushNames(t *testing.T) {
	tests := []struct {
		name     string
		pushName string
		fromMe   bool
	}{
		{name: "missing", pushName: ""},
		{name: "placeholder", pushName: "-"},
		{name: "oversized", pushName: strings.Repeat("x", maxHistoryPushNameBytes+1)},
		{name: "self", pushName: "Own Name", fromMe: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := testHistorySync(waHistorySync.HistorySync_RECENT, 100)
			source := raw.Data.Conversations[0].Messages[0].Message
			source.PushName = proto.String(test.pushName)
			source.Key.FromMe = proto.Bool(test.fromMe)
			eventsCapture := &captureHistoryEvents{}
			if err := NewHistorySyncer(eventsCapture, newHistoryStateStub()).Sync(context.Background(), "instance-a", raw, testHistoryMessageParser); err != nil {
				t.Fatal(err)
			}
			for _, event := range eventsCapture.events {
				if event.EventType == "push_name" {
					t.Fatalf("unusable push name produced contact event: %#v", event)
				}
			}
		})
	}
}

func TestHistorySyncerFailsClosedWhenPushNameIsNotDurable(t *testing.T) {
	raw := testHistorySync(waHistorySync.HistorySync_RECENT, 100)
	raw.Data.Conversations[0].Messages[0].Message.PushName = proto.String("Provider Name")
	eventsCapture := &failHistoryContactEvents{}
	state := newHistoryStateStub()
	err := NewHistorySyncer(eventsCapture, state).Sync(context.Background(), "instance-a", raw, testHistoryMessageParser)
	if err == nil {
		t.Fatal("history sync completed after contact enrichment ingestion failed")
	}
	details := DescribeHistorySyncFailure(err)
	if details.Stage != "contact_enrichment" || details.Code != "history_sync_contact_enrichment_failed" || details.ConversationIndex != 0 || details.MessageIndex != -1 {
		t.Fatalf("failure details = %#v", details)
	}
	for _, event := range eventsCapture.events {
		if event.EventType == "history_sync_complete" {
			t.Fatal("contact enrichment failure emitted readiness completion")
		}
	}
}

func TestHistorySyncerFailsClosedForPayloadBearingMessageWithoutIdentity(t *testing.T) {
	raw := testHistorySync(waHistorySync.HistorySync_RECENT, 100)
	raw.Data.Conversations[0].Messages[0].Message.Key.ID = nil

	eventsCapture, state := &captureHistoryEvents{}, newHistoryStateStub()
	err := NewHistorySyncer(eventsCapture, state).Sync(context.Background(), "instance-a", raw, testHistoryMessageParser)
	if err == nil {
		t.Fatal("payload-bearing message without identity unexpectedly completed")
	}
	details := DescribeHistorySyncFailure(err)
	if details.Stage != "message_ingest" || details.Code != "history_sync_message_ingest_failed" ||
		details.ConversationIndex != 0 || details.MessageIndex != 0 {
		t.Fatalf("failure details = %#v", details)
	}
	if len(state.failed) != 2 {
		t.Fatalf("failed resources = %#v", state.failed)
	}
	for _, event := range eventsCapture.events {
		if event.EventType == "history_sync_complete" {
			t.Fatal("malformed payload-bearing message emitted readiness completion")
		}
	}
}

func testHistorySync(syncType waHistorySync.HistorySync_HistorySyncType, progress uint32) *events.HistorySync {
	chatID, messageID, participant := "group@g.us", "history-message", "sender@s.whatsapp.net"
	timestamp := uint64(800)
	status := waWeb.WebMessageInfo_DELIVERY_ACK
	return &events.HistorySync{Data: &waHistorySync.HistorySync{
		SyncType: &syncType, Progress: &progress, ChunkOrder: proto.Uint32(1),
		Conversations: []*waHistorySync.Conversation{{
			ID: &chatID, Name: proto.String("History group"), LastMsgTimestamp: &timestamp, UnreadCount: proto.Uint32(1), Archived: proto.Bool(true),
			Messages: []*waHistorySync.HistorySyncMsg{{Message: &waWeb.WebMessageInfo{
				Key:     &waCommon.MessageKey{RemoteJID: &chatID, ID: &messageID, Participant: &participant},
				Message: &waE2E.Message{Conversation: proto.String("historical")}, MessageTimestamp: &timestamp, Status: &status, Participant: &participant,
			}}},
		}},
	}}
}

func testHistoryMessageParser(chat types.JID, source *waWeb.WebMessageInfo) (*events.Message, error) {
	sender, err := types.ParseJID(source.GetParticipant())
	if err != nil {
		return nil, err
	}
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: sender, IsFromMe: source.GetKey().GetFromMe(), IsGroup: chat.Server == types.GroupServer},
			ID:            types.MessageID(source.GetKey().GetID()), Type: "text", Timestamp: time.Unix(int64(source.GetMessageTimestamp()), 0).UTC(),
		},
		Message: source.GetMessage(), SourceWebMsg: source,
	}, nil
}
