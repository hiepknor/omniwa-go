package projection_service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	"github.com/evolution-foundation/evolution-go/pkg/waquery"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"gorm.io/gorm"
)

type joinedGroupsQuery func(context.Context) ([]*types.GroupInfo, error)

type groupReconciliationRepository interface {
	ApplySnapshot(context.Context, *projection_model.Group, []projection_model.GroupParticipant) (bool, error)
	TombstoneMissing(context.Context, string, []string, string, time.Time) (int, error)
}

type reconciliationState interface {
	Get(instanceID, resource string) (*projection_model.State, error)
	MarkSyncing(instanceID, resource string, schemaVersion int64) error
	MarkReady(instanceID, resource string, schemaVersion int64, reconciledAt time.Time) error
	MarkStale(instanceID, resource string, schemaVersion int64) error
	MarkFailed(instanceID, resource string, schemaVersion int64) error
}

type GroupReconciler struct {
	guard  waquery.Guard
	groups groupReconciliationRepository
	state  reconciliationState
	now    func() time.Time
}

func NewGroupReconciler(guard waquery.Guard, groups groupReconciliationRepository, state reconciliationState) *GroupReconciler {
	return &GroupReconciler{guard: guard, groups: groups, state: state, now: time.Now}
}

func (r *GroupReconciler) Reconcile(ctx context.Context, instanceID string, query joinedGroupsQuery) error {
	if r == nil || r.guard == nil || r.groups == nil || r.state == nil || instanceID == "" || query == nil {
		return errors.New("group reconciliation dependencies are required")
	}
	previous, err := r.state.Get(instanceID, groupResource)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	hadReadyData := previous != nil && previous.LastReconciledAt != nil
	if err := r.state.MarkSyncing(instanceID, groupResource, groupsProjectionSchemaVersion); err != nil {
		return err
	}
	snapshotFence := r.now().UTC()
	groups, err := waquery.Do(ctx, r.guard, instanceID, waquery.OperationGroupsList, "", query)
	if err != nil {
		return errors.Join(err, r.markFailure(instanceID, hadReadyData))
	}
	byID := make(map[string]*types.GroupInfo, len(groups))
	for _, group := range groups {
		if group == nil || group.JID.IsEmpty() {
			continue
		}
		byID[group.JID.String()] = group
	}
	groupIDs := make([]string, 0, len(byID))
	for groupID := range byID {
		groupIDs = append(groupIDs, groupID)
	}
	sort.Strings(groupIDs)
	for _, groupID := range groupIDs {
		info := byID[groupID]
		eventKey := reconciliationEventKey(snapshotFence, groupID)
		payload := normalizeJoinedGroup(&events.JoinedGroup{GroupInfo: *info})
		event := &projection_model.Event{
			InstanceID: instanceID, EntityKey: groupID, EventKey: eventKey,
			Resource: groupResource, EventType: "reconciliation", OccurredAt: snapshotFence,
		}
		if _, err := r.groups.ApplySnapshot(ctx, groupFromSnapshot(event, &payload), participantsFromSnapshot(&payload)); err != nil {
			return errors.Join(err, r.markFailure(instanceID, hadReadyData))
		}
	}
	if _, err := r.groups.TombstoneMissing(ctx, instanceID, groupIDs, reconciliationEventKey(snapshotFence, "missing"), snapshotFence); err != nil {
		return errors.Join(err, r.markFailure(instanceID, hadReadyData))
	}
	return r.state.MarkReady(instanceID, groupResource, groupsProjectionSchemaVersion, r.now().UTC())
}

func (r *GroupReconciler) markFailure(instanceID string, hadReadyData bool) error {
	if hadReadyData {
		return r.state.MarkStale(instanceID, groupResource, groupsProjectionSchemaVersion)
	}
	return r.state.MarkFailed(instanceID, groupResource, groupsProjectionSchemaVersion)
}

func reconciliationEventKey(reconciledAt time.Time, resource string) string {
	sum := sha256.Sum256([]byte(reconciledAt.UTC().Format(time.RFC3339Nano) + "\x00" + resource))
	return hex.EncodeToString(sum[:])
}
