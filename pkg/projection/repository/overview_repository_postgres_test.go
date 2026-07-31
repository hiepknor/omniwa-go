package projection_repository

import (
	"context"
	"encoding/json"
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

func TestOverviewCountsCanonicalConversationsSeparatelyFromProviderChats(t *testing.T) {
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
	instance := instance_model.Instance{Name: "overview-conversation-" + suffix, Token: "overview-conversation-token-" + suffix}
	if err = db.Create(&instance).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Delete(&instance).Error })

	now := time.Now().UTC()
	conversationID := uuid.NewString()
	conversation := projection_model.Conversation{
		InstanceID: instance.Id, ConversationID: conversationID, Type: projection_model.ChatTypeDirect,
		FieldVersions: json.RawMessage(`{}`), LastSyncedAt: now,
	}
	if err = db.Create(&conversation).Error; err != nil {
		t.Fatal(err)
	}
	for index, chatID := range []string{"15551230000@s.whatsapp.net", "900000000000@lid"} {
		chat := projection_model.Chat{
			InstanceID: instance.Id, ChatID: chatID, ConversationID: &conversationID, Type: projection_model.ChatTypeDirect,
			SourceOccurredAt: now, SourceEventKey: suffix + "-" + string(rune('a'+index)), FieldVersions: json.RawMessage(`{}`), LastSyncedAt: now,
		}
		if err = db.Create(&chat).Error; err != nil {
			t.Fatal(err)
		}
	}

	counts, err := NewOverviewRepository(db).Snapshot(context.Background(), instance.Id, now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if counts.Chats != 2 || counts.Conversations != 1 {
		t.Fatalf("overview counts chats=%d conversations=%d", counts.Chats, counts.Conversations)
	}
}
