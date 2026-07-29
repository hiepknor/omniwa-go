package projection_repository

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	"github.com/evolution-foundation/evolution-go/pkg/migrations"
	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestConversationBackfillLeasesResumeAndAssociatesAliasHistory(t *testing.T) {
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
	instance := instance_model.Instance{Name: "conversation-backfill-" + uuid.NewString(), Token: "conversation-backfill-token-" + uuid.NewString()}
	if err = db.Create(&instance).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Delete(&instance).Error })

	phoneJID, lid := "15559876543@s.whatsapp.net", "900000098765@lid"
	contact, _, err := NewContactRepository(db).Apply(context.Background(), ContactPatch{
		InstanceID: instance.Id,
		Identities: []ContactIdentityRef{
			{Kind: projection_model.ContactIdentityKindPhoneJID, Value: phoneJID},
			{Kind: projection_model.ContactIdentityKindLID, Value: lid},
		},
		Aspect: ContactAspectIdentity, OccurredAt: time.Unix(10, 0).UTC(), EventKey: "mapped-pn-lid",
	})
	if err != nil {
		t.Fatal(err)
	}
	chatRepository := NewChatMessageRepository(db)
	for index, chatID := range []string{phoneJID, lid} {
		at := time.Unix(int64(20+index), 0).UTC()
		if _, err = chatRepository.ApplyChat(context.Background(), &projection_model.Chat{
			InstanceID: instance.Id, ChatID: chatID, Type: projection_model.ChatTypeDirect,
			ContactID: &contact.ContactID, UnreadCount: index + 1, LastActivityAt: &at,
			SourceOccurredAt: at, SourceEventKey: "chat:" + chatID,
		}, ChatAspectIdentity, ChatAspectActivity, ChatAspectSettings); err != nil {
			t.Fatal(err)
		}
	}
	messageAt := time.Unix(30, 0).UTC()
	if _, err = chatRepository.ApplyMessage(context.Background(), &projection_model.ProjectedMessage{
		InstanceID: instance.Id, MessageID: "message-before-backfill", ChatID: lid,
		Direction: projection_model.MessageDirectionIncoming, MessageType: "image",
		ProviderTimestamp: messageAt, Provenance: projection_model.MessageProvenanceHistorySync,
		SourceOccurredAt: messageAt, SourceEventKey: "history-message",
	}, MessageAspectEnvelope); err != nil {
		t.Fatal(err)
	}

	// Recreate the state an upgrade from the pre-foundation schema presents.
	if err = db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&projection_model.ProjectedMessage{}).Where("instance_id = ?", instance.Id).Update("conversation_id", nil).Error; err != nil {
			return err
		}
		if err := tx.Model(&projection_model.Chat{}).Where("instance_id = ?", instance.Id).Update("conversation_id", nil).Error; err != nil {
			return err
		}
		if err := tx.Where("instance_id = ?", instance.Id).Delete(&projection_model.ChatAlias{}).Error; err != nil {
			return err
		}
		if err := tx.Where("instance_id = ?", instance.Id).Delete(&projection_model.ConversationRedirect{}).Error; err != nil {
			return err
		}
		return tx.Where("instance_id = ?", instance.Id).Delete(&projection_model.Conversation{}).Error
	}); err != nil {
		t.Fatal(err)
	}

	backfill := NewConversationBackfillRepository(db)
	ownerOne, ownerTwo := uuid.NewString(), uuid.NewString()
	first, err := backfill.ClaimBatch(context.Background(), instance.Id, 1, ownerOne, 1, time.Unix(100, 0).UTC(), time.Unix(200, 0).UTC())
	if err != nil || len(first.Items) != 1 || first.Complete {
		t.Fatalf("first claim = %#v, %v", first, err)
	}
	if _, err = backfill.ClaimBatch(context.Background(), instance.Id, 1, ownerTwo, 1, time.Unix(150, 0).UTC(), time.Unix(250, 0).UTC()); !errors.Is(err, ErrConversationBackfillLeaseHeld) {
		t.Fatalf("competing lease error = %v", err)
	}
	recovered, err := backfill.ClaimBatch(context.Background(), instance.Id, 1, ownerTwo, 1, time.Unix(201, 0).UTC(), time.Unix(301, 0).UTC())
	if err != nil || len(recovered.Items) != 1 || recovered.Items[0].ChatID != first.Items[0].ChatID {
		t.Fatalf("expired lease recovery = %#v, %v", recovered, err)
	}
	association, err := backfill.AssociateChat(context.Background(), instance.Id, recovered.Items[0].ChatID, time.Unix(202, 0).UTC())
	if err != nil || association.Associated != 2 || association.Messages != 1 {
		t.Fatalf("first association = %#v, %v", association, err)
	}
	cursor := recovered.Items[0].ChatID
	if err = backfill.CommitBatch(context.Background(), instance.Id, 1, ownerOne, &cursor, ConversationBackfillCounts{Scanned: 1}, false, time.Unix(203, 0).UTC()); !errors.Is(err, ErrConversationBackfillLeaseLost) {
		t.Fatalf("stale owner commit error = %v", err)
	}
	if err = backfill.CommitBatch(context.Background(), instance.Id, 1, ownerTwo, &cursor, ConversationBackfillCounts{
		Scanned: 1, Associated: association.Associated, Messages: association.Messages,
	}, false, time.Unix(204, 0).UTC()); err != nil {
		t.Fatal(err)
	}

	second, err := backfill.ClaimBatch(context.Background(), instance.Id, 1, ownerTwo, 10, time.Unix(205, 0).UTC(), time.Unix(305, 0).UTC())
	if err != nil || len(second.Items) != 1 || !second.Complete {
		t.Fatalf("second claim = %#v, %v", second, err)
	}
	association, err = backfill.AssociateChat(context.Background(), instance.Id, second.Items[0].ChatID, time.Unix(206, 0).UTC())
	if err != nil || association.Associated != 0 || association.Messages != 0 {
		t.Fatalf("second association = %#v, %v", association, err)
	}
	validation, err := backfill.Validate(context.Background(), instance.Id)
	if err != nil || !validation.AssociationsValid() || validation.Ready() || validation.UnreadNonAuthoritative != 1 {
		t.Fatalf("validation = %#v, %v", validation, err)
	}
	cursor = second.Items[0].ChatID
	if err = backfill.CommitBatch(context.Background(), instance.Id, 1, ownerTwo, &cursor, ConversationBackfillCounts{
		Scanned: 1, Associated: association.Associated, Messages: association.Messages,
	}, true, time.Unix(207, 0).UTC()); err != nil {
		t.Fatal(err)
	}

	state, err := backfill.GetState(context.Background(), instance.Id)
	if err != nil || state.Status != projection_model.ConversationBackfillComplete || state.ScannedCount != 2 || state.AssociatedCount != 2 || state.MessageCount != 1 {
		t.Fatalf("state = %#v, %v", state, err)
	}
	var chats []projection_model.Chat
	if err = db.Where("instance_id = ?", instance.Id).Order("chat_id ASC").Find(&chats).Error; err != nil {
		t.Fatal(err)
	}
	if len(chats) != 2 || chats[0].ConversationID == nil || chats[1].ConversationID == nil || *chats[0].ConversationID != *chats[1].ConversationID {
		t.Fatalf("canonical chats = %#v", chats)
	}
	message, err := chatRepository.GetMessage(context.Background(), instance.Id, "message-before-backfill")
	if err != nil || message.ConversationID == nil || *message.ConversationID != *chats[0].ConversationID {
		t.Fatalf("backfilled message = %#v, %v", message, err)
	}
}
