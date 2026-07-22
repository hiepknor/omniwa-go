package projection_repository

import (
	"encoding/json"
	"testing"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
)

func TestChatAndMessageApplyValidation(t *testing.T) {
	now := time.Unix(100, 0)
	chat := &projection_model.Chat{
		InstanceID: "instance-a", ChatID: "chat@s.whatsapp.net", Type: projection_model.ChatTypeDirect,
		SourceOccurredAt: now, SourceEventKey: "chat-100",
	}
	if err := validateChatApply(chat, []ChatAspect{ChatAspectIdentity}); err != nil {
		t.Fatalf("valid chat rejected: %v", err)
	}
	if err := validateChatApply(chat, []ChatAspect{ChatAspectIdentity, ChatAspectIdentity}); err == nil {
		t.Fatal("duplicate chat aspect accepted")
	}
	message := &projection_model.ProjectedMessage{
		InstanceID: "instance-a", MessageID: "message-100", ChatID: chat.ChatID,
		Direction: projection_model.MessageDirectionIncoming, MessageType: "text", ProviderTimestamp: now,
		Provenance: projection_model.MessageProvenanceLive, SourceOccurredAt: now, SourceEventKey: "message-100",
	}
	if err := validateMessageApply(message, []MessageAspect{MessageAspectEnvelope}); err != nil {
		t.Fatalf("valid message rejected: %v", err)
	}
	message.Provenance = "provider_native"
	if err := validateMessageApply(message, []MessageAspect{MessageAspectEnvelope}); err == nil {
		t.Fatal("invalid message provenance accepted")
	}
	message.Provenance = projection_model.MessageProvenanceLive
	oversizedSender := string(make([]byte, 256))
	message.SenderJID = &oversizedSender
	if err := validateMessageApply(message, []MessageAspect{MessageAspectEnvelope}); err == nil {
		t.Fatal("oversized message sender accepted")
	}
}

func TestProjectionAspectsAreIndependent(t *testing.T) {
	oldName, oldMessage := "Old", "message-old"
	chat := projection_model.Chat{DisplayName: &oldName, LastMessageID: &oldMessage}
	newName := "New"
	applyChatAspect(&chat, &projection_model.Chat{DisplayName: &newName, Type: projection_model.ChatTypeGroup}, ChatAspectIdentity)
	if chat.DisplayName == nil || *chat.DisplayName != newName || chat.LastMessageID == nil || *chat.LastMessageID != oldMessage {
		t.Fatalf("chat identity patch changed another aspect: %#v", chat)
	}
	oldContent, status := "old content", "delivered"
	message := projection_model.ProjectedMessage{ContentText: &oldContent, Status: &status}
	newContent := "new content"
	applyMessageAspect(&message, &projection_model.ProjectedMessage{ContentText: &newContent}, MessageAspectContent)
	if message.ContentText == nil || *message.ContentText != newContent || message.Status == nil || *message.Status != status {
		t.Fatalf("message content patch changed another aspect: %#v", message)
	}
}

func TestProjectionVersionOrderingUsesEventKeyAsTieBreaker(t *testing.T) {
	base := time.Unix(100, 0)
	if projectionVersionLess(projectionFieldVersion{OccurredAt: base, EventKey: "b"}, projectionFieldVersion{OccurredAt: base, EventKey: "a"}) {
		t.Fatal("lower event key replaced a version at the same timestamp")
	}
	if !projectionVersionLess(projectionFieldVersion{OccurredAt: base, EventKey: "a"}, projectionFieldVersion{OccurredAt: base, EventKey: "b"}) {
		t.Fatal("higher event key did not replace a version at the same timestamp")
	}
	versions, err := decodeProjectionVersions(json.RawMessage(`{"content":{"occurredAt":"1970-01-01T00:01:40Z","eventKey":"message-100"}}`))
	if err != nil || versions[string(MessageAspectContent)].EventKey != "message-100" {
		t.Fatalf("decoded versions = %#v, %v", versions, err)
	}
}

func TestRepositoryRejectsInvalidPaginationBeforeQuery(t *testing.T) {
	repository := &chatMessageRepository{}
	if _, err := repository.ListChats(t.Context(), "instance-a", 0, nil); err == nil {
		t.Fatal("zero chat page size accepted")
	}
	if _, err := repository.ListMessages(t.Context(), "instance-a", "chat-a", maxProjectionPageSize+1, nil); err == nil {
		t.Fatal("oversized message page accepted")
	}
}
