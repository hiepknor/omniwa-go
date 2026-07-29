package projection_service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
)

type captureHistoryReadyState struct{ resources []string }

func (c *captureHistoryReadyState) MarkReady(_ string, resource string, _ int64, _ time.Time) error {
	c.resources = append(c.resources, resource)
	return nil
}

type captureUnreadSnapshot struct {
	instanceID string
	err        error
}

func (c *captureUnreadSnapshot) ReconcileUnreadSnapshots(_ context.Context, instanceID string) error {
	c.instanceID = instanceID
	return c.err
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
	unread := &captureUnreadSnapshot{}
	projector := NewHistoryReadinessProjector(state, readiness).WithCanonicalUnread(unread)
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
	if unread.instanceID != "instance-a" {
		t.Fatalf("unread snapshot = %#v", unread)
	}
	state.resources = nil
	unread.err = errors.New("snapshot failed")
	if err := projector.Handle(context.Background(), event); err == nil || len(state.resources) != 1 || state.resources[0] != "chats" {
		t.Fatalf("snapshot failure should not mark messages ready: resources=%#v err=%v", state.resources, err)
	}
}
