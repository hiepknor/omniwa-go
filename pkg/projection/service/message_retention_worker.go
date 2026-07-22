package projection_service

import (
	"context"
	"errors"
	"time"
)

type messageRetentionDeleter interface {
	DeleteBefore(context.Context, time.Time, int) (int64, error)
}

type MessageRetentionResultHandler func(deleted int64, err error)

type MessageRetentionWorker struct {
	repository messageRetentionDeleter
	retention  time.Duration
	batchSize  int
	interval   time.Duration
	now        func() time.Time
	onResult   MessageRetentionResultHandler
}

func NewMessageRetentionWorker(repository messageRetentionDeleter, retention time.Duration, batchSize int, interval time.Duration, onResult MessageRetentionResultHandler) *MessageRetentionWorker {
	return &MessageRetentionWorker{
		repository: repository, retention: retention, batchSize: batchSize, interval: interval,
		now: time.Now, onResult: onResult,
	}
}

func (w *MessageRetentionWorker) Sweep(ctx context.Context) (int64, error) {
	if w == nil || w.repository == nil || ctx == nil || w.retention <= 0 || w.batchSize <= 0 || w.now == nil {
		return 0, errors.New("message retention worker configuration is invalid")
	}
	return w.repository.DeleteBefore(ctx, w.now().UTC().Add(-w.retention), w.batchSize)
}

func (w *MessageRetentionWorker) Run(ctx context.Context) error {
	if w == nil || ctx == nil || w.interval <= 0 {
		return errors.New("message retention worker configuration is invalid")
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
