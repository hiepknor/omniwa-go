package projection_repository

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	"github.com/evolution-foundation/evolution-go/pkg/migrations"
	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestChatMessageRepositoryPostgresOrderingPaginationAndConcurrency(t *testing.T) {
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
	instance := instance_model.Instance{Name: "chat-message-repository-test", Token: "chat-message-repository-token"}
	if err := db.Create(&instance).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Delete(&instance).Error })

	clock := time.Unix(1_000, 0).UTC()
	repository := &chatMessageRepository{db: db, now: func() time.Time { return clock }}
	base := time.Unix(100, 0).UTC()
	chat := projection_model.Chat{
		InstanceID: instance.Id, ChatID: "chat@s.whatsapp.net", Type: projection_model.ChatTypeDirect,
		LastActivityAt: &base, SourceOccurredAt: base, SourceEventKey: "chat-100",
	}
	if applied, err := repository.ApplyChat(context.Background(), &chat, ChatAspectIdentity, ChatAspectActivity); err != nil || !applied {
		t.Fatalf("create chat = %v, %v", applied, err)
	}
	if applied, err := repository.ApplyChat(context.Background(), &chat, ChatAspectIdentity, ChatAspectActivity); err != nil || applied {
		t.Fatalf("duplicate chat = %v, %v", applied, err)
	}
	archived := true
	settingsTime := time.Unix(300, 0).UTC()
	settings := projection_model.Chat{
		InstanceID: instance.Id, ChatID: chat.ChatID, Archived: &archived,
		SourceOccurredAt: settingsTime, SourceEventKey: "settings-300",
	}
	if applied, err := repository.ApplyChat(context.Background(), &settings, ChatAspectSettings); err != nil || !applied {
		t.Fatalf("apply chat settings = %v, %v", applied, err)
	}
	activityTime := time.Unix(200, 0).UTC()
	activity := projection_model.Chat{
		InstanceID: instance.Id, ChatID: chat.ChatID, LastActivityAt: &activityTime,
		SourceOccurredAt: activityTime, SourceEventKey: "activity-200",
	}
	if applied, err := repository.ApplyChat(context.Background(), &activity, ChatAspectActivity); err != nil || !applied {
		t.Fatalf("apply independently ordered chat activity = %v, %v", applied, err)
	}
	storedChat, err := repository.GetChat(context.Background(), instance.Id, chat.ChatID)
	if err != nil || storedChat.Archived == nil || !*storedChat.Archived || storedChat.LastActivityAt == nil || !storedChat.LastActivityAt.Equal(activityTime) || !storedChat.SourceOccurredAt.Equal(settingsTime) {
		t.Fatalf("stored chat = %#v, %v", storedChat, err)
	}
	fullName := "Canonical Name"
	contactRepository := NewContactRepository(db)
	contact, _, err := contactRepository.Apply(context.Background(), ContactPatch{
		InstanceID: instance.Id, Identities: []ContactIdentityRef{{Kind: projection_model.ContactIdentityKindJID, Value: chat.ChatID}},
		Aspect: ContactAspectDetails, OccurredAt: time.Unix(310, 0).UTC(), EventKey: "contact-name-310", FullName: &fullName,
	})
	if err != nil {
		t.Fatal(err)
	}
	storedChat, err = repository.GetChat(context.Background(), instance.Id, chat.ChatID)
	if err != nil || storedChat.ContactID == nil || *storedChat.ContactID != contact.ContactID || storedChat.DisplayName == nil ||
		*storedChat.DisplayName != fullName || storedChat.DisplayNameSource == nil || *storedChat.DisplayNameSource != "full_name" {
		t.Fatalf("canonical direct chat identity = %#v, %v", storedChat, err)
	}
	renamed := "Renamed Contact"
	if _, _, err := contactRepository.Apply(context.Background(), ContactPatch{
		InstanceID: instance.Id, Identities: []ContactIdentityRef{{Kind: projection_model.ContactIdentityKindJID, Value: chat.ChatID}},
		Aspect: ContactAspectDetails, OccurredAt: time.Unix(320, 0).UTC(), EventKey: "contact-name-320", FullName: &renamed,
	}); err != nil {
		t.Fatal(err)
	}
	replayedIdentity := projection_model.Chat{InstanceID: instance.Id, ChatID: chat.ChatID, Type: projection_model.ChatTypeDirect, SourceOccurredAt: time.Unix(330, 0).UTC(), SourceEventKey: "chat-330"}
	if _, err := repository.ApplyChat(context.Background(), &replayedIdentity, ChatAspectIdentity); err != nil {
		t.Fatal(err)
	}
	storedChat, err = repository.GetChat(context.Background(), instance.Id, chat.ChatID)
	if err != nil || storedChat.DisplayName == nil || *storedChat.DisplayName != renamed || storedChat.ConversationID == nil {
		t.Fatalf("contact rename or replayed chat identity lost = %#v, %v", storedChat, err)
	}
	groupName := "Group Subject"
	group := projection_model.Chat{InstanceID: instance.Id, ChatID: "group@g.us", Type: projection_model.ChatTypeGroup, DisplayName: &groupName, SourceOccurredAt: time.Unix(340, 0).UTC(), SourceEventKey: "group-340"}
	if _, err := repository.ApplyChat(context.Background(), &group, ChatAspectIdentity); err != nil {
		t.Fatal(err)
	}
	if _, _, err := contactRepository.Apply(context.Background(), ContactPatch{
		InstanceID: instance.Id, Identities: []ContactIdentityRef{{Kind: projection_model.ContactIdentityKindJID, Value: group.ChatID}},
		Aspect: ContactAspectDetails, OccurredAt: time.Unix(350, 0).UTC(), EventKey: "group-contact-350", FullName: &fullName,
	}); err != nil {
		t.Fatal(err)
	}
	storedGroup, err := repository.GetChat(context.Background(), instance.Id, group.ChatID)
	if err != nil || storedGroup.ContactID != nil || storedGroup.DisplayName == nil || *storedGroup.DisplayName != groupName ||
		storedGroup.DisplayNameSource == nil || *storedGroup.DisplayNameSource != "group_subject" {
		t.Fatalf("group name overwritten by contact logic = %#v, %v", storedGroup, err)
	}

	content := "first"
	message := projection_model.ProjectedMessage{
		InstanceID: instance.Id, MessageID: "message-100", ChatID: chat.ChatID,
		Direction: projection_model.MessageDirectionIncoming, MessageType: "text", ContentText: &content,
		ProviderTimestamp: base, Provenance: projection_model.MessageProvenanceLive,
		SourceOccurredAt: base, SourceEventKey: "message-100",
	}
	if applied, err := repository.ApplyMessage(context.Background(), &message, MessageAspectEnvelope, MessageAspectContent); err != nil || !applied {
		t.Fatalf("create message = %v, %v", applied, err)
	}
	status := "read"
	lifecycle := projection_model.ProjectedMessage{
		InstanceID: instance.Id, MessageID: message.MessageID, Status: &status,
		SourceOccurredAt: settingsTime, SourceEventKey: "receipt-300",
	}
	if applied, err := repository.ApplyMessage(context.Background(), &lifecycle, MessageAspectLifecycle); err != nil || !applied {
		t.Fatalf("apply message lifecycle = %v, %v", applied, err)
	}
	lateContentValue := "late content"
	lateContent := projection_model.ProjectedMessage{
		InstanceID: instance.Id, MessageID: message.MessageID, ContentText: &lateContentValue,
		SourceOccurredAt: activityTime, SourceEventKey: "message-200",
	}
	if applied, err := repository.ApplyMessage(context.Background(), &lateContent, MessageAspectContent); err != nil || !applied {
		t.Fatalf("apply independently ordered message content = %v, %v", applied, err)
	}
	storedMessage, err := repository.GetMessage(context.Background(), instance.Id, message.MessageID)
	if err != nil || storedMessage.ContentText == nil || *storedMessage.ContentText != lateContentValue || storedMessage.Status == nil || *storedMessage.Status != status ||
		!storedMessage.SourceOccurredAt.Equal(settingsTime) || storedMessage.ConversationID == nil || *storedMessage.ConversationID != *storedChat.ConversationID {
		t.Fatalf("stored message = %#v, %v", storedMessage, err)
	}

	receipt := projection_model.MessageReceipt{
		InstanceID: instance.Id, MessageID: message.MessageID, RecipientJID: "recipient@s.whatsapp.net", ReceiptType: "read",
		ReceiptAt: settingsTime, SourceOccurredAt: settingsTime, SourceEventKey: "receipt-b",
	}
	if applied, err := repository.ApplyReceipt(context.Background(), &receipt); err != nil || !applied {
		t.Fatalf("create receipt = %v, %v", applied, err)
	}
	olderReceipt := receipt
	olderReceipt.ReceiptAt = activityTime
	olderReceipt.SourceOccurredAt = activityTime
	olderReceipt.SourceEventKey = "receipt-old"
	if applied, err := repository.ApplyReceipt(context.Background(), &olderReceipt); err != nil || applied {
		t.Fatalf("older receipt = %v, %v", applied, err)
	}
	receipts, err := repository.ListReceipts(context.Background(), instance.Id, message.MessageID)
	if err != nil || len(receipts) != 1 || !receipts[0].ReceiptAt.Equal(settingsTime) {
		t.Fatalf("receipts = %#v, %v", receipts, err)
	}

	for index, id := range []string{"chat-c", "chat-b", "chat-a"} {
		at := time.Unix(int64(400-index), 0).UTC()
		item := projection_model.Chat{
			InstanceID: instance.Id, ChatID: id, Type: projection_model.ChatTypeDirect, LastActivityAt: &at,
			SourceOccurredAt: at, SourceEventKey: id,
		}
		if _, err := repository.ApplyChat(context.Background(), &item, ChatAspectIdentity, ChatAspectActivity); err != nil {
			t.Fatal(err)
		}
	}
	nullActivityChat := projection_model.Chat{
		InstanceID: instance.Id, ChatID: "chat-null", Type: projection_model.ChatTypeDirect,
		SourceOccurredAt: time.Unix(350, 0).UTC(), SourceEventKey: "chat-null",
	}
	if _, err := repository.ApplyChat(context.Background(), &nullActivityChat, ChatAspectIdentity); err != nil {
		t.Fatal(err)
	}
	firstChats, err := repository.ListChats(context.Background(), instance.Id, 2, nil)
	if err != nil || len(firstChats.Items) != 2 || firstChats.NextCursor == nil || firstChats.Total != 6 {
		t.Fatalf("first chat page = %#v, %v", firstChats, err)
	}
	secondChats, err := repository.ListChats(context.Background(), instance.Id, 2, firstChats.NextCursor)
	if err != nil || len(secondChats.Items) != 2 || secondChats.NextCursor == nil || secondChats.Total != firstChats.Total || firstChats.Items[1].ChatID == secondChats.Items[0].ChatID {
		t.Fatalf("second chat page = %#v, %v", secondChats, err)
	}
	thirdChats, err := repository.ListChats(context.Background(), instance.Id, 2, secondChats.NextCursor)
	if err != nil || len(thirdChats.Items) != 2 || thirdChats.Items[1].ChatID != nullActivityChat.ChatID || thirdChats.NextCursor != nil || thirdChats.Total != firstChats.Total {
		t.Fatalf("third chat page = %#v, %v", thirdChats, err)
	}

	for index, id := range []string{"message-c", "message-b", "message-a"} {
		at := time.Unix(int64(400-index), 0).UTC()
		item := projection_model.ProjectedMessage{
			InstanceID: instance.Id, MessageID: id, ChatID: chat.ChatID,
			Direction: projection_model.MessageDirectionIncoming, MessageType: "text", ProviderTimestamp: at,
			Provenance: projection_model.MessageProvenanceHistorySync, SourceOccurredAt: at, SourceEventKey: id,
		}
		if _, err := repository.ApplyMessage(context.Background(), &item, MessageAspectEnvelope); err != nil {
			t.Fatal(err)
		}
	}
	firstMessages, err := repository.ListMessages(context.Background(), instance.Id, chat.ChatID, 2, nil)
	if err != nil || len(firstMessages.Items) != 2 || firstMessages.NextCursor == nil {
		t.Fatalf("first message page = %#v, %v", firstMessages, err)
	}
	secondMessages, err := repository.ListMessages(context.Background(), instance.Id, chat.ChatID, 2, firstMessages.NextCursor)
	if err != nil || len(secondMessages.Items) != 2 || firstMessages.Items[1].MessageID == secondMessages.Items[0].MessageID {
		t.Fatalf("second message page = %#v, %v", secondMessages, err)
	}

	var appliedCount atomic.Int32
	var wait sync.WaitGroup
	errorsChannel := make(chan error, 20)
	for index := 0; index < 20; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			concurrent := projection_model.ProjectedMessage{
				InstanceID: instance.Id, MessageID: "message-concurrent", ChatID: chat.ChatID,
				Direction: projection_model.MessageDirectionIncoming, MessageType: "text", ProviderTimestamp: settingsTime,
				Provenance: projection_model.MessageProvenanceLive, SourceOccurredAt: settingsTime, SourceEventKey: "message-concurrent",
			}
			applied, err := repository.ApplyMessage(context.Background(), &concurrent, MessageAspectEnvelope)
			if err != nil {
				errorsChannel <- err
				return
			}
			if applied {
				appliedCount.Add(1)
			}
		}()
	}
	wait.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		t.Error(err)
	}
	if appliedCount.Load() != 1 {
		t.Fatalf("concurrent applied count = %d", appliedCount.Load())
	}

	retentionTimestamp := time.Unix(10, 0).UTC()
	retentionMessage := projection_model.ProjectedMessage{
		InstanceID: instance.Id, MessageID: "message-expired", ChatID: chat.ChatID,
		Direction: projection_model.MessageDirectionIncoming, MessageType: "text", ProviderTimestamp: retentionTimestamp,
		Provenance: projection_model.MessageProvenanceLive, SourceOccurredAt: retentionTimestamp, SourceEventKey: "message-expired",
	}
	if _, err := repository.ApplyMessage(context.Background(), &retentionMessage, MessageAspectEnvelope); err != nil {
		t.Fatal(err)
	}
	retentionReceipt := projection_model.MessageReceipt{
		InstanceID: instance.Id, MessageID: retentionMessage.MessageID, RecipientJID: "recipient@s.whatsapp.net", ReceiptType: "delivered",
		ReceiptAt: retentionTimestamp, SourceOccurredAt: retentionTimestamp, SourceEventKey: "message-expired-receipt",
	}
	if _, err := repository.ApplyReceipt(context.Background(), &retentionReceipt); err != nil {
		t.Fatal(err)
	}
	retentionEvent := &projection_model.Event{
		InstanceID: instance.Id, Resource: "messages", EventKey: "message-expired-event", EntityKey: retentionMessage.MessageID,
		EventType: "history_message", OccurredAt: retentionTimestamp, Payload: json.RawMessage(`{"contentText":"expired"}`),
	}
	if inserted, err := NewEventRepository(db).Enqueue(context.Background(), retentionEvent); err != nil || !inserted {
		t.Fatalf("retention event ingest = %v, %v", inserted, err)
	}
	deleted, err := NewMessageRetentionRepository(db).DeleteBefore(context.Background(), retentionTimestamp, 1)
	if err != nil || deleted != 1 {
		t.Fatalf("retention delete = %d, %v", deleted, err)
	}
	if _, err := repository.GetMessage(context.Background(), instance.Id, retentionMessage.MessageID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("expired message still exists: %v", err)
	}
	retentionReceipts, err := repository.ListReceipts(context.Background(), instance.Id, retentionMessage.MessageID)
	if err != nil || len(retentionReceipts) != 0 {
		t.Fatalf("expired message receipts = %#v, %v", retentionReceipts, err)
	}
	var retentionEventCount int64
	if err := db.Model(&projection_model.Event{}).Where("instance_id = ? AND resource = ? AND event_key = ?", instance.Id, "messages", retentionEvent.EventKey).Count(&retentionEventCount).Error; err != nil || retentionEventCount != 0 {
		t.Fatalf("expired projection event count = %d, %v", retentionEventCount, err)
	}
}
