package projection_service

import (
	"context"
	"errors"
	"time"
)

type durableEventRetentionDeleter interface {
	DeleteExpired(context.Context, time.Time, int) (int64, error)
}

type DurableEventRetentionResultHandler func(deleted int64, err error)

type DurableEventRetentionWorker struct {
	repository durableEventRetentionDeleter
	batchSize  int
	interval   time.Duration
	now        func() time.Time
	onResult   DurableEventRetentionResultHandler
}

func NewDurableEventRetentionWorker(repository durableEventRetentionDeleter, batchSize int, interval time.Duration, onResult DurableEventRetentionResultHandler) *DurableEventRetentionWorker {
	return &DurableEventRetentionWorker{repository: repository, batchSize: batchSize, interval: interval, now: time.Now, onResult: onResult}
}

func (w *DurableEventRetentionWorker) Sweep(ctx context.Context) (int64, error) {
	if w == nil || w.repository == nil || ctx == nil || w.batchSize <= 0 || w.now == nil {
		return 0, errors.New("durable event retention worker configuration is invalid")
	}
	return w.repository.DeleteExpired(ctx, w.now().UTC(), w.batchSize)
}

func (w *DurableEventRetentionWorker) Run(ctx context.Context) error {
	if w == nil || ctx == nil || w.interval <= 0 {
		return errors.New("durable event retention worker configuration is invalid")
	}
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		deleted, err := w.Sweep(ctx)
		if w.onResult != nil {
			w.onResult(deleted, err)
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
