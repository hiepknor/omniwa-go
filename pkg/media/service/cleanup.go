package media_service

import (
	"context"
	"errors"
	"time"

	media_model "github.com/evolution-foundation/evolution-go/pkg/media/model"
	storage_interfaces "github.com/evolution-foundation/evolution-go/pkg/storage/interfaces"
)

type cleanupRepository interface {
	ClaimExpired(context.Context, int, time.Duration) ([]media_model.Asset, error)
	ListVariants(context.Context, string, string) ([]media_model.AssetVariant, error)
	CompleteCleanup(context.Context, *media_model.Asset) error
	ReleaseCleanup(context.Context, *media_model.Asset) error
}

type CleanupResultHandler func(cleaned int, err error)

type CleanupWorker struct {
	repository cleanupRepository
	store      storage_interfaces.MediaAssetStore
	batchSize  int
	lease      time.Duration
	interval   time.Duration
	onResult   CleanupResultHandler
}

func NewCleanupWorker(repository cleanupRepository, store storage_interfaces.MediaAssetStore, batchSize int, lease, interval time.Duration, onResult CleanupResultHandler) *CleanupWorker {
	return &CleanupWorker{repository: repository, store: store, batchSize: batchSize, lease: lease, interval: interval, onResult: onResult}
}

func (w *CleanupWorker) RunOnce(ctx context.Context) (int, error) {
	if err := w.validate(ctx); err != nil {
		return 0, err
	}
	assets, err := w.repository.ClaimExpired(ctx, w.batchSize, w.lease)
	if err != nil {
		return 0, err
	}
	cleaned := 0
	var failures []error
	for index := range assets {
		asset := &assets[index]
		variants, listErr := w.repository.ListVariants(ctx, asset.InstanceID, asset.ID)
		if listErr != nil {
			failures = append(failures, listErr, w.release(asset))
			continue
		}
		deleteFailed := false
		for variantIndex := range variants {
			if deleteErr := w.store.Delete(ctx, variants[variantIndex].ObjectKey); deleteErr != nil {
				failures = append(failures, deleteErr)
				deleteFailed = true
				break
			}
		}
		if deleteFailed {
			failures = append(failures, w.release(asset))
			continue
		}
		if completeErr := w.repository.CompleteCleanup(ctx, asset); completeErr != nil {
			failures = append(failures, completeErr)
			continue
		}
		cleaned++
	}
	return cleaned, errors.Join(failures...)
}

func (w *CleanupWorker) Run(ctx context.Context) error {
	if err := w.validate(ctx); err != nil {
		return err
	}
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		cleaned, err := w.RunOnce(ctx)
		if w.onResult != nil {
			w.onResult(cleaned, err)
		}
		if ctx.Err() != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (w *CleanupWorker) validate(ctx context.Context) error {
	if w == nil || w.repository == nil || w.store == nil || ctx == nil || w.batchSize < 1 || w.batchSize > 100 || w.lease <= 0 || w.interval <= 0 {
		return errors.New("bounded media asset cleanup worker is required")
	}
	return nil
}

func (w *CleanupWorker) release(asset *media_model.Asset) error {
	compensationCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return w.repository.ReleaseCleanup(compensationCtx, asset)
}
