package projection_service

import (
	"context"
	"errors"
	"testing"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	"github.com/evolution-foundation/evolution-go/pkg/waquery"
	"go.mau.fi/whatsmeow/types"
)

type captureGroupReconciliationRepository struct {
	groups          []*projection_model.Group
	activeGroupIDs  []string
	tombstoneCalled bool
}

func (r *captureGroupReconciliationRepository) ApplySnapshot(_ context.Context, group *projection_model.Group, _ []projection_model.GroupParticipant) (bool, error) {
	r.groups = append(r.groups, group)
	return true, nil
}

func (r *captureGroupReconciliationRepository) TombstoneMissing(_ context.Context, _ string, activeGroupIDs []string, _ string, _ time.Time) (int, error) {
	r.activeGroupIDs = append([]string(nil), activeGroupIDs...)
	r.tombstoneCalled = true
	return 0, nil
}

func TestGroupReconcilerMarksReadyAfterGuardedAuthoritativeSnapshot(t *testing.T) {
	guard, err := waquery.New(waquery.Settings{RatePerSecond: 100, Burst: 10, MaxWait: time.Second, Cooldown: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	stateRepository := newMemoryRepository()
	state := NewStateService(stateRepository)
	groups := &captureGroupReconciliationRepository{}
	snapshotFence := time.Unix(1000, 0)
	reconciledAt := time.Unix(1100, 0)
	nowCalls := 0
	reconciler := &GroupReconciler{guard: guard, groups: groups, state: state, now: func() time.Time {
		nowCalls++
		if nowCalls == 1 {
			return snapshotFence
		}
		return reconciledAt
	}}
	queryCalls := 0
	err = reconciler.Reconcile(context.Background(), "instance-a", func(context.Context) ([]*types.GroupInfo, error) {
		queryCalls++
		return []*types.GroupInfo{
			{JID: types.NewJID("b", types.GroupServer), GroupName: types.GroupName{Name: "B"}},
			{JID: types.NewJID("a", types.GroupServer), GroupName: types.GroupName{Name: "A"}},
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := state.Get("instance-a", groupResource)
	if err != nil || stored.SyncStatus != projection_model.SyncStatusReady || stored.LastReconciledAt == nil || !stored.LastReconciledAt.Equal(reconciledAt) {
		t.Fatalf("unexpected reconciliation state: %#v, %v", stored, err)
	}
	if queryCalls != 1 || len(groups.groups) != 2 || !groups.tombstoneCalled || len(groups.activeGroupIDs) != 2 || groups.activeGroupIDs[0] != "a@g.us" || groups.activeGroupIDs[1] != "b@g.us" {
		t.Fatalf("unexpected reconciliation work: calls=%d repo=%#v", queryCalls, groups)
	}
	if !groups.groups[0].SourceOccurredAt.Equal(snapshotFence) {
		t.Fatalf("snapshot was not fenced before query: %v", groups.groups[0].SourceOccurredAt)
	}
}

func TestGroupReconcilerMarksInitialFailureWithoutPublishingCapability(t *testing.T) {
	guard, _ := waquery.New(waquery.Settings{RatePerSecond: 100, Burst: 10, MaxWait: time.Second, Cooldown: time.Second})
	state := NewStateService(newMemoryRepository())
	reconciler := NewGroupReconciler(guard, &captureGroupReconciliationRepository{}, state)
	err := reconciler.Reconcile(context.Background(), "instance-a", func(context.Context) ([]*types.GroupInfo, error) {
		return nil, errors.New("upstream unavailable")
	})
	if err == nil {
		t.Fatal("reconciliation failure was hidden")
	}
	stored, _ := state.Get("instance-a", groupResource)
	capabilities, _ := state.Capabilities("instance-a")
	if stored.SyncStatus != projection_model.SyncStatusFailed || containsCapability(capabilities, "groups_projection") {
		t.Fatalf("failure published projection: state=%#v capabilities=%v", stored, capabilities)
	}
}
