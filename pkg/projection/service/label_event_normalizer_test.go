package projection_service

import (
	"bytes"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/appstate"
	waSyncAction "go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func TestNormalizeLabelEventsPreservesExplicitValuesAndStableKeys(t *testing.T) {
	name, color, deleted, active := "Priority", int32(0), false, false
	kind := waSyncAction.LabelEditAction_CUSTOM
	occurredAt := time.Date(2025, 4, 5, 6, 7, 8, 9, time.FixedZone("test", 2*60*60))
	raw := &events.LabelEdit{
		Timestamp: occurredAt, LabelID: "label-1",
		Action: &waSyncAction.LabelEditAction{Name: &name, Color: &color, Deleted: &deleted, IsActive: &active, Type: &kind},
	}
	first, relevant, err := NormalizeLabelEvent("instance-a", raw)
	if err != nil || !relevant {
		t.Fatalf("NormalizeLabelEvent() = %#v, %v, %v", first, relevant, err)
	}
	second, _, err := NormalizeLabelEvent("instance-a", raw)
	if err != nil || first.EventKey != second.EventKey {
		t.Fatalf("label event key is unstable: %q != %q, %v", first.EventKey, second.EventKey, err)
	}
	if first.Resource != labelResource || first.EventType != "label_edit" || first.EntityKey != "label-1" || !first.OccurredAt.Equal(occurredAt) {
		t.Fatalf("unexpected normalized label event: %#v", first)
	}
	if !bytes.Contains(first.Payload, []byte(`"color":0`)) || !bytes.Contains(first.Payload, []byte(`"active":false`)) || !bytes.Contains(first.Payload, []byte(`"kind":"custom"`)) {
		t.Fatalf("normalized label payload lost explicit values: %s", first.Payload)
	}
}

func TestNormalizeLabelSyncCompletionIsStableAndCollectionScoped(t *testing.T) {
	first, relevant, err := NormalizeLabelEvent("instance-a", &events.AppStateSyncComplete{Name: appstate.WAPatchRegular, Version: 7})
	if err != nil || !relevant {
		t.Fatalf("label sync completion = %#v, %v, %v", first, relevant, err)
	}
	second, _, err := NormalizeLabelEvent("instance-a", &events.AppStateSyncComplete{Name: appstate.WAPatchRegular, Version: 7})
	if err != nil || first.EventKey != second.EventKey || first.EventType != "label_sync_complete" || first.OccurredAt.Year() != 9999 {
		t.Fatalf("unstable label sync completion: %#v / %#v / %v", first, second, err)
	}
	if event, relevant, err := NormalizeLabelEvent("instance-a", &events.AppStateSyncComplete{Name: appstate.WAPatchRegularHigh, Version: 7}); err != nil || relevant || event != nil {
		t.Fatalf("unrelated collection was accepted: %#v, %v, %v", event, relevant, err)
	}
}

func TestNormalizeLabelAssociationsAndRejectsIncompleteEvents(t *testing.T) {
	labeled := true
	chat := types.NewJID("12345", types.DefaultUserServer)
	event, relevant, err := NormalizeProjectionEvent("instance-a", &events.LabelAssociationMessage{
		JID: chat, Timestamp: time.Unix(100, 0), LabelID: "label-1", MessageID: "message-1",
		Action: &waSyncAction.LabelAssociationAction{Labeled: &labeled},
	})
	if err != nil || !relevant || event.EventType != "label_message_association" ||
		!bytes.Contains(event.Payload, []byte(`"chatId":"12345@s.whatsapp.net"`)) || !bytes.Contains(event.Payload, []byte(`"labeled":true`)) {
		t.Fatalf("normalized message association = %#v payload=%s relevant=%v error=%v", event, event.Payload, relevant, err)
	}
	if _, relevant, err := NormalizeLabelEvent("instance-a", &events.LabelAssociationChat{LabelID: "label-1", JID: chat}); err == nil || !relevant {
		t.Fatalf("missing association action = relevant %v, error %v", relevant, err)
	}
	if event, relevant, err := NormalizeLabelEvent("instance-a", struct{}{}); err != nil || relevant || event != nil {
		t.Fatalf("unknown event was accepted: %#v, %v, %v", event, relevant, err)
	}
}
