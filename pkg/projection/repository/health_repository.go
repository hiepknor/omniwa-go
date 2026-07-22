package projection_repository

import (
	"context"
	"errors"

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	"gorm.io/gorm"
)

type InstanceHealthRecord struct {
	InstanceID string
	Connected  bool
}

type HealthRepository interface {
	ListInstances(context.Context, string) ([]InstanceHealthRecord, error)
}

type healthRepository struct{ db *gorm.DB }

func NewHealthRepository(db *gorm.DB) HealthRepository { return &healthRepository{db: db} }

func (r *healthRepository) ListInstances(ctx context.Context, instanceID string) ([]InstanceHealthRecord, error) {
	if r == nil || r.db == nil || ctx == nil {
		return nil, errors.New("health repository parameters are invalid")
	}
	query := r.db.WithContext(ctx).Model(&instance_model.Instance{}).Select("id AS instance_id, connected")
	if instanceID != "" {
		query = query.Where("id = ?", instanceID)
	}
	var records []InstanceHealthRecord
	return records, query.Order("id ASC").Scan(&records).Error
}
