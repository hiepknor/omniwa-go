package projection_service

import (
	"context"
	"encoding/json"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
)

const LabelsProjectionSchemaVersion int64 = 1

type labelProjectionWriter interface {
	ApplyLabel(context.Context, *projection_model.Label) (bool, error)
	ApplyChatAssociation(context.Context, *projection_model.LabelChatAssociation) (bool, error)
	ApplyMessageAssociation(context.Context, *projection_model.LabelMessageAssociation) (bool, error)
}

type labelProjectionState interface {
	RecordEvent(instanceID, resource string, schemaVersion int64, occurredAt time.Time) error
	MarkReady(instanceID, resource string, schemaVersion int64, reconciledAt time.Time) error
}

type projectionReadinessBarrier interface {
	HasUnprocessedEvents(context.Context, string, string, []string, string) (bool, error)
}

var labelMutationEventTypes = []string{"label_edit", "label_chat_association", "label_message_association"}

type LabelProjector struct {
	labels    labelProjectionWriter
	state     labelProjectionState
	readiness projectionReadinessBarrier
}

func NewLabelProjector(labels labelProjectionWriter, state labelProjectionState, readiness projectionReadinessBarrier) *LabelProjector {
	return &LabelProjector{labels: labels, state: state, readiness: readiness}
}

func (p *LabelProjector) Handle(ctx context.Context, event *projection_model.Event) error {
	if p == nil || p.labels == nil || p.state == nil || p.readiness == nil {
		return permanentProjectionFailure(errorCodeMisconfigured)
	}
	if event == nil || event.Resource != labelResource || event.InstanceID == "" || event.EventKey == "" {
		return permanentProjectionFailure(errorCodeUnsupportedEvent)
	}
	var payload labelEventPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return permanentProjectionFailure(errorCodeInvalidPayload)
	}
	if payload.LabelID == "" || payload.LabelID != event.EntityKey {
		return permanentProjectionFailure(errorCodeIdentityMismatch)
	}
	var err error
	switch event.EventType {
	case "label_sync_complete":
		if payload.Collection != "regular" || payload.CompletedAt.IsZero() {
			return permanentProjectionFailure(errorCodeIncompletePayload)
		}
		unprocessed, err := p.readiness.HasUnprocessedEvents(ctx, event.InstanceID, labelResource, labelMutationEventTypes, event.EventKey)
		if err != nil {
			return err
		}
		if unprocessed {
			return retryableProjectionFailure(errorCodeDependencyPending)
		}
		return p.state.MarkReady(event.InstanceID, labelResource, LabelsProjectionSchemaVersion, payload.CompletedAt)
	case "label_edit":
		label := &projection_model.Label{
			InstanceID: event.InstanceID, LabelID: payload.LabelID, Name: payload.Name, Color: payload.Color,
			PredefinedID: payload.PredefinedID, OrderIndex: payload.OrderIndex, Active: payload.Active,
			Immutable: payload.Immutable, Kind: payload.Kind, SourceOccurredAt: event.OccurredAt, SourceEventKey: event.EventKey,
		}
		if payload.Deleted != nil && *payload.Deleted {
			tombstonedAt := event.OccurredAt.UTC()
			label.TombstonedAt = &tombstonedAt
		}
		_, err = p.labels.ApplyLabel(ctx, label)
	case "label_chat_association":
		if payload.ChatID == "" || payload.Labeled == nil {
			return permanentProjectionFailure(errorCodeIncompletePayload)
		}
		association := &projection_model.LabelChatAssociation{
			InstanceID: event.InstanceID, LabelID: payload.LabelID, ChatID: payload.ChatID,
			SourceOccurredAt: event.OccurredAt, SourceEventKey: event.EventKey,
		}
		association.TombstonedAt = labelTombstone(event.OccurredAt, *payload.Labeled)
		_, err = p.labels.ApplyChatAssociation(ctx, association)
	case "label_message_association":
		if payload.ChatID == "" || payload.MessageID == "" || payload.Labeled == nil {
			return permanentProjectionFailure(errorCodeIncompletePayload)
		}
		association := &projection_model.LabelMessageAssociation{
			InstanceID: event.InstanceID, LabelID: payload.LabelID, ChatID: payload.ChatID, MessageID: payload.MessageID,
			SourceOccurredAt: event.OccurredAt, SourceEventKey: event.EventKey,
		}
		association.TombstonedAt = labelTombstone(event.OccurredAt, *payload.Labeled)
		_, err = p.labels.ApplyMessageAssociation(ctx, association)
	default:
		return permanentProjectionFailure(errorCodeUnsupportedEvent)
	}
	if err != nil {
		return err
	}
	return p.state.RecordEvent(event.InstanceID, labelResource, LabelsProjectionSchemaVersion, event.OccurredAt)
}

func labelTombstone(occurredAt time.Time, labeled bool) *time.Time {
	if labeled {
		return nil
	}
	value := occurredAt.UTC()
	return &value
}
