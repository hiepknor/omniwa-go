package projection_service

import (
	"context"
	"os"
	"testing"
	"time"

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	"github.com/evolution-foundation/evolution-go/pkg/migrations"
	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
	waE2E "go.mau.fi/whatsmeow/proto/waE2E"
	waHistorySync "go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestChatMessageProjectionPostgresReceiptBeforeMessageConverges(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not set")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&instance_model.Instance{}); err != nil {
		t.Fatal(err)
	}
	if err := migrations.Run(db); err != nil {
		t.Fatal(err)
	}
	instance := instance_model.Instance{Name: "chat-message-projection-test", Token: "chat-message-projection-token"}
	if err := db.Create(&instance).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Delete(&instance).Error })

	eventsRepository := projection_repository.NewEventRepository(db)
	eventsService := NewEventService(eventsRepository, 30*time.Second, 0)
	projectionRepository := projection_repository.NewChatMessageRepository(db)
	stateService := NewStateService(projection_repository.NewStateRepository(db))
	projector := NewChatMessageProjector(projectionRepository, stateService)

	receiptAt := time.Unix(900, 0).UTC()
	receiptEvent, _, err := NormalizeChatMessageEvent(instance.Id, &events.Receipt{
		MessageSource: types.MessageSource{
			Chat: types.NewJID("15550001", types.DefaultUserServer), Sender: types.NewJID("15550001", types.DefaultUserServer),
		},
		MessageIDs: []types.MessageID{"message-late"}, Timestamp: receiptAt, Type: types.ReceiptTypeDelivered,
	})
	if err != nil {
		t.Fatal(err)
	}
	if inserted, err := eventsService.Ingest(context.Background(), receiptEvent); err != nil || !inserted {
		t.Fatalf("first receipt ingest = %v, %v", inserted, err)
	}
	if inserted, err := eventsService.Ingest(context.Background(), receiptEvent); err != nil || inserted {
		t.Fatalf("duplicate receipt ingest = %v, %v", inserted, err)
	}
	result, err := eventsService.ProcessBatchFor(context.Background(), messageResource, []string{"receipt"}, 10, projector.Handle)
	if err != nil || result.Processed != 1 || result.Failed != 0 {
		t.Fatalf("receipt batch = %#v, %v", result, err)
	}

	messageAt := time.Unix(850, 0).UTC()
	messageEvent, _, err := NormalizeChatMessageEvent(instance.Id, &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: types.NewJID("15550001", types.DefaultUserServer), Sender: types.NewJID("self", types.DefaultUserServer), IsFromMe: true},
			ID:            "message-late", Type: "text", Timestamp: messageAt,
		},
		Message: &waE2E.Message{Conversation: proto.String("arrived after its receipt")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eventsService.Ingest(context.Background(), messageEvent); err != nil {
		t.Fatal(err)
	}
	result, err = eventsService.ProcessBatchFor(context.Background(), messageResource, []string{"message"}, 10, projector.Handle)
	if err != nil || result.Processed != 1 || result.Failed != 0 {
		t.Fatalf("message batch = %#v, %v", result, err)
	}

	message, err := projectionRepository.GetMessage(context.Background(), instance.Id, "message-late")
	if err != nil || message.MessageType != "text" || message.ContentText == nil || *message.ContentText != "arrived after its receipt" || !message.ProviderTimestamp.Equal(messageAt) {
		t.Fatalf("converged projected message = %#v, %v", message, err)
	}
	receipts, err := projectionRepository.ListReceipts(context.Background(), instance.Id, message.MessageID)
	if err != nil || len(receipts) != 1 || !receipts[0].ReceiptAt.Equal(receiptAt) {
		t.Fatalf("converged projected receipts = %#v, %v", receipts, err)
	}
	chat, err := projectionRepository.GetChat(context.Background(), instance.Id, message.ChatID)
	if err != nil || chat.LastMessageAt == nil || !chat.LastMessageAt.Equal(messageAt) {
		t.Fatalf("converged projected chat = %#v, %v", chat, err)
	}

	writeThroughAt := time.Unix(1_000, 0).UTC()
	writeThroughInfo := types.MessageInfo{
		MessageSource: types.MessageSource{
			Chat: types.NewJID("15550002", types.DefaultUserServer), Sender: types.NewJID("self", types.DefaultUserServer), IsFromMe: true,
		},
		ID: "message-write-through", Type: "ExtendedTextMessage", Timestamp: writeThroughAt,
	}
	writeThroughMessage := &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("confirmed outbound")}}
	if err := NewMessageWriteThrough(projector).WriteSent(context.Background(), instance.Id, writeThroughInfo, writeThroughMessage); err != nil {
		t.Fatal(err)
	}
	echoEvent, _, err := NormalizeChatMessageEvent(instance.Id, &events.Message{Info: writeThroughInfo, Message: writeThroughMessage})
	if err != nil {
		t.Fatal(err)
	}
	if err := projector.Handle(context.Background(), echoEvent); err != nil {
		t.Fatal(err)
	}
	var writeThroughCount int64
	if err := db.Table("projected_messages").Where("instance_id = ? AND message_id = ?", instance.Id, writeThroughInfo.ID).Count(&writeThroughCount).Error; err != nil || writeThroughCount != 1 {
		t.Fatalf("write-through/echo message count = %d, %v", writeThroughCount, err)
	}
	storedWriteThrough, err := projectionRepository.GetMessage(context.Background(), instance.Id, string(writeThroughInfo.ID))
	if err != nil || storedWriteThrough.ContentText == nil || *storedWriteThrough.ContentText != "confirmed outbound" || !storedWriteThrough.ProviderTimestamp.Equal(writeThroughAt) {
		t.Fatalf("write-through/echo message = %#v, %v", storedWriteThrough, err)
	}

	historySyncer := NewHistorySyncer(eventsService, stateService)
	if err := historySyncer.Sync(context.Background(), instance.Id, testHistorySync(waHistorySync.HistorySync_RECENT, 100), testHistoryMessageParser); err != nil {
		t.Fatal(err)
	}
	result, err = eventsService.ProcessBatchFor(context.Background(), messageResource, historyProjectionEventTypes, 10, projector.Handle)
	if err != nil || result.Processed != 2 || result.Failed != 0 {
		t.Fatalf("history projection batch = %#v, %v", result, err)
	}
	readiness := NewHistoryReadinessProjector(stateService, projection_repository.NewReadinessRepository(db))
	result, err = eventsService.ProcessBatchFor(context.Background(), messageResource, []string{"history_sync_complete"}, 10, readiness.Handle)
	if err != nil || result.Processed != 1 || result.Failed != 0 {
		t.Fatalf("history readiness batch = %#v, %v", result, err)
	}
	historical, err := projectionRepository.GetMessage(context.Background(), instance.Id, "history-message")
	if err != nil || historical.Provenance != projection_model.MessageProvenanceHistorySync || historical.HistorySyncID == nil || *historical.HistorySyncID == "" {
		t.Fatalf("historical projected message = %#v, %v", historical, err)
	}
	for _, resource := range []string{"chats", messageResource} {
		state, stateErr := stateService.Get(instance.Id, resource)
		if stateErr != nil || state.SyncStatus != projection_model.SyncStatusReady || state.LastReconciledAt == nil {
			t.Fatalf("history readiness state for %s = %#v, %v", resource, state, stateErr)
		}
	}
}
