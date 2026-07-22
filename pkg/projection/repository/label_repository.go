package projection_repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type LabelRepository interface {
	ApplyLabel(context.Context, *projection_model.Label) (bool, error)
	ApplyLabelMutation(context.Context, LabelMutation) (bool, error)
	ApplyChatAssociation(context.Context, *projection_model.LabelChatAssociation) (bool, error)
	ApplyMessageAssociation(context.Context, *projection_model.LabelMessageAssociation) (bool, error)
	GetLabel(context.Context, string, string) (*projection_model.Label, error)
	ListLabels(context.Context, string) ([]projection_model.Label, error)
	ListChatAssociations(context.Context, string, string) ([]projection_model.LabelChatAssociation, error)
	ListMessageAssociations(context.Context, string, string, string) ([]projection_model.LabelMessageAssociation, error)
}

type LabelMutation struct {
	InstanceID string
	LabelID    string
	Name       string
	Color      int32
	Deleted    bool
	OccurredAt time.Time
	EventKey   string
}

type labelRepository struct {
	db  *gorm.DB
	now func() time.Time
}

func NewLabelProjectionRepository(db *gorm.DB) LabelRepository {
	return &labelRepository{db: db, now: time.Now}
}

func (r *labelRepository) ApplyLabel(ctx context.Context, label *projection_model.Label) (bool, error) {
	if err := validateLabel(label); err != nil {
		return false, err
	}
	label.SourceOccurredAt = label.SourceOccurredAt.UTC()
	label.LastSyncedAt = r.now().UTC()
	result := r.db.WithContext(ctx).Clauses(orderedUpsert(
		[]clause.Column{{Name: "instance_id"}, {Name: "label_id"}},
		[]string{"name", "color", "predefined_id", "order_index", "active", "immutable", "kind", "source_occurred_at", "source_event_key", "last_synced_at", "tombstoned_at", "updated_at"},
		"projected_labels",
	)).Create(label)
	if result.Error != nil {
		return false, fmt.Errorf("apply label projection: %w", result.Error)
	}
	return result.RowsAffected > 0, nil
}

func (r *labelRepository) ApplyLabelMutation(ctx context.Context, mutation LabelMutation) (bool, error) {
	if mutation.InstanceID == "" || mutation.LabelID == "" || mutation.OccurredAt.IsZero() || mutation.EventKey == "" ||
		len(mutation.LabelID) > 255 || len(mutation.EventKey) > 255 {
		return false, errors.New("label mutation identity and source version are required")
	}
	mutation.OccurredAt = mutation.OccurredAt.UTC()
	label := &projection_model.Label{
		InstanceID: mutation.InstanceID, LabelID: mutation.LabelID, Name: &mutation.Name, Color: &mutation.Color,
		SourceOccurredAt: mutation.OccurredAt, SourceEventKey: mutation.EventKey, LastSyncedAt: r.now().UTC(),
	}
	if mutation.Deleted {
		label.TombstonedAt = &mutation.OccurredAt
	}
	result := r.db.WithContext(ctx).Clauses(orderedUpsert(
		[]clause.Column{{Name: "instance_id"}, {Name: "label_id"}},
		[]string{"name", "color", "source_occurred_at", "source_event_key", "last_synced_at", "tombstoned_at", "updated_at"},
		"projected_labels",
	)).Create(label)
	if result.Error != nil {
		return false, fmt.Errorf("apply projected label mutation: %w", result.Error)
	}
	return result.RowsAffected > 0, nil
}

func (r *labelRepository) ApplyChatAssociation(ctx context.Context, association *projection_model.LabelChatAssociation) (bool, error) {
	if err := validateChatAssociation(association); err != nil {
		return false, err
	}
	association.SourceOccurredAt = association.SourceOccurredAt.UTC()
	association.LastSyncedAt = r.now().UTC()
	result := r.db.WithContext(ctx).Clauses(orderedUpsert(
		[]clause.Column{{Name: "instance_id"}, {Name: "label_id"}, {Name: "chat_id"}},
		[]string{"source_occurred_at", "source_event_key", "last_synced_at", "tombstoned_at", "updated_at"},
		"projected_label_chat_associations",
	)).Create(association)
	if result.Error != nil {
		return false, fmt.Errorf("apply projected label chat association: %w", result.Error)
	}
	return result.RowsAffected > 0, nil
}

func (r *labelRepository) ApplyMessageAssociation(ctx context.Context, association *projection_model.LabelMessageAssociation) (bool, error) {
	if err := validateMessageAssociation(association); err != nil {
		return false, err
	}
	association.SourceOccurredAt = association.SourceOccurredAt.UTC()
	association.LastSyncedAt = r.now().UTC()
	result := r.db.WithContext(ctx).Clauses(orderedUpsert(
		[]clause.Column{{Name: "instance_id"}, {Name: "label_id"}, {Name: "chat_id"}, {Name: "message_id"}},
		[]string{"source_occurred_at", "source_event_key", "last_synced_at", "tombstoned_at", "updated_at"},
		"projected_label_message_associations",
	)).Create(association)
	if result.Error != nil {
		return false, fmt.Errorf("apply projected label message association: %w", result.Error)
	}
	return result.RowsAffected > 0, nil
}

func (r *labelRepository) GetLabel(ctx context.Context, instanceID, labelID string) (*projection_model.Label, error) {
	if instanceID == "" || labelID == "" {
		return nil, errors.New("label projection identity is required")
	}
	var label projection_model.Label
	err := r.db.WithContext(ctx).
		Where("instance_id = ? AND label_id = ? AND tombstoned_at IS NULL", instanceID, labelID).
		First(&label).Error
	return &label, err
}

func (r *labelRepository) ListLabels(ctx context.Context, instanceID string) ([]projection_model.Label, error) {
	if instanceID == "" {
		return nil, errors.New("label projection instance identity is required")
	}
	var labels []projection_model.Label
	err := r.db.WithContext(ctx).
		Where("instance_id = ? AND tombstoned_at IS NULL", instanceID).
		Order("order_index ASC NULLS LAST, label_id ASC").
		Find(&labels).Error
	return labels, err
}

func (r *labelRepository) ListChatAssociations(ctx context.Context, instanceID, chatID string) ([]projection_model.LabelChatAssociation, error) {
	if instanceID == "" || chatID == "" {
		return nil, errors.New("label chat association identity is required")
	}
	var associations []projection_model.LabelChatAssociation
	err := r.db.WithContext(ctx).
		Where("instance_id = ? AND chat_id = ? AND tombstoned_at IS NULL", instanceID, chatID).
		Order("label_id ASC").
		Find(&associations).Error
	return associations, err
}

func (r *labelRepository) ListMessageAssociations(ctx context.Context, instanceID, chatID, messageID string) ([]projection_model.LabelMessageAssociation, error) {
	if instanceID == "" || chatID == "" || messageID == "" {
		return nil, errors.New("label message association identity is required")
	}
	var associations []projection_model.LabelMessageAssociation
	err := r.db.WithContext(ctx).
		Where("instance_id = ? AND chat_id = ? AND message_id = ? AND tombstoned_at IS NULL", instanceID, chatID, messageID).
		Order("label_id ASC").
		Find(&associations).Error
	return associations, err
}

func orderedUpsert(columns []clause.Column, assignments []string, table string) clause.OnConflict {
	return clause.OnConflict{
		Columns:   columns,
		DoUpdates: clause.AssignmentColumns(assignments),
		Where: clause.Where{Exprs: []clause.Expression{clause.Expr{
			SQL: table + ".source_occurred_at < excluded.source_occurred_at OR (" +
				table + ".source_occurred_at = excluded.source_occurred_at AND " +
				table + ".source_event_key < excluded.source_event_key)",
		}}},
	}
}

func validateLabel(label *projection_model.Label) error {
	if label == nil || label.InstanceID == "" || label.LabelID == "" || label.SourceOccurredAt.IsZero() || label.SourceEventKey == "" {
		return errors.New("label projection identity and source version are required")
	}
	if len(label.LabelID) > 255 || len(label.SourceEventKey) > 255 {
		return errors.New("label projection identity exceeds storage limits")
	}
	return nil
}

func validateChatAssociation(association *projection_model.LabelChatAssociation) error {
	if association == nil || association.InstanceID == "" || association.LabelID == "" || association.ChatID == "" ||
		association.SourceOccurredAt.IsZero() || association.SourceEventKey == "" {
		return errors.New("label chat association identity and source version are required")
	}
	if len(association.LabelID) > 255 || len(association.ChatID) > 255 || len(association.SourceEventKey) > 255 {
		return errors.New("label chat association identity exceeds storage limits")
	}
	return nil
}

func validateMessageAssociation(association *projection_model.LabelMessageAssociation) error {
	if association == nil || association.InstanceID == "" || association.LabelID == "" || association.ChatID == "" || association.MessageID == "" ||
		association.SourceOccurredAt.IsZero() || association.SourceEventKey == "" {
		return errors.New("label message association identity and source version are required")
	}
	if len(association.LabelID) > 255 || len(association.ChatID) > 255 || len(association.MessageID) > 255 || len(association.SourceEventKey) > 255 {
		return errors.New("label message association identity exceeds storage limits")
	}
	return nil
}
