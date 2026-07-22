package projection_service

import (
	"bytes"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func TestNormalizeGroupInfoIsStableAndPreservesProviderTime(t *testing.T) {
	locked := &types.GroupLocked{IsLocked: false}
	occurredAt := time.Date(2025, 3, 4, 5, 6, 7, 8, time.FixedZone("test", 2*60*60))
	raw := &events.GroupInfo{
		JID: types.NewJID("12345", types.GroupServer), Timestamp: occurredAt, Locked: locked,
		Join: []types.JID{types.NewJID("222", types.DefaultUserServer), types.NewJID("111", types.DefaultUserServer)}, ParticipantVersionID: "v2",
	}
	first, relevant, err := NormalizeGroupEvent("instance-a", raw)
	if err != nil || !relevant {
		t.Fatalf("NormalizeGroupEvent() = %#v, %v, %v", first, relevant, err)
	}
	reordered := *raw
	reordered.Join = []types.JID{raw.Join[1], raw.Join[0]}
	second, _, err := NormalizeGroupEvent("instance-a", &reordered)
	if err != nil {
		t.Fatal(err)
	}
	if first.EventKey != second.EventKey {
		t.Fatalf("event key is not stable: %q != %q", first.EventKey, second.EventKey)
	}
	if first.EntityKey != "12345@g.us" || first.EventType != "group_info" || !first.OccurredAt.Equal(occurredAt) || first.OccurredAt.Location() != time.UTC {
		t.Fatalf("unexpected normalized event: %#v", first)
	}
	if !bytes.Contains(first.Payload, []byte(`"locked":false`)) || !bytes.Contains(first.Payload, []byte(`"joinedParticipants":["111@s.whatsapp.net","222@s.whatsapp.net"]`)) {
		t.Fatalf("normalized payload lost an explicit change: %s", first.Payload)
	}
}

func TestNormalizeJoinedGroupUsesStableUnknownOccurrenceAndExcludesUnknownEvents(t *testing.T) {
	raw := &events.JoinedGroup{
		Type: "new", CreateKey: "create-1",
		GroupInfo: types.GroupInfo{JID: types.NewJID("12345", types.GroupServer)},
	}
	event, relevant, err := NormalizeGroupEvent("instance-a", raw)
	if err != nil || !relevant {
		t.Fatalf("NormalizeGroupEvent() = %#v, %v, %v", event, relevant, err)
	}
	if event.EventType != "joined_group" || !event.OccurredAt.Equal(time.Unix(0, 0)) || !bytes.Contains(event.Payload, []byte(`"createKey":"create-1"`)) {
		t.Fatalf("unexpected joined event: %#v payload=%s", event, event.Payload)
	}
	if event, relevant, err := NormalizeGroupEvent("instance-a", struct{}{}); err != nil || relevant || event != nil {
		t.Fatalf("unknown event was accepted: %#v, %v, %v", event, relevant, err)
	}
}

func TestNormalizeGroupEventRejectsMissingIdentity(t *testing.T) {
	if _, relevant, err := NormalizeGroupEvent("instance-a", &events.GroupInfo{}); err == nil || !relevant {
		t.Fatalf("missing group identity = relevant %v, error %v", relevant, err)
	}
}
