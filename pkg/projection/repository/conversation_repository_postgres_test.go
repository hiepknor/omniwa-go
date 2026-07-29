package projection_repository

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	"github.com/evolution-foundation/evolution-go/pkg/migrations"
	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestCanonicalConversationAssociatesAuthoritativeAliasesConcurrently(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not set")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.AutoMigrate(&instance_model.Instance{}); err != nil {
		t.Fatal(err)
	}
	if err = migrations.Run(db); err != nil {
		t.Fatal(err)
	}
	suffix := uuid.NewString()
	instances := []instance_model.Instance{
		{Name: "canonical-conversation-a-" + suffix, Token: "canonical-conversation-token-a-" + suffix},
		{Name: "canonical-conversation-b-" + suffix, Token: "canonical-conversation-token-b-" + suffix},
	}
	for index := range instances {
		if err = db.Create(&instances[index]).Error; err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for index := range instances {
			_ = db.Delete(&instances[index]).Error
		}
	})

	phoneJID, lid := "15551235555@s.whatsapp.net", "900000005555@lid"
	contactRepository := NewContactRepository(db)
	contact, _, err := contactRepository.Apply(context.Background(), ContactPatch{
		InstanceID: instances[0].Id,
		Identities: []ContactIdentityRef{
			{Kind: projection_model.ContactIdentityKindJID, Value: phoneJID},
			{Kind: projection_model.ContactIdentityKindJID, Value: lid},
			{Kind: projection_model.ContactIdentityKindPhoneJID, Value: phoneJID},
			{Kind: projection_model.ContactIdentityKindLID, Value: lid},
		},
		Aspect: ContactAspectIdentity, OccurredAt: time.Unix(100, 0).UTC(), EventKey: "authoritative-pn-lid-map",
	})
	if err != nil {
		t.Fatal(err)
	}

	chatRepository := NewChatMessageRepository(db)
	start := make(chan struct{})
	errorsChannel := make(chan error, 2)
	var wait sync.WaitGroup
	for index, chatID := range []string{phoneJID, lid} {
		wait.Add(1)
		go func(index int, chatID string) {
			defer wait.Done()
			<-start
			at := time.Unix(int64(200+index), 0).UTC()
			_, applyErr := chatRepository.ApplyChat(context.Background(), &projection_model.Chat{
				InstanceID: instances[0].Id, ChatID: chatID, Type: projection_model.ChatTypeDirect,
				LastActivityAt: &at, SourceOccurredAt: at, SourceEventKey: chatID,
			}, ChatAspectIdentity, ChatAspectActivity)
			errorsChannel <- applyErr
		}(index, chatID)
	}
	close(start)
	wait.Wait()
	close(errorsChannel)
	for applyErr := range errorsChannel {
		if applyErr != nil {
			t.Fatal(applyErr)
		}
	}

	var chats []projection_model.Chat
	if err = db.Where("instance_id = ? AND chat_id IN ?", instances[0].Id, []string{phoneJID, lid}).Order("chat_id").Find(&chats).Error; err != nil {
		t.Fatal(err)
	}
	if len(chats) != 2 || chats[0].ConversationID == nil || chats[1].ConversationID == nil || *chats[0].ConversationID != *chats[1].ConversationID {
		t.Fatalf("canonical direct chats = %#v", chats)
	}
	conversationID := *chats[0].ConversationID
	var conversation projection_model.Conversation
	if err = db.Where("instance_id = ? AND conversation_id = ?", instances[0].Id, conversationID).First(&conversation).Error; err != nil ||
		conversation.ContactID == nil || *conversation.ContactID != contact.ContactID || conversation.AddressingJID == nil || *conversation.AddressingJID != phoneJID ||
		conversation.UnreadAuthoritative {
		t.Fatalf("canonical conversation = %#v, %v", conversation, err)
	}
	if err = db.Model(&projection_model.Conversation{}).
		Where("instance_id = ? AND conversation_id = ?", instances[0].Id, conversationID).
		Update("addressing_jid", lid).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err = contactRepository.Apply(context.Background(), ContactPatch{
		InstanceID: instances[0].Id,
		Identities: []ContactIdentityRef{
			{Kind: projection_model.ContactIdentityKindJID, Value: phoneJID},
			{Kind: projection_model.ContactIdentityKindJID, Value: lid},
			{Kind: projection_model.ContactIdentityKindPhoneJID, Value: phoneJID},
			{Kind: projection_model.ContactIdentityKindLID, Value: lid},
		},
		Aspect: ContactAspectIdentity, OccurredAt: time.Unix(100, 0).UTC(), EventKey: "authoritative-pn-lid-map",
	}); err != nil {
		t.Fatal(err)
	}
	if err = db.Where("instance_id = ? AND conversation_id = ?", instances[0].Id, conversationID).First(&conversation).Error; err != nil ||
		conversation.AddressingJID == nil || *conversation.AddressingJID != phoneJID {
		t.Fatalf("replayed contact did not refresh conversation addressing = %#v, %v", conversation, err)
	}

	messageAt := time.Unix(300, 0).UTC()
	message := projection_model.ProjectedMessage{
		InstanceID: instances[0].Id, MessageID: "canonical-message", ChatID: lid,
		Direction: projection_model.MessageDirectionIncoming, MessageType: "text", ProviderTimestamp: messageAt,
		Provenance: projection_model.MessageProvenanceHistorySync, SourceOccurredAt: messageAt, SourceEventKey: "canonical-message",
	}
	if _, err = chatRepository.ApplyMessage(context.Background(), &message, MessageAspectEnvelope); err != nil {
		t.Fatal(err)
	}
	storedMessage, err := chatRepository.GetMessage(context.Background(), instances[0].Id, message.MessageID)
	if err != nil || storedMessage.ConversationID == nil || *storedMessage.ConversationID != conversationID {
		t.Fatalf("canonical message association = %#v, %v", storedMessage, err)
	}
	syncID := "canonical-unread-sync"
	for _, snapshot := range []struct {
		chatID string
		unread int
	}{{phoneJID, 0}, {lid, 1}} {
		chat := projection_model.Chat{
			InstanceID: instances[0].Id, ChatID: snapshot.chatID, Type: projection_model.ChatTypeDirect,
			UnreadCount: snapshot.unread, UnreadSnapshotSyncID: &syncID, LastActivityAt: &messageAt,
			SourceOccurredAt: messageAt.Add(time.Second), SourceEventKey: "snapshot:" + snapshot.chatID,
		}
		if _, err = chatRepository.ApplyChat(context.Background(), &chat, ChatAspectIdentity, ChatAspectActivity, ChatAspectSettings); err != nil {
			t.Fatal(err)
		}
	}
	if err = chatRepository.ReconcileUnreadSnapshot(context.Background(), instances[0].Id, syncID); err != nil {
		t.Fatal(err)
	}
	if err = db.Where("instance_id = ? AND conversation_id = ?", instances[0].Id, conversationID).First(&conversation).Error; err != nil ||
		!conversation.UnreadAuthoritative || conversation.UnreadCount != 1 {
		t.Fatalf("authoritative canonical unread = %#v, %v", conversation, err)
	}
	if err = chatRepository.ReconcileUnreadSnapshot(context.Background(), instances[0].Id, syncID); err != nil {
		t.Fatal(err)
	}
	if changed, markErr := chatRepository.MarkMessageRead(context.Background(), instances[0].Id, message.MessageID, messageAt.Add(2*time.Second)); markErr != nil || !changed {
		t.Fatalf("mark canonical message read: changed=%v err=%v", changed, markErr)
	}
	unread := true
	lateLive := message
	lateLive.Provenance = projection_model.MessageProvenanceLive
	lateLive.IsUnread = &unread
	lateLive.SourceEventKey = "late-live-message"
	if _, err = chatRepository.ApplyMessage(context.Background(), &lateLive, MessageAspectEnvelope, MessageAspectUnread); err != nil {
		t.Fatal(err)
	}
	storedMessage, err = chatRepository.GetMessage(context.Background(), instances[0].Id, message.MessageID)
	if err != nil || storedMessage.IsUnread == nil || *storedMessage.IsUnread {
		t.Fatalf("late live replay overwrote read receipt = %#v, %v", storedMessage, err)
	}
	if err = db.Where("instance_id = ? AND conversation_id = ?", instances[0].Id, conversationID).First(&conversation).Error; err != nil ||
		!conversation.UnreadAuthoritative || conversation.UnreadCount != 0 {
		t.Fatalf("canonical unread after read = %#v, %v", conversation, err)
	}
	record, err := chatRepository.GetConversation(context.Background(), instances[0].Id, lid)
	if err != nil || record.Conversation.ConversationID != conversationID || len(record.Aliases) != 2 {
		t.Fatalf("canonical alias lookup = %#v, %v", record, err)
	}

	group := projection_model.Chat{
		InstanceID: instances[0].Id, ChatID: "canonical-group@g.us", ContactID: &contact.ContactID,
		Type: projection_model.ChatTypeGroup, SourceOccurredAt: time.Unix(400, 0).UTC(), SourceEventKey: "canonical-group",
	}
	if _, err = chatRepository.ApplyChat(context.Background(), &group, ChatAspectIdentity); err != nil {
		t.Fatal(err)
	}
	storedGroup, err := chatRepository.GetChat(context.Background(), instances[0].Id, group.ChatID)
	if err != nil || storedGroup.ConversationID == nil || *storedGroup.ConversationID == conversationID || storedGroup.ContactID != nil {
		t.Fatalf("group conversation isolation = %#v, %v", storedGroup, err)
	}

	crossInstance := projection_model.Chat{
		InstanceID: instances[1].Id, ChatID: phoneJID, Type: projection_model.ChatTypeDirect,
		SourceOccurredAt: time.Unix(500, 0).UTC(), SourceEventKey: "cross-instance-phone",
	}
	if _, err = chatRepository.ApplyChat(context.Background(), &crossInstance, ChatAspectIdentity); err != nil {
		t.Fatal(err)
	}
	storedCrossInstance, err := chatRepository.GetChat(context.Background(), instances[1].Id, phoneJID)
	if err != nil || storedCrossInstance.ConversationID == nil || *storedCrossInstance.ConversationID == conversationID {
		t.Fatalf("cross-instance conversation isolation = %#v, %v", storedCrossInstance, err)
	}
	crossRecord, err := chatRepository.GetConversation(context.Background(), instances[1].Id, phoneJID)
	if err != nil || crossRecord.Conversation.ConversationID != *storedCrossInstance.ConversationID {
		t.Fatalf("cross-instance alias lookup = %#v, %v", crossRecord, err)
	}
	if _, err = chatRepository.GetConversation(context.Background(), instances[1].Id, conversationID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("foreign canonical identity lookup error = %v", err)
	}
}
