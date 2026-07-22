package projection_repository

import (
	"encoding/json"
	"testing"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
)

func TestValidateEventRejectsInvalidAndOversizedPayloads(t *testing.T) {
	event := &projection_model.Event{
		InstanceID: "instance-a", Resource: "groups", EventKey: "event-1", EntityKey: "group-1",
		EventType: "group_info", OccurredAt: time.Now(), Payload: json.RawMessage(`{"id":"group-1"}`),
	}
	if err := validateEvent(event); err != nil {
		t.Fatalf("valid event rejected: %v", err)
	}
	event.Payload = json.RawMessage(`{"broken"`)
	if err := validateEvent(event); err == nil {
		t.Fatal("invalid JSON payload accepted")
	}
	event.Payload = make(json.RawMessage, maxEventPayloadSize+1)
	if err := validateEvent(event); err == nil {
		t.Fatal("oversized payload accepted")
	}
}
