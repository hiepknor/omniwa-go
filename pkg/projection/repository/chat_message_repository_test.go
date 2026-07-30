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

func TestChatSettingAspectsDoNotClobberEachOther(t *testing.T) {
	archived, pinned := false, true
	mutedUntil := time.Unix(500, 0).UTC()
	chat := projection_model.Chat{Archived: &archived, Pinned: &pinned, MutedUntil: &mutedUntil}
	newArchived := true
	applyChatAspect(&chat, &projection_model.Chat{Archived: &newArchived}, ChatAspectArchived)
	if chat.Archived == nil || !*chat.Archived || chat.Pinned == nil || !*chat.Pinned || chat.MutedUntil == nil || !chat.MutedUntil.Equal(mutedUntil) {
		t.Fatalf("archive patch changed another setting: %#v", chat)
	}
	applyChatAspect(&chat, &projection_model.Chat{}, ChatAspectMuted)
	if chat.MutedUntil != nil || chat.Archived == nil || !*chat.Archived || chat.Pinned == nil || !*chat.Pinned {
		t.Fatalf("unmute patch changed another setting: %#v", chat)
	}
}

func TestCanonicalSettingsChooseNewestAspectAcrossAliases(t *testing.T) {
	archivedPN, archivedLID := false, true
	chats := []projection_model.Chat{
		{ChatID: "15550001@s.whatsapp.net", Archived: &archivedPN, FieldVersions: json.RawMessage(`{"settings":{"occurredAt":"1970-01-01T00:03:20Z","eventKey":"snapshot"}}`)},
		{ChatID: "123456@lid", Archived: &archivedLID, FieldVersions: json.RawMessage(`{"archived":{"occurredAt":"1970-01-01T00:01:40Z","eventKey":"event"}}`)},
	}
	source, err := latestConversationSettingSource(chats, ChatAspectArchived, ChatAspectSettings)
	if err != nil || source == nil || source.ChatID != "15550001@s.whatsapp.net" || source.Archived == nil || *source.Archived {
		t.Fatalf("newer snapshot source=%+v err=%v", source, err)
	}
	chats[1].FieldVersions = json.RawMessage(`{"archived":{"occurredAt":"1970-01-01T00:05:00Z","eventKey":"event"}}`)
	source, err = latestConversationSettingSource(chats, ChatAspectArchived, ChatAspectSettings)
	if err != nil || source == nil || source.ChatID != "123456@lid" || source.Archived == nil || !*source.Archived {
		t.Fatalf("newer exact source=%+v err=%v", source, err)
	}
}

func TestNonDirectChatNamesKeepTypeSpecificSources(t *testing.T) {
	for chatType, expected := range map[projection_model.ChatType]string{
		projection_model.ChatTypeGroup:      "group_subject",
		projection_model.ChatTypeNewsletter: "newsletter_name",
		projection_model.ChatTypeBroadcast:  "broadcast_name",
	} {
		name := "Projected name"
		incoming := projection_model.Chat{Type: chatType, DisplayName: &name}
		if err := resolveChatIdentity(nil, &incoming, &projection_model.Chat{}, true, time.Unix(100, 0).UTC()); err != nil {
			t.Fatal(err)
		}
		if incoming.ContactID != nil || incoming.DisplayNameSource == nil || *incoming.DisplayNameSource != expected || incoming.DisplayNameUpdatedAt == nil {
			t.Fatalf("%s identity = %#v", chatType, incoming)
		}
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
