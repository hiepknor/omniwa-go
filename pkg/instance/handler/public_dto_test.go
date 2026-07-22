package instance_handler

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
)

func TestInstanceViewPreservesCompatibilityAndRedactsStorageSecrets(t *testing.T) {
	createdAt := time.Unix(100, 0).UTC()
	view := instanceView(&instance_model.Instance{
		Id: "instance-id", Name: "primary", Token: "temporary-compatible-token", TokenGeneration: 3, Webhook: "https://example.com/hook",
		RabbitmqEnable: "true", WebSocketEnable: "true", NatsEnable: "false", Jid: "15550001111@s.whatsapp.net",
		Qrcode: "pairing-secret", Connected: true, Expiration: 123, DisconnectReason: "none", Events: "MESSAGE",
		OsName: "Chrome", Proxy: `{"username":"proxy-user","password":"proxy-secret"}`, ClientName: "OmniWA", CreatedAt: createdAt,
		AlwaysOnline: true, RejectCall: true, MsgRejectCall: "busy", ReadMessages: true, IgnoreGroups: true, IgnoreStatus: true,
	})
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	serialized := string(encoded)
	if !strings.Contains(serialized, `"token":"temporary-compatible-token"`) || !strings.Contains(serialized, `"id":"instance-id"`) ||
		!strings.Contains(serialized, `"credentialVersion":3`) ||
		!strings.Contains(serialized, `"rabbitmqEnable":"true"`) || !strings.Contains(serialized, `"disconnect_reason":"none"`) ||
		!strings.Contains(serialized, `"createdAt":"1970-01-01T00:01:40Z"`) {
		t.Fatalf("compatibility fields missing: %s", serialized)
	}
	if strings.Contains(serialized, "pairing-secret") || strings.Contains(serialized, "proxy-secret") || strings.Contains(serialized, "proxy-user") ||
		!strings.Contains(serialized, `"qrcode":""`) || !strings.Contains(serialized, `"proxy":""`) {
		t.Fatalf("storage secret leaked: %s", serialized)
	}
}

func TestInstanceViewListReturnsArrayAndHandlesNilRecords(t *testing.T) {
	views := instanceViewList(nil)
	encoded, err := json.Marshal(views)
	if err != nil || string(encoded) != "[]" {
		t.Fatalf("empty list = %s, %v", encoded, err)
	}
	views = instanceViewList([]*instance_model.Instance{nil, {Id: "instance-id"}})
	if len(views) != 2 || views[0].ID != "" || views[1].ID != "instance-id" {
		t.Fatalf("mapped list = %#v", views)
	}
}
