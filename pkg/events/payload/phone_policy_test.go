package payload

import (
	"encoding/json"
	"testing"
)

func TestPhonePayloadPolicyRedactsOnlyNewDataFields(t *testing.T) {
	payload := []byte(`{"event":"Message","data":{"senderPhoneNumber":"15550001","recipientPhoneNumber":"15550002","participantPhoneNumber":"15550003","phoneNumber":"15550004","Sender":"legacy"},"phoneNumber":"outside"}`)
	result, err := NewPhonePayloadPolicy(false).Apply(payload)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(result, &root); err != nil {
		t.Fatal(err)
	}
	data := root["data"].(map[string]any)
	for _, key := range phonePayloadFields {
		if _, exists := data[key]; exists {
			t.Fatalf("%s was not redacted", key)
		}
	}
	if data["Sender"] != "legacy" || root["phoneNumber"] != "outside" {
		t.Fatalf("legacy payload changed: %#v", root)
	}
}

func TestPhonePayloadPolicyFailsClosedForMalformedJSON(t *testing.T) {
	if _, err := NewPhonePayloadPolicy(false).Apply([]byte(`{"data":`)); err == nil {
		t.Fatal("expected malformed payload rejection")
	}
}

func TestPhonePayloadPolicyEnabledPreservesPhoneFields(t *testing.T) {
	payload := []byte(`{"data":{"senderPhoneNumber":"15550001"}}`)
	result, err := NewPhonePayloadPolicy(true).Apply(payload)
	if err != nil || !json.Valid(result) {
		t.Fatalf("result=%s err=%v", result, err)
	}
	var root map[string]any
	_ = json.Unmarshal(result, &root)
	if root["data"].(map[string]any)["senderPhoneNumber"] != "15550001" {
		t.Fatalf("result=%s", result)
	}
}
