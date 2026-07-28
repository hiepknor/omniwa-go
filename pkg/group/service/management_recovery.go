package group_service

import (
	"context"
	"errors"
	"time"
)

type managementRecoveryRepository interface {
	RecoverStaleExecuting(context.Context, time.Time, int) (int64, error)
}

type ManagementRecoveryResultHandler func(recovered int64, err error)

type ManagementRecoveryWorker struct {
	repository managementRecoveryRepository
	staleAfter time.Duration
	interval   time.Duration
	batchSize  int
	now        func() time.Time
	onResult   ManagementRecoveryResultHandler
}

func NewManagementRecoveryWorker(repository managementRecoveryRepository, staleAfter, interval time.Duration, batchSize int, onResult ManagementRecoveryResultHandler) *ManagementRecoveryWorker {
	return &ManagementRecoveryWorker{repository: repository, staleAfter: staleAfter, interval: interval, batchSize: batchSize, now: time.Now, onResult: onResult}
}

func (w *ManagementRecoveryWorker) RunOnce(ctx context.Context) (int64, error) {
	if w == nil || w.repository == nil || w.now == nil || ctx == nil || w.staleAfter <= 0 || w.interval <= 0 || w.batchSize < 1 || w.batchSize > 1000 {
		return 0, errors.New("group management recovery configuration is invalid")
	}
	return w.repository.RecoverStaleExecuting(ctx, w.now().UTC().Add(-w.staleAfter), w.batchSize)
}

func (w *ManagementRecoveryWorker) Run(ctx context.Context) error {
	if w == nil || w.repository == nil || w.now == nil || ctx == nil || w.staleAfter <= 0 || w.interval <= 0 || w.batchSize < 1 || w.batchSize > 1000 {
		return errors.New("group management recovery configuration is invalid")
	}
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		recovered, err := w.RunOnce(ctx)
		if w.onResult != nil {
			w.onResult(recovered, err)
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
