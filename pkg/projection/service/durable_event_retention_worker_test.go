package projection_service

import (
	"context"
	"errors"
	"testing"
	"time"
)

type durableEventRetentionStub struct {
	now     time.Time
	limit   int
	deleted int64
	err     error
}

func (s *durableEventRetentionStub) DeleteExpired(_ context.Context, now time.Time, limit int) (int64, error) {
	s.now, s.limit = now, limit
	return s.deleted, s.err
}

func TestDurableEventRetentionWorkerUsesCurrentTimeAndBoundedBatch(t *testing.T) {
	repository := &durableEventRetentionStub{deleted: 9}
	worker := NewDurableEventRetentionWorker(repository, 500, time.Hour, nil)
	wantNow := time.Unix(4_000_000, 0).UTC()
	worker.now = func() time.Time { return wantNow }
	deleted, err := worker.Sweep(context.Background())
	if err != nil || deleted != 9 || repository.limit != 500 || !repository.now.Equal(wantNow) {
		t.Fatalf("Sweep() = %d, %v repository=%#v", deleted, err, repository)
	}
}

func TestDurableEventRetentionWorkerRejectsUnsafeConfigurationAndPropagatesErrors(t *testing.T) {
	if _, err := NewDurableEventRetentionWorker(&durableEventRetentionStub{}, 0, time.Hour, nil).Sweep(context.Background()); err == nil {
		t.Fatal("zero batch size was accepted")
	}
	want := errors.New("delete failed")
	worker := NewDurableEventRetentionWorker(&durableEventRetentionStub{err: want}, 1, time.Hour, nil)
	if _, err := worker.Sweep(context.Background()); !errors.Is(err, want) {
		t.Fatalf("Sweep() error = %v", err)
	}
	if err := worker.Run(nil); err == nil {
		t.Fatal("nil worker context was accepted")
	}
}
