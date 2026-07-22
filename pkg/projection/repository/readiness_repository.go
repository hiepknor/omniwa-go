package projection_repository

import (
	"context"
	"errors"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	"gorm.io/gorm"
)

type ReadinessRepository interface {
	HasUnprocessedEvents(context.Context, string, string, []string, string) (bool, error)
}

type readinessRepository struct{ db *gorm.DB }

func NewReadinessRepository(db *gorm.DB) ReadinessRepository {
	return &readinessRepository{db: db}
}

func (r *readinessRepository) HasUnprocessedEvents(ctx context.Context, instanceID, resource string, eventTypes []string, excludedEventKey string) (bool, error) {
	if instanceID == "" || resource == "" || len(eventTypes) == 0 || excludedEventKey == "" {
		return false, errors.New("projection readiness query is incomplete")
	}
	var count int64
	err := r.db.WithContext(ctx).Model(&projection_model.Event{}).
		Where("instance_id = ? AND resource = ? AND event_type IN ? AND event_key <> ? AND status <> ?", instanceID, resource, eventTypes, excludedEventKey, projection_model.EventStatusProcessed).
		Limit(1).Count(&count).Error
	return count > 0, err
}
