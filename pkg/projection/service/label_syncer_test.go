package projection_service

import (
	"context"
	"errors"
	"testing"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	"github.com/evolution-foundation/evolution-go/pkg/waquery"
	"gorm.io/gorm"
)

type labelSyncStateStub struct {
	state       *projection_model.State
	getErr      error
	syncingCall int
	failedCall  int
	staleCall   int
}

func (s *labelSyncStateStub) Get(string, string) (*projection_model.State, error) {
	return s.state, s.getErr
}

func (s *labelSyncStateStub) MarkSyncing(string, string, int64) error {
	s.syncingCall++
	return nil
}

func (s *labelSyncStateStub) MarkFailed(string, string, int64) error {
	s.failedCall++
	return nil
}

func (s *labelSyncStateStub) MarkStale(string, string, int64) error {
	s.staleCall++
	return nil
}

func TestLabelSyncerSkipsReadyProjectionAndGuardsInitialFetch(t *testing.T) {
	guard, err := waquery.New(waquery.Settings{RatePerSecond: 100, Burst: 2, MaxWait: time.Second, Cooldown: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ready := &labelSyncStateStub{state: &projection_model.State{SyncStatus: projection_model.SyncStatusReady, SchemaVersion: LabelsProjectionSchemaVersion}}
	called := 0
	if err := NewLabelSyncer(guard, ready).Sync(context.Background(), "instance-a", func(context.Context) error { called++; return nil }); err != nil || called != 0 {
		t.Fatalf("ready projection sync = calls %d, error %v", called, err)
	}

	pending := &labelSyncStateStub{getErr: gorm.ErrRecordNotFound}
	if err := NewLabelSyncer(guard, pending).Sync(context.Background(), "instance-a", func(context.Context) error { called++; return nil }); err != nil {
		t.Fatal(err)
	}
	if called != 1 || pending.syncingCall != 1 || pending.failedCall != 0 {
		t.Fatalf("initial sync = calls %d, state %#v", called, pending)
	}
}

func TestLabelSyncerMarksFailedFetch(t *testing.T) {
	guard, err := waquery.New(waquery.Settings{RatePerSecond: 100, Burst: 2, MaxWait: time.Second, Cooldown: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	state := &labelSyncStateStub{getErr: gorm.ErrRecordNotFound}
	upstreamErr := errors.New("fetch failed")
	err = NewLabelSyncer(guard, state).Sync(context.Background(), "instance-a", func(context.Context) error { return upstreamErr })
	if !errors.Is(err, upstreamErr) || state.failedCall != 1 {
		t.Fatalf("failed sync = state %#v, error %v", state, err)
	}
}
