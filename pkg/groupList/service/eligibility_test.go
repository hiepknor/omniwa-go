package group_list_service

import (
	"context"
	"errors"
	"testing"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
	projection_service "github.com/evolution-foundation/evolution-go/pkg/projection/service"
	"gorm.io/gorm"
)

type eligibilityGroupStub struct {
	records []projection_repository.GroupRecord
	calls   int
}

func (stub *eligibilityGroupStub) GetForEligibility(context.Context, string, string, []string) ([]projection_repository.GroupRecord, error) {
	stub.calls++
	return stub.records, nil
}

type eligibilityStateStub struct {
	state *projection_model.State
	err   error
}

func (stub eligibilityStateStub) GetServingState(string, string) (*projection_model.State, error) {
	return stub.state, stub.err
}

func TestEligibilityRequiresReadyReconciledProjection(t *testing.T) {
	group := &eligibilityGroupStub{}
	for _, state := range []*projection_model.State{
		nil,
		{SyncStatus: projection_model.SyncStatusSyncing, SchemaVersion: projection_service.GroupsProjectionSchemaVersion},
		{SyncStatus: projection_model.SyncStatusReady, SchemaVersion: projection_service.GroupsProjectionSchemaVersion},
		{SyncStatus: projection_model.SyncStatusReady, SchemaVersion: projection_service.GroupsProjectionSchemaVersion - 1, LastReconciledAt: timePointer(time.Unix(1, 0))},
	} {
		service := NewEligibilityService(group, eligibilityStateStub{state: state})
		service.now = func() time.Time { return time.Unix(10, 0) }
		results, err := service.Evaluate(context.Background(), "instance", "5511@s.whatsapp.net", []string{"120363000001@g.us"})
		if err != nil {
			t.Fatal(err)
		}
		assertEligibility(t, results[0], EligibilityUnknown, ReasonProjectionNotReady, false)
	}
	service := NewEligibilityService(group, eligibilityStateStub{err: gorm.ErrRecordNotFound})
	service.now = func() time.Time { return time.Unix(10, 0) }
	results, err := service.Evaluate(context.Background(), "instance", "5511@s.whatsapp.net", []string{"120363000001@g.us"})
	if err != nil {
		t.Fatal(err)
	}
	assertEligibility(t, results[0], EligibilityUnknown, ReasonProjectionNotReady, false)
	if group.calls != 0 {
		t.Fatalf("group projection read before readiness: %d calls", group.calls)
	}
}

func TestEligibilityAssessmentReturnsTheEvaluatedProjectionMeta(t *testing.T) {
	lastReconciledAt := time.Unix(8, 0)
	state := &projection_model.State{
		SyncStatus: projection_model.SyncStatusStale, SchemaVersion: projection_service.GroupsProjectionSchemaVersion,
		LastReconciledAt: &lastReconciledAt,
	}
	service := NewEligibilityService(&eligibilityGroupStub{}, eligibilityStateStub{state: state})
	service.now = func() time.Time { return time.Unix(10, 0) }
	assessment, err := service.Assess(context.Background(), "instance", "5511@s.whatsapp.net", []string{"120363000001@g.us"})
	if err != nil {
		t.Fatal(err)
	}
	if assessment.Meta.Source != "groups_projection" || assessment.Meta.SyncStatus != projection_model.SyncStatusStale ||
		assessment.Meta.LastSyncedAt == nil || !assessment.Meta.LastSyncedAt.Equal(lastReconciledAt) {
		t.Fatalf("meta = %+v", assessment.Meta)
	}
	assertEligibility(t, assessment.Results[0], EligibilityUnknown, ReasonProjectionNotReady, false)
}

func TestEligibilityMapsGroupAccessAndSendPermission(t *testing.T) {
	groupJID := "120363000001@g.us"
	identity := "5511@s.whatsapp.net"
	ready := &projection_model.State{SyncStatus: projection_model.SyncStatusReady, SchemaVersion: projection_service.GroupsProjectionSchemaVersion, LastReconciledAt: timePointer(time.Unix(1, 0))}
	falseValue, trueValue := false, true
	name := "Branch"
	tombstonedAt := time.Unix(2, 0)

	tests := []struct {
		name       string
		records    []projection_repository.GroupRecord
		wantState  string
		wantReason string
		canSend    bool
	}{
		{name: "missing means access lost", wantState: EligibilityUnavailable, wantReason: ReasonAccessLost},
		{name: "access tombstone", records: []projection_repository.GroupRecord{{Group: projection_model.Group{GroupID: groupJID, Name: &name, TombstonedAt: &tombstonedAt, TombstoneCause: causePointer(projection_model.GroupTombstoneAccessLost)}}}, wantState: EligibilityUnavailable, wantReason: ReasonAccessLost},
		{name: "dissolved tombstone", records: []projection_repository.GroupRecord{{Group: projection_model.Group{GroupID: groupJID, Name: &name, TombstonedAt: &tombstonedAt, TombstoneCause: causePointer(projection_model.GroupTombstoneDissolved)}}}, wantState: EligibilityUnavailable, wantReason: ReasonDissolved},
		{name: "incomplete projection", records: []projection_repository.GroupRecord{{Group: projection_model.Group{GroupID: groupJID, Name: &name}}}, wantState: EligibilityUnknown, wantReason: ReasonProjectionNotReady},
		{name: "instance absent", records: []projection_repository.GroupRecord{{Group: projection_model.Group{GroupID: groupJID, Name: &name, Suspended: &falseValue, Announce: &falseValue}}}, wantState: EligibilityUnavailable, wantReason: ReasonAccessLost},
		{name: "suspended", records: []projection_repository.GroupRecord{{Group: projection_model.Group{GroupID: groupJID, Name: &name, Suspended: &trueValue, Announce: &falseValue}, Participants: []projection_model.GroupParticipant{{ParticipantID: identity, Role: projection_model.ParticipantRoleMember}}}}, wantState: EligibilityUnavailable, wantReason: ReasonSuspended},
		{name: "announce member denied", records: []projection_repository.GroupRecord{{Group: projection_model.Group{GroupID: groupJID, Name: &name, Suspended: &falseValue, Announce: &trueValue}, Participants: []projection_model.GroupParticipant{{ParticipantID: identity, Role: projection_model.ParticipantRoleMember}}}}, wantState: EligibilityUnavailable, wantReason: ReasonPermissionDenied},
		{name: "announce admin eligible", records: []projection_repository.GroupRecord{{Group: projection_model.Group{GroupID: groupJID, Name: &name, Suspended: &falseValue, Announce: &trueValue}, Participants: []projection_model.GroupParticipant{{ParticipantID: identity, Role: projection_model.ParticipantRoleAdmin}}}}, wantState: EligibilityEligible, canSend: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := NewEligibilityService(&eligibilityGroupStub{records: test.records}, eligibilityStateStub{state: ready})
			service.now = func() time.Time { return time.Unix(10, 0) }
			results, err := service.Evaluate(context.Background(), "instance", identity, []string{groupJID})
			if err != nil {
				t.Fatal(err)
			}
			assertEligibility(t, results[0], test.wantState, test.wantReason, test.canSend)
			if test.records != nil && results[0].CurrentName != name {
				t.Fatalf("current name = %q", results[0].CurrentName)
			}
		})
	}
}

func TestEligibilityMatchesPhoneAndLIDAliases(t *testing.T) {
	ready := &projection_model.State{SyncStatus: projection_model.SyncStatusReady, SchemaVersion: projection_service.GroupsProjectionSchemaVersion, LastReconciledAt: timePointer(time.Unix(1, 0))}
	groupJID := "120363000001@g.us"
	identity := "5511@s.whatsapp.net"
	falseValue := false
	for _, participant := range []projection_model.GroupParticipant{
		{ParticipantID: "123@lid", PhoneNumberJID: stringPointer(identity), Role: projection_model.ParticipantRoleMember},
		{ParticipantID: "999@lid", LID: stringPointer("123@lid"), Role: projection_model.ParticipantRoleMember},
	} {
		service := NewEligibilityService(&eligibilityGroupStub{records: []projection_repository.GroupRecord{{Group: projection_model.Group{GroupID: groupJID, Suspended: &falseValue, Announce: &falseValue}, Participants: []projection_model.GroupParticipant{participant}}}}, eligibilityStateStub{state: ready})
		instanceIdentity := identity
		if participant.PhoneNumberJID == nil {
			instanceIdentity = "123@lid"
		}
		results, err := service.Evaluate(context.Background(), "instance", instanceIdentity, []string{groupJID})
		if err != nil {
			t.Fatal(err)
		}
		assertEligibility(t, results[0], EligibilityEligible, "", true)
	}
}

func TestCanonicalGroupJIDRejectsNonGroupAndDuplicates(t *testing.T) {
	if _, err := CanonicalGroupJID("5511@s.whatsapp.net"); err == nil {
		t.Fatal("direct JID accepted as a group")
	}
	service := NewEligibilityService(&eligibilityGroupStub{}, eligibilityStateStub{})
	_, err := service.Evaluate(context.Background(), "instance", "5511@s.whatsapp.net", []string{"120363000001@g.us", "120363000001@g.us"})
	if err == nil || errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("duplicate result = %v", err)
	}
}

func TestMutationEntriesCollectsIssuesWithUnknownPrecedenceAndTruncation(t *testing.T) {
	results := make([]EligibilityResult, 102)
	for index := range results {
		reason := ReasonAccessLost
		results[index] = EligibilityResult{GroupJID: "120363000001@g.us", Eligibility: EligibilityUnavailable, EligibilityReason: &reason, CheckedAt: time.Unix(10, 0)}
	}
	unknownReason := ReasonProjectionNotReady
	results[101].Eligibility = EligibilityUnknown
	results[101].EligibilityReason = &unknownReason
	_, err := MutationEntries(results)
	var issues *EligibilityIssuesError
	if !errors.As(err, &issues) || !errors.Is(err, ErrProjectionNotReady) {
		t.Fatalf("error = %T %v", err, err)
	}
	if issues.Details.IssueCount != 102 || !issues.Details.Truncated || len(issues.Details.Issues) != 100 {
		t.Fatalf("details = %+v", issues.Details)
	}
}

func TestMutationEntriesBuildsEligibleSnapshots(t *testing.T) {
	entries, err := MutationEntries([]EligibilityResult{{
		GroupJID: "120363000001@g.us", CurrentName: "Branch", Eligibility: EligibilityEligible, CanSend: true,
	}})
	if err != nil || len(entries) != 1 || entries[0].GroupNameSnapshot != "Branch" {
		t.Fatalf("entries=%+v err=%v", entries, err)
	}
}

func assertEligibility(t *testing.T, result EligibilityResult, state, reason string, canSend bool) {
	t.Helper()
	if result.Eligibility != state || result.CanSend != canSend {
		t.Fatalf("eligibility = %+v, want state=%s canSend=%t", result, state, canSend)
	}
	if reason == "" {
		if result.EligibilityReason != nil {
			t.Fatalf("reason = %v, want nil", *result.EligibilityReason)
		}
	} else if result.EligibilityReason == nil || *result.EligibilityReason != reason {
		t.Fatalf("reason = %v, want %s", result.EligibilityReason, reason)
	}
}

func timePointer(value time.Time) *time.Time { return &value }
func stringPointer(value string) *string     { return &value }
func causePointer(value projection_model.GroupTombstoneCause) *projection_model.GroupTombstoneCause {
	return &value
}
