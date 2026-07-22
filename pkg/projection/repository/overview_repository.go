package projection_repository

import (
	"context"
	"database/sql"
	"errors"
	"time"

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	"gorm.io/gorm"
)

type OverviewCounts struct {
	InstancesTotal     int64
	InstancesConnected int64
	Groups             int64
	Contacts           int64
	Chats              int64
	Messages           int64
	MessagesIncoming   int64
	MessagesOutgoing   int64
	Events             int64
}

type OverviewRepository interface {
	Snapshot(context.Context, string, time.Time, time.Time) (*OverviewCounts, error)
}

type overviewRepository struct{ db *gorm.DB }

func NewOverviewRepository(db *gorm.DB) OverviewRepository { return &overviewRepository{db: db} }

func (r *overviewRepository) Snapshot(ctx context.Context, instanceID string, start, end time.Time) (*OverviewCounts, error) {
	if r == nil || r.db == nil || ctx == nil || start.IsZero() || end.IsZero() || !start.Before(end) {
		return nil, errors.New("overview snapshot parameters are invalid")
	}
	counts := &OverviewCounts{}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		queries := []struct {
			model      any
			idColumn   string
			conditions string
			args       []any
			target     *int64
		}{
			{&instance_model.Instance{}, "id", "", nil, &counts.InstancesTotal},
			{&instance_model.Instance{}, "id", "connected = ?", []any{true}, &counts.InstancesConnected},
			{&projection_model.Group{}, "instance_id", "tombstoned_at IS NULL", nil, &counts.Groups},
			{&projection_model.Contact{}, "instance_id", "tombstoned_at IS NULL", nil, &counts.Contacts},
			{&projection_model.Chat{}, "instance_id", "tombstoned_at IS NULL", nil, &counts.Chats},
			{&projection_model.ProjectedMessage{}, "instance_id", "deleted_at IS NULL AND provider_timestamp >= ? AND provider_timestamp < ?", []any{start.UTC(), end.UTC()}, &counts.Messages},
			{&projection_model.ProjectedMessage{}, "instance_id", "deleted_at IS NULL AND provider_timestamp >= ? AND provider_timestamp < ? AND direction = ?", []any{start.UTC(), end.UTC(), projection_model.MessageDirectionIncoming}, &counts.MessagesIncoming},
			{&projection_model.ProjectedMessage{}, "instance_id", "deleted_at IS NULL AND provider_timestamp >= ? AND provider_timestamp < ? AND direction = ?", []any{start.UTC(), end.UTC(), projection_model.MessageDirectionOutgoing}, &counts.MessagesOutgoing},
			{&projection_model.DurableEvent{}, "instance_id", "occurred_at >= ? AND occurred_at < ?", []any{start.UTC(), end.UTC()}, &counts.Events},
		}
		for _, query := range queries {
			dbQuery := tx.Model(query.model)
			if instanceID != "" {
				dbQuery = dbQuery.Where(query.idColumn+" = ?", instanceID)
			}
			if query.conditions != "" {
				dbQuery = dbQuery.Where(query.conditions, query.args...)
			}
			if err := dbQuery.Count(query.target).Error; err != nil {
				return err
			}
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return counts, err
}
