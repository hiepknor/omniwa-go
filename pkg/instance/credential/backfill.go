package instance_credential

import (
	"context"
	"errors"
)

type BackfillRepository interface {
	BackfillTokenDigests(ctx context.Context, batchSize int) (int, error)
}

type BackfillResult struct {
	Updated  int
	Complete bool
	Batches  int
}

// RunBoundedBackfill performs finite, restartable work. Complete is false when
// the configured batch budget was exhausted, so a later run can resume safely.
func RunBoundedBackfill(ctx context.Context, repository BackfillRepository, batchSize, maxBatches int) (BackfillResult, error) {
	if repository == nil {
		return BackfillResult{}, errors.New("token digest backfill repository is required")
	}
	if batchSize <= 0 || maxBatches <= 0 {
		return BackfillResult{}, errors.New("token digest backfill bounds must be positive")
	}
	result := BackfillResult{}
	for result.Batches < maxBatches {
		updated, err := repository.BackfillTokenDigests(ctx, batchSize)
		if err != nil {
			return result, err
		}
		result.Batches++
		result.Updated += updated
		if updated < batchSize {
			result.Complete = true
			return result, nil
		}
	}
	return result, nil
}
