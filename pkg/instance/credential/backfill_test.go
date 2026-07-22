package instance_credential

import (
	"context"
	"errors"
	"testing"
)

type scriptedBackfill struct {
	results []int
	errAt   int
	calls   int
}

func (s *scriptedBackfill) BackfillTokenDigests(context.Context, int) (int, error) {
	s.calls++
	if s.errAt == s.calls {
		return 0, errors.New("database unavailable")
	}
	if len(s.results) == 0 {
		return 0, nil
	}
	result := s.results[0]
	s.results = s.results[1:]
	return result, nil
}

func TestRunBoundedBackfillStopsWhenComplete(t *testing.T) {
	repository := &scriptedBackfill{results: []int{2, 1}}
	result, err := RunBoundedBackfill(context.Background(), repository, 2, 5)
	if err != nil || result.Updated != 3 || result.Batches != 2 || !result.Complete {
		t.Fatalf("RunBoundedBackfill() = %#v, %v", result, err)
	}
}

func TestRunBoundedBackfillHonorsBudgetAndPropagatesErrors(t *testing.T) {
	repository := &scriptedBackfill{results: []int{2, 2, 2}}
	result, err := RunBoundedBackfill(context.Background(), repository, 2, 2)
	if err != nil || result.Updated != 4 || result.Batches != 2 || result.Complete {
		t.Fatalf("bounded result = %#v, %v", result, err)
	}
	repository = &scriptedBackfill{errAt: 1}
	if _, err := RunBoundedBackfill(context.Background(), repository, 2, 2); err == nil {
		t.Fatal("backfill error was swallowed")
	}
}
