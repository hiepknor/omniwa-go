package projection_service

import (
	"context"
	"errors"
	"testing"
	"time"
)

type retentionDeleteCall struct {
	cutoff time.Time
	limit  int
}

type retentionDeleterStub struct {
	calls   []retentionDeleteCall
	deleted int64
	err     error
}

func (s *retentionDeleterStub) DeleteBefore(_ context.Context, cutoff time.Time, limit int) (int64, error) {
	s.calls = append(s.calls, retentionDeleteCall{cutoff: cutoff, limit: limit})
	return s.deleted, s.err
}

func TestMessageRetentionWorkerUsesConfiguredCutoffAndBatch(t *testing.T) {
	repository := &retentionDeleterStub{deleted: 7}
	worker := NewMessageRetentionWorker(repository, 30*24*time.Hour, 500, time.Hour, nil)
	worker.now = func() time.Time { return time.Unix(4_000_000, 0).UTC() }
	deleted, err := worker.Sweep(context.Background())
	if err != nil || deleted != 7 || len(repository.calls) != 1 || repository.calls[0].limit != 500 {
		t.Fatalf("Sweep() = %d, %v calls=%#v", deleted, err, repository.calls)
	}
	want := worker.now().Add(-30 * 24 * time.Hour)
	if !repository.calls[0].cutoff.Equal(want) {
		t.Fatalf("retention cutoff = %v, want %v", repository.calls[0].cutoff, want)
	}
}

func TestMessageRetentionWorkerRejectsUnsafeConfigurationAndPropagatesErrors(t *testing.T) {
	if _, err := NewMessageRetentionWorker(&retentionDeleterStub{}, 0, 1, time.Hour, nil).Sweep(context.Background()); err == nil {
		t.Fatal("zero retention was accepted")
	}
	want := errors.New("delete failed")
	worker := NewMessageRetentionWorker(&retentionDeleterStub{err: want}, time.Hour, 1, time.Hour, nil)
	if _, err := worker.Sweep(context.Background()); !errors.Is(err, want) {
		t.Fatalf("Sweep() error = %v", err)
	}
	if err := worker.Run(nil); err == nil {
		t.Fatal("nil worker context was accepted")
	}
}
