package projection_service

import (
	"context"
	"encoding/json"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
)

var historyProjectionEventTypes = []string{"history_chat", "history_message"}

type historyReadinessState interface {
	MarkReady(instanceID, resource string, schemaVersion int64, reconciledAt time.Time) error
}

type HistoryReadinessProjector struct {
	state     historyReadinessState
	readiness projectionReadinessBarrier
}

func NewHistoryReadinessProjector(state historyReadinessState, readiness projectionReadinessBarrier) *HistoryReadinessProjector {
	return &HistoryReadinessProjector{state: state, readiness: readiness}
}

func (p *HistoryReadinessProjector) Handle(ctx context.Context, event *projection_model.Event) error {
	if p == nil || p.state == nil || p.readiness == nil {
		return permanentProjectionFailure(errorCodeMisconfigured)
	}
	if event == nil || event.Resource != messageResource || event.EventType != "history_sync_complete" || event.InstanceID == "" || event.EventKey == "" {
		return permanentProjectionFailure(errorCodeUnsupportedEvent)
	}
	var payload messageEventPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return permanentProjectionFailure(errorCodeInvalidPayload)
	}
	if payload.HistorySyncID == nil || *payload.HistorySyncID == "" || payload.HistorySyncType == nil ||
		payload.CompletedAt == nil || payload.CompletedAt.IsZero() || (!payload.ChatsReady && !payload.MessagesReady) {
		return permanentProjectionFailure(errorCodeIncompletePayload)
	}
	unprocessed, err := p.readiness.HasUnprocessedEvents(ctx, event.InstanceID, messageResource, historyProjectionEventTypes, event.EventKey)
	if err != nil {
		return err
	}
	if unprocessed {
		return retryableProjectionFailure(errorCodeDependencyPending)
	}
	if payload.ChatsReady {
		if err := p.state.MarkReady(event.InstanceID, "chats", ChatsProjectionSchemaVersion, payload.CompletedAt.UTC()); err != nil {
			return err
		}
	}
	if payload.MessagesReady {
		if err := p.state.MarkReady(event.InstanceID, messageResource, MessagesProjectionSchemaVersion, payload.CompletedAt.UTC()); err != nil {
			return err
		}
	}
	return nil
}
