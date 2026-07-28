package group_service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type recoveryRepositoryStub struct {
	mu       sync.Mutex
	calls    int
	failures int
	cutoff   time.Time
	batch    int
}

func (r *recoveryRepositoryStub) RecoverStaleExecuting(_ context.Context, cutoff time.Time, batch int) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	r.cutoff = cutoff
	r.batch = batch
	if r.calls <= r.failures {
		return 0, errors.New("temporary database failure")
	}
	return 2, nil
}

func TestManagementRecoveryWorkerRunOnceUsesBoundedCutoff(t *testing.T) {
	repository := &recoveryRepositoryStub{}
	worker := NewManagementRecoveryWorker(repository, 5*time.Minute, time.Minute, 100, nil)
	now := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	worker.now = func() time.Time { return now }

	recovered, err := worker.RunOnce(context.Background())
	if err != nil || recovered != 2 {
		t.Fatalf("RunOnce() = (%d, %v), want (2, nil)", recovered, err)
	}
	if !repository.cutoff.Equal(now.Add(-5*time.Minute)) || repository.batch != 100 {
		t.Fatalf("repository call = (%s, %d)", repository.cutoff, repository.batch)
	}
}

func TestManagementRecoveryWorkerContinuesAfterTransientFailure(t *testing.T) {
	repository := &recoveryRepositoryStub{failures: 1}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := make(chan error, 2)
	worker := NewManagementRecoveryWorker(repository, time.Second, time.Millisecond, 10, func(_ int64, err error) {
		results <- err
		if err == nil {
			cancel()
		}
	})

	if err := worker.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if first, second := <-results, <-results; first == nil || second != nil {
		t.Fatalf("result errors = (%v, %v), want transient failure then success", first, second)
	}
}
