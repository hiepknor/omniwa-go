package projection_model

import (
	"encoding/json"
	"testing"
)

func TestEventWorkerDataIsNotPubliclySerialized(t *testing.T) {
	claim := "secret-claim"
	event := Event{InstanceID: "instance-a", Payload: json.RawMessage(`{"secret":true}`), ClaimToken: &claim}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != "{}" {
		t.Fatalf("internal event data was serialized: %s", encoded)
	}
}
