package projection_service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
)

type captureHistoryReadyState struct{ resources []string }

func (c *captureHistoryReadyState) MarkReady(_ string, resource string, _ int64, _ time.Time) error {
	c.resources = append(c.resources, resource)
	return nil
}

func TestHistoryReadinessWaitsForFanoutAndMarksResourcesIndependently(t *testing.T) {
	completedAt := time.Unix(900, 0).UTC()
	syncID, syncType := "sync-1", "RECENT"
	payload, err := json.Marshal(messageEventPayload{
		ChatID: "history-sync", HistorySyncID: &syncID, HistorySyncType: &syncType, CompletedAt: &completedAt,
		ChatsReady: true, MessagesReady: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	event := &projection_model.Event{
		InstanceID: "instance-a", Resource: messageResource, EventType: "history_sync_complete", EventKey: "completion-1", Payload: payload,
	}
	state, readiness := &captureHistoryReadyState{}, &captureLabelReadiness{unprocessed: true}
	projector := NewHistoryReadinessProjector(state, readiness)
	if err := projector.Handle(context.Background(), event); err == nil {
		t.Fatal("history completion ignored pending fanout events")
	}
	readiness.unprocessed = false
	if err := projector.Handle(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if len(state.resources) != 2 || state.resources[0] != "chats" || state.resources[1] != messageResource {
		t.Fatalf("ready resources = %#v", state.resources)
	}
}
