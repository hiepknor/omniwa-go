package projection_service

import (
	"context"
	"errors"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
	"github.com/google/uuid"
)

type labelWriteRepository interface {
	ApplyLabelMutation(context.Context, projection_repository.LabelMutation) (bool, error)
	ApplyChatAssociation(context.Context, *projection_model.LabelChatAssociation) (bool, error)
	ApplyMessageAssociation(context.Context, *projection_model.LabelMessageAssociation) (bool, error)
}

type labelWriteState interface {
	RecordEvent(instanceID, resource string, schemaVersion int64, occurredAt time.Time) error
	MarkStale(instanceID, resource string, schemaVersion int64) error
}

type LabelWriter struct {
	labels labelWriteRepository
	state  labelWriteState
	now    func() time.Time
}

func NewLabelWriter(labels labelWriteRepository, state labelWriteState) *LabelWriter {
	return &LabelWriter{labels: labels, state: state, now: time.Now}
}

func (w *LabelWriter) WriteLabel(ctx context.Context, instanceID, labelID, name string, color int32, deleted bool) error {
	version, err := w.version(instanceID, labelID)
	if err != nil {
		return err
	}
	_, err = w.labels.ApplyLabelMutation(ctx, projection_repository.LabelMutation{
		InstanceID: instanceID, LabelID: labelID, Name: name, Color: color, Deleted: deleted,
		OccurredAt: version.at, EventKey: version.key,
	})
	if err != nil {
		return err
	}
	return w.record(instanceID, version.at)
}

func (w *LabelWriter) WriteChatAssociation(ctx context.Context, instanceID, labelID, chatID string, labeled bool) error {
	version, err := w.version(instanceID, labelID)
	if err != nil || chatID == "" {
		if err != nil {
			return err
		}
		return errors.New("label chat association identity is required")
	}
	association := &projection_model.LabelChatAssociation{
		InstanceID: instanceID, LabelID: labelID, ChatID: chatID,
		SourceOccurredAt: version.at, SourceEventKey: version.key, TombstonedAt: labelTombstone(version.at, labeled),
	}
	if _, err := w.labels.ApplyChatAssociation(ctx, association); err != nil {
		return err
	}
	return w.record(instanceID, version.at)
}

func (w *LabelWriter) WriteMessageAssociation(ctx context.Context, instanceID, labelID, chatID, messageID string, labeled bool) error {
	version, err := w.version(instanceID, labelID)
	if err != nil || chatID == "" || messageID == "" {
		if err != nil {
			return err
		}
		return errors.New("label message association identity is required")
	}
	association := &projection_model.LabelMessageAssociation{
		InstanceID: instanceID, LabelID: labelID, ChatID: chatID, MessageID: messageID,
		SourceOccurredAt: version.at, SourceEventKey: version.key, TombstonedAt: labelTombstone(version.at, labeled),
	}
	if _, err := w.labels.ApplyMessageAssociation(ctx, association); err != nil {
		return err
	}
	return w.record(instanceID, version.at)
}

func (w *LabelWriter) MarkStale(instanceID string) error {
	if err := w.validate(instanceID); err != nil {
		return err
	}
	return w.state.MarkStale(instanceID, labelResource, LabelsProjectionSchemaVersion)
}

type labelMutationVersion struct {
	at  time.Time
	key string
}

func (w *LabelWriter) version(instanceID, labelID string) (labelMutationVersion, error) {
	if err := w.validate(instanceID); err != nil || labelID == "" {
		if err != nil {
			return labelMutationVersion{}, err
		}
		return labelMutationVersion{}, errors.New("label identity is required")
	}
	return labelMutationVersion{at: w.now().UTC(), key: "mutation:" + uuid.NewString()}, nil
}

func (w *LabelWriter) record(instanceID string, occurredAt time.Time) error {
	return w.state.RecordEvent(instanceID, labelResource, LabelsProjectionSchemaVersion, occurredAt)
}

func (w *LabelWriter) validate(instanceID string) error {
	if w == nil || w.labels == nil || w.state == nil || w.now == nil || instanceID == "" {
		return errors.New("label projection writer dependencies and instance identity are required")
	}
	return nil
}
