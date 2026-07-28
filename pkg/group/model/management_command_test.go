package group_model

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestManagementCommandJSONDoesNotExposeInternalIdentityMaterial(t *testing.T) {
	value, err := json.Marshal(ManagementCommand{
		ID: "command-id", InstanceID: "instance-id", GroupJID: stringPointer("120363000001@g.us"),
		IdempotencyKeyHash: stringPointer("idempotency-secret-hash"), RequestFingerprint: "request-fingerprint",
		ActorType: "instance", ActorReferenceHash: "actor-secret-hash", SafeOutcome: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"instance-id", "idempotency-secret-hash", "request-fingerprint", "actor-secret-hash"} {
		if strings.Contains(string(value), forbidden) {
			t.Fatalf("management command JSON exposed %q: %s", forbidden, value)
		}
	}
}

func TestManagementAuditJSONDoesNotExposeScopedIdentityMaterial(t *testing.T) {
	value, err := json.Marshal(ManagementAuditEvent{
		ID: "audit-id", CommandID: "command-id", InstanceID: "instance-id", GroupJID: stringPointer("120363000001@g.us"),
		ActorReferenceHash: "actor-secret-hash", Summary: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"instance-id", "120363000001@g.us", "actor-secret-hash"} {
		if strings.Contains(string(value), forbidden) {
			t.Fatalf("management audit JSON exposed %q: %s", forbidden, value)
		}
	}
}

func stringPointer(value string) *string { return &value }
