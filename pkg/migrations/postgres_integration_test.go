package migrations_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	"github.com/evolution-foundation/evolution-go/pkg/migrations"
	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
	projection_service "github.com/evolution-foundation/evolution-go/pkg/projection/service"
	"github.com/evolution-foundation/evolution-go/pkg/waquery"
	"go.mau.fi/whatsmeow/types"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestPostgresMigrationIsIdempotentAndStateSurvivesReconnect(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not set")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&instance_model.Instance{}); err != nil {
		t.Fatal(err)
	}
	if err := migrations.Run(db); err != nil {
		t.Fatal(err)
	}
	if err := migrations.Run(db); err != nil {
		t.Fatalf("second migration run failed: %v", err)
	}

	instance := instance_model.Instance{Name: "migration-test", Token: "migration-test-token"}
	if err := db.Create(&instance).Error; err != nil {
		t.Fatal(err)
	}
	repository := projection_repository.NewStateRepository(db)
	state := &projection_model.State{InstanceID: instance.Id, Resource: "groups", SyncStatus: projection_model.SyncStatusNotStarted, SchemaVersion: 1}
	if err := repository.Upsert(state); err != nil {
		t.Fatal(err)
	}
	eventRepository := projection_repository.NewEventRepository(db)
	event := &projection_model.Event{
		InstanceID: instance.Id, Resource: "groups", EventKey: "event-1", EntityKey: "group-1",
		EventType: "group_info", OccurredAt: time.Now(), Payload: json.RawMessage(`{"id":"group-1"}`),
	}
	inserted, err := eventRepository.Enqueue(context.Background(), event)
	if err != nil || !inserted {
		t.Fatalf("first enqueue = %v, %v", inserted, err)
	}
	duplicate := *event
	inserted, err = eventRepository.Enqueue(context.Background(), &duplicate)
	if err != nil || inserted {
		t.Fatalf("duplicate enqueue = %v, %v", inserted, err)
	}

	raw, _ := db.DB()
	_ = raw.Close()
	reopened, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := projection_repository.NewStateRepository(reopened).Get(instance.Id, "groups")
	if err != nil {
		t.Fatal(err)
	}
	if stored.SyncStatus != projection_model.SyncStatusNotStarted || stored.SchemaVersion != 1 {
		t.Fatalf("unexpected stored state: %#v", stored)
	}
	claimed, err := projection_repository.NewEventRepository(reopened).ClaimPending(context.Background(), 10, time.Minute)
	if err != nil || len(claimed) != 1 || claimed[0].EventKey != "event-1" {
		t.Fatalf("claimed after reconnect = %#v, %v", claimed, err)
	}
	if err := reopened.Model(&projection_model.Event{}).
		Where("instance_id = ? AND resource = ? AND event_key = ?", instance.Id, "groups", "event-1").
		Update("lease_until", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	reclaimed, err := projection_repository.NewEventRepository(reopened).ClaimPending(context.Background(), 10, time.Minute)
	if err != nil || len(reclaimed) != 1 || reclaimed[0].ClaimToken == nil || claimed[0].ClaimToken == nil || *reclaimed[0].ClaimToken == *claimed[0].ClaimToken {
		t.Fatalf("reclaimed expired lease = %#v, %v", reclaimed, err)
	}
	if err := projection_repository.NewEventRepository(reopened).MarkProcessed(context.Background(), &claimed[0]); !errors.Is(err, projection_repository.ErrEventClaimLost) {
		t.Fatalf("stale worker MarkProcessed() error = %v", err)
	}

	groupRepository := projection_repository.NewGroupRepository(reopened)
	newName := "Current group"
	nameSetAt := time.Unix(450, 0)
	topicID := "topic-1"
	announceVersion := "announce-v2"
	incognito := true
	participantCount := 1
	creatorCountryCode := "84"
	approvalMode := "request_required"
	newer := time.Unix(500, 0)
	applied, err := groupRepository.ApplySnapshot(context.Background(), &projection_model.Group{
		InstanceID: instance.Id, GroupID: "group@g.us", Name: &newName, NameSetAt: &nameSetAt, TopicID: &topicID,
		AnnounceVersion: &announceVersion, Incognito: &incognito, ParticipantCount: &participantCount,
		CreatorCountryCode: &creatorCountryCode, DefaultApprovalMode: &approvalMode,
		SourceOccurredAt: newer, SourceEventKey: "event-500",
	}, []projection_model.GroupParticipant{{ParticipantID: "user-a@s.whatsapp.net", Role: projection_model.ParticipantRoleAdmin}})
	if err != nil || !applied {
		t.Fatalf("new group snapshot = %v, %v", applied, err)
	}
	oldName := "Stale group"
	applied, err = groupRepository.ApplySnapshot(context.Background(), &projection_model.Group{
		InstanceID: instance.Id, GroupID: "group@g.us", Name: &oldName, SourceOccurredAt: time.Unix(400, 0), SourceEventKey: "event-400",
	}, []projection_model.GroupParticipant{{ParticipantID: "stale-user@s.whatsapp.net", Role: projection_model.ParticipantRoleMember}})
	if err != nil || applied {
		t.Fatalf("stale group snapshot = %v, %v", applied, err)
	}
	applied, err = groupRepository.ApplySnapshot(context.Background(), &projection_model.Group{
		InstanceID: instance.Id, GroupID: "group@g.us", Name: &oldName, SourceOccurredAt: newer, SourceEventKey: "event-499",
	}, nil)
	if err != nil || applied {
		t.Fatalf("lower-key timestamp tie = %v, %v", applied, err)
	}
	storedGroup, storedParticipants, err := groupRepository.Get(context.Background(), instance.Id, "group@g.us")
	if err != nil || storedGroup.Name == nil || *storedGroup.Name != newName || len(storedParticipants) != 1 || storedParticipants[0].ParticipantID != "user-a@s.whatsapp.net" {
		t.Fatalf("stored group after stale snapshot = %#v, %#v, %v", storedGroup, storedParticipants, err)
	}
	if storedGroup.NameSetAt == nil || !storedGroup.NameSetAt.Equal(nameSetAt) || storedGroup.TopicID == nil || *storedGroup.TopicID != topicID ||
		storedGroup.AnnounceVersion == nil || *storedGroup.AnnounceVersion != announceVersion || storedGroup.Incognito == nil || !*storedGroup.Incognito ||
		storedGroup.ParticipantCount == nil || *storedGroup.ParticipantCount != participantCount || storedGroup.CreatorCountryCode == nil || *storedGroup.CreatorCountryCode != creatorCountryCode ||
		storedGroup.DefaultApprovalMode == nil || *storedGroup.DefaultApprovalMode != approvalMode {
		t.Fatalf("stored group lost read-model metadata: %#v", storedGroup)
	}
	reconciledName := "Reconciled group"
	applied, err = groupRepository.ApplySnapshot(context.Background(), &projection_model.Group{
		InstanceID: instance.Id, GroupID: "group@g.us", Name: &reconciledName, SourceOccurredAt: time.Unix(600, 0), SourceEventKey: "event-600",
	}, []projection_model.GroupParticipant{{ParticipantID: "user-b@s.whatsapp.net", Role: projection_model.ParticipantRoleSuperAdmin}})
	if err != nil || !applied {
		t.Fatalf("reconciled group snapshot = %v, %v", applied, err)
	}
	_, storedParticipants, err = groupRepository.Get(context.Background(), instance.Id, "group@g.us")
	if err != nil || len(storedParticipants) != 1 || storedParticipants[0].ParticipantID != "user-b@s.whatsapp.net" {
		t.Fatalf("participants after replacement = %#v, %v", storedParticipants, err)
	}
	applied, err = groupRepository.Tombstone(context.Background(), instance.Id, "group@g.us", "delete-550", time.Unix(550, 0))
	if err != nil || applied {
		t.Fatalf("stale tombstone = %v, %v", applied, err)
	}
	applied, err = groupRepository.Tombstone(context.Background(), instance.Id, "group@g.us", "delete-700", time.Unix(700, 0))
	if err != nil || !applied {
		t.Fatalf("new tombstone = %v, %v", applied, err)
	}
	if _, _, err := groupRepository.Get(context.Background(), instance.Id, "group@g.us"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("tombstoned group remained readable: %v", err)
	}
	announce := true
	applied, err = groupRepository.ApplyPatch(context.Background(), projection_repository.GroupPatch{
		InstanceID: instance.Id, GroupID: "group@g.us", EventKey: "announce-900", OccurredAt: time.Unix(900, 0), Announce: &announce,
		ParticipantChanges: []projection_repository.GroupParticipantPatch{{ParticipantID: "user-c@s.whatsapp.net", Role: projection_model.ParticipantRoleAdmin}},
	})
	if err != nil || !applied {
		t.Fatalf("newer group patch = %v, %v", applied, err)
	}
	lateName := "Late valid name"
	applied, err = groupRepository.ApplyPatch(context.Background(), projection_repository.GroupPatch{
		InstanceID: instance.Id, GroupID: "group@g.us", EventKey: "name-800", OccurredAt: time.Unix(800, 0), Name: &lateName,
	})
	if err != nil || !applied {
		t.Fatalf("older disjoint field patch = %v, %v", applied, err)
	}
	staleAnnounce := false
	applied, err = groupRepository.ApplyPatch(context.Background(), projection_repository.GroupPatch{
		InstanceID: instance.Id, GroupID: "group@g.us", EventKey: "announce-850", OccurredAt: time.Unix(850, 0), Announce: &staleAnnounce,
	})
	if err != nil || applied {
		t.Fatalf("stale same-field patch = %v, %v", applied, err)
	}
	lateSnapshotName := "Snapshot name"
	lateSnapshotAnnounce := false
	applied, err = groupRepository.ApplySnapshot(context.Background(), &projection_model.Group{
		InstanceID: instance.Id, GroupID: "group@g.us", Name: &lateSnapshotName, Announce: &lateSnapshotAnnounce,
		SourceOccurredAt: time.Unix(750, 0), SourceEventKey: "snapshot-750",
	}, []projection_model.GroupParticipant{{ParticipantID: "user-d@s.whatsapp.net", Role: projection_model.ParticipantRoleMember}})
	if err != nil || !applied {
		t.Fatalf("late partial-fill snapshot = %v, %v", applied, err)
	}
	patchedGroup, patchedParticipants, err := groupRepository.Get(context.Background(), instance.Id, "group@g.us")
	if err != nil || patchedGroup.Name == nil || *patchedGroup.Name != lateName || patchedGroup.Announce == nil || !*patchedGroup.Announce ||
		patchedGroup.ParticipantCount == nil || *patchedGroup.ParticipantCount != 2 || len(patchedParticipants) != 2 ||
		patchedParticipants[0].ParticipantID != "user-c@s.whatsapp.net" || patchedParticipants[1].ParticipantID != "user-d@s.whatsapp.net" {
		t.Fatalf("out-of-order merged group = %#v, %#v, %v", patchedGroup, patchedParticipants, err)
	}

	pendingDelta := &projection_model.Event{
		InstanceID: instance.Id, Resource: "groups", EventKey: "pending-delta", EntityKey: "worker@g.us",
		EventType: "group_info", OccurredAt: time.Unix(800, 0), Payload: json.RawMessage(`{"groupId":"worker@g.us","name":{"name":"Worker renamed","setAt":"1970-01-01T00:13:20Z"}}`),
	}
	if inserted, err := projection_repository.NewEventRepository(reopened).Enqueue(context.Background(), pendingDelta); err != nil || !inserted {
		t.Fatalf("pending delta enqueue = %v, %v", inserted, err)
	}
	joinedSnapshot := &projection_model.Event{
		InstanceID: instance.Id, Resource: "groups", EventKey: "joined-snapshot", EntityKey: "worker@g.us",
		EventType: "joined_group", OccurredAt: time.Unix(800, 0),
		Payload: json.RawMessage(`{"groupId":"worker@g.us","name":{"name":"Worker group","setAt":"1970-01-01T00:13:20Z"},"locked":false,"announce":{"enabled":false},"ephemeral":{"enabled":false,"timer":0},"joinApprovalRequired":false,"suspended":false,"joined":{"type":"new"},"participants":[{"id":"worker-admin@s.whatsapp.net","admin":true}]}`),
	}
	if inserted, err := projection_repository.NewEventRepository(reopened).Enqueue(context.Background(), joinedSnapshot); err != nil || !inserted {
		t.Fatalf("joined snapshot enqueue = %v, %v", inserted, err)
	}
	stateService := projection_service.NewStateService(projection_repository.NewStateRepository(reopened))
	projector := projection_service.NewGroupProjector(groupRepository, stateService)
	eventService := projection_service.NewEventService(projection_repository.NewEventRepository(reopened), time.Minute, time.Second)
	batch, err := eventService.ProcessBatchFor(context.Background(), "groups", []string{"joined_group", "group_info"}, 10, projector.Handle)
	if err != nil || batch.Claimed != 2 || batch.Processed != 2 || batch.Failed != 0 {
		t.Fatalf("joined snapshot batch = %#v, %v", batch, err)
	}
	workerGroup, workerParticipants, err := groupRepository.Get(context.Background(), instance.Id, "worker@g.us")
	if err != nil || workerGroup.Name == nil || *workerGroup.Name != "Worker renamed" || len(workerParticipants) != 1 || workerParticipants[0].Role != projection_model.ParticipantRoleAdmin {
		t.Fatalf("worker projection = %#v, %#v, %v", workerGroup, workerParticipants, err)
	}
	state, err = stateService.Get(instance.Id, "groups")
	if err != nil || state.SyncStatus != projection_model.SyncStatusNotStarted || state.LastEventAt == nil || !state.LastEventAt.Equal(time.Unix(800, 0)) {
		t.Fatalf("groups projection state = %#v, %v", state, err)
	}
	var pendingCount int64
	if err := reopened.Model(&projection_model.Event{}).
		Where("instance_id = ? AND resource = ? AND event_key = ? AND status = ?", instance.Id, "groups", "pending-delta", projection_model.EventStatusProcessed).
		Count(&pendingCount).Error; err != nil || pendingCount != 1 {
		t.Fatalf("group delta was not processed: count=%d error=%v", pendingCount, err)
	}
	var stateWrites sync.WaitGroup
	stateErrors := make(chan error, 50)
	for index := 0; index < 50; index++ {
		index := index
		stateWrites.Add(1)
		go func() {
			defer stateWrites.Done()
			stateErrors <- stateService.RecordEvent(instance.Id, "contacts", int64(index%3+1), time.Unix(int64(index), 0))
		}()
	}
	stateWrites.Wait()
	close(stateErrors)
	for err := range stateErrors {
		if err != nil {
			t.Fatalf("concurrent state update: %v", err)
		}
	}
	contactState, err := stateService.Get(instance.Id, "contacts")
	if err != nil || contactState.LastEventAt == nil || !contactState.LastEventAt.Equal(time.Unix(49, 0)) || contactState.SchemaVersion != 3 {
		t.Fatalf("monotonic concurrent state = %#v, %v", contactState, err)
	}
	guard, err := waquery.New(waquery.Settings{RatePerSecond: 100, Burst: 10, MaxWait: time.Second, Cooldown: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	reconciler := projection_service.NewGroupReconciler(guard, groupRepository, stateService)
	queryCalls := 0
	if err := reconciler.Reconcile(context.Background(), instance.Id, func(context.Context) ([]*types.GroupInfo, error) {
		queryCalls++
		return []*types.GroupInfo{{
			JID: types.NewJID("authoritative", types.GroupServer), GroupName: types.GroupName{Name: "Authoritative group"},
			Participants: []types.GroupParticipant{{JID: types.NewJID("authoritative-admin", types.DefaultUserServer), IsAdmin: true}},
		}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if queryCalls != 1 {
		t.Fatalf("reconciliation upstream calls = %d", queryCalls)
	}
	if _, _, err := groupRepository.Get(context.Background(), instance.Id, "worker@g.us"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("missing group was not tombstoned: %v", err)
	}
	authoritativeGroup, authoritativeParticipants, err := groupRepository.Get(context.Background(), instance.Id, "authoritative@g.us")
	if err != nil || authoritativeGroup.Name == nil || *authoritativeGroup.Name != "Authoritative group" || len(authoritativeParticipants) != 1 || authoritativeParticipants[0].Role != projection_model.ParticipantRoleAdmin {
		t.Fatalf("authoritative group = %#v, %#v, %v", authoritativeGroup, authoritativeParticipants, err)
	}
	groupsState, err := stateService.Get(instance.Id, "groups")
	capabilities, capabilityErr := stateService.Capabilities(instance.Id)
	if err != nil || capabilityErr != nil || groupsState.SyncStatus != projection_model.SyncStatusReady || groupsState.LastReconciledAt == nil || containsString(capabilities, "groups_projection") {
		t.Fatalf("ready groups state = %#v capabilities=%v errors=%v/%v", groupsState, capabilities, err, capabilityErr)
	}
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
