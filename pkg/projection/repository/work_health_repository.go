package projection_repository

import (
	"errors"
	"time"

	"gorm.io/gorm"
)

type ProjectionWorkHealth struct {
	InstanceID          string
	Resource            string
	PendingEvents       int64
	ProcessingEvents    int64
	FailedEvents        int64
	DeadLetterEvents    int64
	OldestUnprocessedAt *time.Time
}

type WorkHealthRepository interface {
	Get(instanceID, resource string) (*ProjectionWorkHealth, error)
	List(instanceID string) ([]ProjectionWorkHealth, error)
}

type workHealthRepository struct{ db *gorm.DB }

func NewWorkHealthRepository(db *gorm.DB) WorkHealthRepository {
	return &workHealthRepository{db: db}
}

const projectionWorkHealthSelect = `SELECT instance_id, resource,
    COUNT(*) FILTER (WHERE status = 'pending') AS pending_events,
    COUNT(*) FILTER (WHERE status = 'processing') AS processing_events,
    COUNT(*) FILTER (WHERE status = 'failed') AS failed_events,
    COUNT(*) FILTER (WHERE status = 'dead_letter') AS dead_letter_events,
    MIN(ingested_at) FILTER (WHERE status <> 'processed') AS oldest_unprocessed_at
FROM projection_event_inbox`

func (r *workHealthRepository) Get(instanceID, resource string) (*ProjectionWorkHealth, error) {
	if r == nil || r.db == nil || instanceID == "" || resource == "" {
		return nil, errors.New("projection work health identity is required")
	}
	var result ProjectionWorkHealth
	err := r.db.Raw(projectionWorkHealthSelect+`
WHERE instance_id = ? AND resource = ? AND status <> 'processed'
GROUP BY instance_id, resource`, instanceID, resource).Scan(&result).Error
	return &result, err
}

func (r *workHealthRepository) List(instanceID string) ([]ProjectionWorkHealth, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("projection work health repository is required")
	}
	query := projectionWorkHealthSelect
	arguments := []any{}
	if instanceID != "" {
		query += ` WHERE instance_id = ? AND status <> 'processed'`
		arguments = append(arguments, instanceID)
	} else {
		query += ` WHERE status <> 'processed'`
	}
	query += ` GROUP BY instance_id, resource ORDER BY instance_id, resource`
	var results []ProjectionWorkHealth
	return results, r.db.Raw(query, arguments...).Scan(&results).Error
}
