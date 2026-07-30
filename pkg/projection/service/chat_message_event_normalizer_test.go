package projection_service

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	waE2E "go.mau.fi/whatsmeow/proto/waE2E"
	waSyncAction "go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestNormalizeMessageEventStoresBoundedNormalizedMetadata(t *testing.T) {
	occurredAt := time.Unix(500, 0).UTC()
	raw := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat: types.NewJID("group", types.GroupServer), Sender: types.NewJID("sender", types.DefaultUserServer), IsGroup: true,
			},
			ID: "message-500", Type: "image", Timestamp: occurredAt,
		},
		Message: &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
			URL: proto.String("https://provider.invalid/secret"), DirectPath: proto.String("/encrypted/provider/path"),
			Mimetype: proto.String("image/jpeg"), Caption: proto.String(strings.Repeat("é", 5000)),
			FileLength: proto.Uint64(2048), Width: proto.Uint32(640), Height: proto.Uint32(480),
			MediaKey: []byte("secret-key"), ContextInfo: &waE2E.ContextInfo{StanzaID: proto.String("quoted-1")},
		}},
	}
	first, relevant, err := NormalizeChatMessageEvent("instance-a", raw)
	if err != nil || !relevant {
		t.Fatalf("NormalizeChatMessageEvent() = %#v, %v, %v", first, relevant, err)
	}
	second, _, err := NormalizeChatMessageEvent("instance-a", raw)
	if err != nil || first.EventKey != second.EventKey {
		t.Fatalf("message event key is unstable: %q/%q, %v", first.EventKey, second.EventKey, err)
	}
	if first.Resource != messageResource || first.EventType != "message" || first.EntityKey != "message-500" || !first.OccurredAt.Equal(occurredAt) {
		t.Fatalf("normalized message event = %#v", first)
	}
	if bytes.Contains(first.Payload, []byte("provider.invalid")) || bytes.Contains(first.Payload, []byte("encrypted/provider")) || bytes.Contains(first.Payload, []byte("secret-key")) {
		t.Fatalf("normalized payload leaked provider-native secrets: %s", first.Payload)
	}
	var payload messageEventPayload
	if err := json.Unmarshal(first.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ChatType != "group" || payload.Direction != "incoming" || payload.MediaType == nil || *payload.MediaType != "image" ||
		payload.MediaSize == nil || *payload.MediaSize != 2048 || payload.MediaWidth == nil || *payload.MediaWidth != 640 ||
		payload.QuotedMessageID == nil || *payload.QuotedMessageID != "quoted-1" || payload.Caption == nil || len(*payload.Caption) > 8*1024 || !utf8.ValidString(*payload.Caption) {
		t.Fatalf("normalized message payload = %#v", payload)
	}
}

func TestNormalizeChatSettingEventsPreservesAuthoritativeState(t *testing.T) {
	jid := types.NewJID("15550001", types.DefaultUserServer)
	occurredAt := time.Unix(900, 0).UTC()
	muteEnd := occurredAt.Add(2 * time.Hour)
	tests := []struct {
		name      string
		raw       any
		eventType string
		assert    func(*messageEventPayload)
	}{
		{name: "archive", raw: &events.Archive{JID: jid, Timestamp: occurredAt, Action: &waSyncAction.ArchiveChatAction{Archived: proto.Bool(true)}}, eventType: "chat_archived", assert: func(payload *messageEventPayload) {
			if payload.Archived == nil || !*payload.Archived {
				t.Fatalf("payload=%+v", payload)
			}
		}},
		{name: "pin", raw: &events.Pin{JID: jid, Timestamp: occurredAt, Action: &waSyncAction.PinAction{Pinned: proto.Bool(false)}}, eventType: "chat_pinned", assert: func(payload *messageEventPayload) {
			if payload.Pinned == nil || *payload.Pinned {
				t.Fatalf("payload=%+v", payload)
			}
		}},
		{name: "finite mute", raw: &events.Mute{JID: jid, Timestamp: occurredAt, Action: &waSyncAction.MuteAction{Muted: proto.Bool(true), MuteEndTimestamp: proto.Int64(muteEnd.UnixMilli())}}, eventType: "chat_muted", assert: func(payload *messageEventPayload) {
			if payload.MutedUntil == nil || !payload.MutedUntil.Equal(muteEnd) {
				t.Fatalf("payload=%+v", payload)
			}
		}},
		{name: "unmute", raw: &events.Mute{JID: jid, Timestamp: occurredAt, Action: &waSyncAction.MuteAction{Muted: proto.Bool(false)}}, eventType: "chat_muted", assert: func(payload *messageEventPayload) {
			if payload.MutedUntil != nil {
				t.Fatalf("payload=%+v", payload)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event, relevant, err := NormalizeChatMessageEvent("instance-a", test.raw)
			if err != nil || !relevant || event.EventType != test.eventType || event.EntityKey != jid.String() || !event.OccurredAt.Equal(occurredAt) {
				t.Fatalf("event=%+v relevant=%t err=%v", event, relevant, err)
			}
			var payload messageEventPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			test.assert(&payload)
		})
	}
	infinite, relevant, err := NormalizeChatMessageEvent("instance-a", &events.Mute{JID: jid, Timestamp: occurredAt, Action: &waSyncAction.MuteAction{Muted: proto.Bool(true)}})
	if err != nil || relevant || infinite != nil {
		t.Fatalf("infinite mute entered finite canonical projection: event=%+v relevant=%t err=%v", infinite, relevant, err)
	}
}

func TestAttachInboundMediaAssetChangesOnlyOpaqueProjectionLink(t *testing.T) {
	raw := &events.Message{
		Info:    types.MessageInfo{MessageSource: types.MessageSource{Chat: types.NewJID("group", types.GroupServer)}, ID: "message-media", Timestamp: time.Now().UTC()},
		Message: &waE2E.Message{ImageMessage: &waE2E.ImageMessage{DirectPath: proto.String("/provider/secret"), MediaKey: []byte("provider-secret")}},
	}
	event, relevant, err := NormalizeChatMessageEvent("instance-a", raw)
	if err != nil || !relevant {
		t.Fatalf("normalize=%+v/%t/%v", event, relevant, err)
	}
	beforeKey := event.EventKey
	assetID := "927beb51-46c2-4331-b3b4-d96f67280bd3"
	attached, err := AttachInboundMediaAsset(event, assetID)
	if err != nil {
		t.Fatal(err)
	}
	var payload messageEventPayload
	if err := json.Unmarshal(attached.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.MediaAssetID == nil || *payload.MediaAssetID != assetID || attached.EventKey == beforeKey ||
		bytes.Contains(attached.Payload, []byte("provider/secret")) || bytes.Contains(attached.Payload, []byte("provider-secret")) {
		t.Fatalf("attached event=%+v payload=%s", attached, attached.Payload)
	}
}

func TestNormalizeReceiptEventDeduplicatesAndSortsMessageIDs(t *testing.T) {
	raw := &events.Receipt{
		MessageSource: types.MessageSource{
			Chat: types.NewJID("15550001", types.DefaultUserServer), Sender: types.NewJID("15550001", types.DefaultUserServer),
		},
		MessageIDs: []types.MessageID{"message-b", "message-a", "message-b"}, Timestamp: time.Unix(600, 0), Type: types.ReceiptTypeDelivered,
	}
	event, relevant, err := NormalizeProjectionEvent("instance-a", raw)
	if err != nil || !relevant {
		t.Fatalf("NormalizeProjectionEvent() = %#v, %v, %v", event, relevant, err)
	}
	var payload messageEventPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if event.Resource != messageResource || event.EventType != "receipt" || payload.ReceiptType != "delivered" ||
		len(payload.MessageIDs) != 2 || payload.MessageIDs[0] != "message-a" || payload.MessageIDs[1] != "message-b" {
		t.Fatalf("normalized receipt = %#v payload=%#v", event, payload)
	}
	if ignored, relevant, err := NormalizeChatMessageEvent("instance-a", &events.Receipt{Type: types.ReceiptTypeInactive}); err != nil || relevant || ignored != nil {
		t.Fatalf("unsupported receipt entered projection: %#v, %v, %v", ignored, relevant, err)
	}
}
