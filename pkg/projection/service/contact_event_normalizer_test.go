package projection_service

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	waSyncAction "go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func TestNormalizeContactEventLinksLIDAndPhoneIdentityDeterministically(t *testing.T) {
	phoneJID, lidJID := "15550001@s.whatsapp.net", "lid-contact@lid"
	firstName, fullName, username := "Ada", "Ada Lovelace", "ada"
	raw := &events.Contact{
		JID: types.NewJID("lid-contact", types.HiddenUserServer), Timestamp: time.Unix(200, 0),
		Action: &waSyncAction.ContactAction{FirstName: &firstName, FullName: &fullName, PnJID: &phoneJID, LidJID: &lidJID, Username: &username},
	}
	first, relevant, err := NormalizeContactEvent("instance-a", raw)
	if err != nil || !relevant {
		t.Fatalf("NormalizeContactEvent() = %#v, %v, %v", first, relevant, err)
	}
	second, _, err := NormalizeContactEvent("instance-a", raw)
	if err != nil || first.EventKey != second.EventKey {
		t.Fatalf("contact event key is unstable: %q/%q, %v", first.EventKey, second.EventKey, err)
	}
	if first.Resource != contactResource || first.EventType != "contact" || first.EntityKey != phoneJID || !first.OccurredAt.Equal(time.Unix(200, 0)) {
		t.Fatalf("normalized contact = %#v", first)
	}
	for _, expected := range [][]byte{[]byte(`"preferredJid":"15550001@s.whatsapp.net"`), []byte(`"kind":"phone_jid"`), []byte(`"kind":"lid"`), []byte(`"username":"ada"`)} {
		if !bytes.Contains(first.Payload, expected) {
			t.Fatalf("contact payload %s does not contain %s", first.Payload, expected)
		}
	}
	var payload contactEventPayload
	if err := json.Unmarshal(first.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]struct{}, len(payload.Identities))
	for _, identity := range payload.Identities {
		key := string(identity.Kind) + "\x00" + identity.Value
		if _, duplicate := seen[key]; duplicate {
			t.Fatalf("duplicate normalized identity: %#v", identity)
		}
		seen[key] = struct{}{}
	}
}

func TestNormalizeContactChangesPreservesMessageAndProviderTimes(t *testing.T) {
	messageTime := time.Unix(300, 0)
	push, relevant, err := NormalizeContactEvent("instance-a", &events.PushName{
		JID: types.NewJID("15550001", types.DefaultUserServer), JIDAlt: types.NewJID("lid-contact", types.HiddenUserServer),
		Message: &types.MessageInfo{Timestamp: messageTime}, NewPushName: "Ada",
	})
	if err != nil || !relevant || push.EventType != "push_name" || !push.OccurredAt.Equal(messageTime) || !bytes.Contains(push.Payload, []byte(`"lid"`)) {
		t.Fatalf("normalized push name = %#v payload=%s, %v, %v", push, push.Payload, relevant, err)
	}
	pictureTime := time.Unix(400, 0)
	picture, relevant, err := NormalizeContactEvent("instance-a", &events.Picture{
		JID: types.NewJID("15550001", types.DefaultUserServer), Timestamp: pictureTime, PictureID: "picture-1",
	})
	if err != nil || !relevant || picture.EventType != "picture" || !picture.OccurredAt.Equal(pictureTime) {
		t.Fatalf("normalized picture = %#v, %v, %v", picture, relevant, err)
	}
	if event, relevant, err := NormalizeContactEvent("instance-a", &events.Picture{JID: types.NewJID("group", types.GroupServer)}); err != nil || relevant || event != nil {
		t.Fatalf("group picture entered contacts projection: %#v, %v, %v", event, relevant, err)
	}
}

func TestNormalizeProjectionEventRoutesContactEvents(t *testing.T) {
	event, relevant, err := NormalizeProjectionEvent("instance-a", &events.UserAbout{
		JID: types.NewJID("15550001", types.DefaultUserServer), Status: "Available", Timestamp: time.Unix(500, 0),
	})
	if err != nil || !relevant || event.Resource != contactResource || event.EventType != "user_about" {
		t.Fatalf("projection event routing = %#v, %v, %v", event, relevant, err)
	}
}
