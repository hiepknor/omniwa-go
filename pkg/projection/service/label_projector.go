package projection_service

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
)

const LabelsProjectionSchemaVersion int64 = 1

type labelProjectionWriter interface {
	ApplyLabel(context.Context, *projection_model.Label) (bool, error)
	ApplyChatAssociation(context.Context, *projection_model.LabelChatAssociation) (bool, error)
	ApplyMessageAssociation(context.Context, *projection_model.LabelMessageAssociation) (bool, error)
}

type LabelProjector struct {
	labels labelProjectionWriter
	state  projectionEventState
}

func NewLabelProjector(labels labelProjectionWriter, state projectionEventState) *LabelProjector {
	return &LabelProjector{labels: labels, state: state}
}

func (p *LabelProjector) Handle(ctx context.Context, event *projection_model.Event) error {
	if p == nil || p.labels == nil || p.state == nil {
		return errors.New("label projector dependencies are required")
	}
	if event == nil || event.Resource != labelResource || event.InstanceID == "" || event.EventKey == "" {
		return errors.New("unsupported label projection event")
	}
	var payload labelEventPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return errors.New("invalid normalized label projection payload")
	}
	if payload.LabelID == "" || payload.LabelID != event.EntityKey {
		return errors.New("label projection payload identity mismatch")
	}
	var err error
	switch event.EventType {
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
			return errors.New("label chat association payload is incomplete")
		}
		association := &projection_model.LabelChatAssociation{
			InstanceID: event.InstanceID, LabelID: payload.LabelID, ChatID: payload.ChatID,
			SourceOccurredAt: event.OccurredAt, SourceEventKey: event.EventKey,
		}
		association.TombstonedAt = labelTombstone(event.OccurredAt, *payload.Labeled)
		_, err = p.labels.ApplyChatAssociation(ctx, association)
	case "label_message_association":
		if payload.ChatID == "" || payload.MessageID == "" || payload.Labeled == nil {
			return errors.New("label message association payload is incomplete")
		}
		association := &projection_model.LabelMessageAssociation{
			InstanceID: event.InstanceID, LabelID: payload.LabelID, ChatID: payload.ChatID, MessageID: payload.MessageID,
			SourceOccurredAt: event.OccurredAt, SourceEventKey: event.EventKey,
		}
		association.TombstonedAt = labelTombstone(event.OccurredAt, *payload.Labeled)
		_, err = p.labels.ApplyMessageAssociation(ctx, association)
	default:
		return errors.New("unsupported label projection event")
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
