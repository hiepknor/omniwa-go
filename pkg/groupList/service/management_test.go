package group_list_service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	group_list_model "github.com/evolution-foundation/evolution-go/pkg/groupList/model"
	group_list_repository "github.com/evolution-foundation/evolution-go/pkg/groupList/repository"
	"github.com/evolution-foundation/evolution-go/pkg/observability"
	"github.com/google/uuid"
)

type managementRepositoryStub struct {
	created     *group_list_repository.CreateInput
	createErr   error
	listCursor  *group_list_repository.ListCursor
	listPage    group_list_repository.ListPage
	entriesPage group_list_repository.EntryPage
	auditPage   group_list_repository.AuditPage
	snapshot    *group_list_repository.Summary
	snapshotErr error
}

func (stub *managementRepositoryStub) Create(_ context.Context, input group_list_repository.CreateInput) (*group_list_model.GroupList, error) {
	stub.created = &input
	if stub.createErr != nil {
		return nil, stub.createErr
	}
	return &group_list_model.GroupList{ID: input.ID, InstanceID: input.InstanceID, Name: input.Name, NormalizedName: input.NormalizedName, Version: 1}, nil
}

func (stub *managementRepositoryStub) Get(context.Context, string, string) (*group_list_repository.Summary, error) {
	return nil, group_list_repository.ErrNotFound
}

func (stub *managementRepositoryStub) List(_ context.Context, _, _ string, _ int, cursor *group_list_repository.ListCursor) (*group_list_repository.ListPage, error) {
	stub.listCursor = cursor
	return &stub.listPage, nil
}

func (stub *managementRepositoryStub) ListEntries(context.Context, string, string, int, *group_list_repository.EntryCursor) (*group_list_repository.EntryPage, error) {
	return &stub.entriesPage, nil
}

func (stub *managementRepositoryStub) GetEligibilitySnapshot(context.Context, string, string, int) (*group_list_repository.Summary, []group_list_model.Entry, error) {
	if stub.snapshotErr != nil {
		return nil, nil, stub.snapshotErr
	}
	if stub.snapshot != nil {
		return stub.snapshot, stub.entriesPage.Items, nil
	}
	return &group_list_repository.Summary{GroupList: group_list_model.GroupList{Version: 1}}, stub.entriesPage.Items, nil
}

func (stub *managementRepositoryStub) Update(context.Context, string, string, group_list_repository.UpdateInput) (*group_list_model.GroupList, error) {
	return nil, errors.New("not implemented")
}

func (stub *managementRepositoryStub) Delete(context.Context, string, string, string) error {
	return nil
}

func (stub *managementRepositoryStub) ListAudit(context.Context, string, string, int, *group_list_repository.AuditCursor) (*group_list_repository.AuditPage, error) {
	return &stub.auditPage, nil
}

type managementEligibilityStub struct {
	results []EligibilityResult
	err     error
	calls   *int
}

func (stub managementEligibilityStub) Evaluate(_ context.Context, _, _ string, groupJIDs []string) ([]EligibilityResult, error) {
	if stub.err != nil {
		return nil, stub.err
	}
	if stub.results != nil {
		return stub.results, nil
	}
	results := make([]EligibilityResult, len(groupJIDs))
	for index, groupJID := range groupJIDs {
		results[index] = EligibilityResult{GroupJID: groupJID, CurrentName: "Branch", Eligibility: EligibilityEligible, CanSend: true, CheckedAt: time.Unix(2, 0)}
	}
	return results, nil
}

func (stub managementEligibilityStub) Assess(ctx context.Context, instanceID, instanceJID string, groupJIDs []string) (*EligibilityAssessment, error) {
	if stub.calls != nil {
		*stub.calls++
	}
	results, err := stub.Evaluate(ctx, instanceID, instanceJID, groupJIDs)
	if err != nil {
		return nil, err
	}
	return &EligibilityAssessment{Results: results, Meta: EligibilityMeta{Source: "groups_projection"}}, nil
}

type eligibilityObserverStub struct {
	requests   []string
	rejections []string
}

func (stub *eligibilityObserverStub) ObserveRequest(operation string, _ time.Duration, requested int, _ observability.EligibilityCounts) {
	stub.requests = append(stub.requests, fmt.Sprintf("%s:%d", operation, requested))
}

func (stub *eligibilityObserverStub) ObserveMutationRejection(operation, code string) {
	stub.rejections = append(stub.rejections, operation+":"+code)
}

func (stub managementEligibilityStub) Metadata(string) (EligibilityMeta, error) {
	return EligibilityMeta{Source: "groups_projection"}, stub.err
}

func TestCreateNormalizesSnapshotsAndHashesRawEvidence(t *testing.T) {
	repository := &managementRepositoryStub{}
	service := NewManagementService(repository, managementEligibilityStub{})
	service.now = func() time.Time { return time.Unix(10, 0) }
	instanceID := uuid.NewString()
	result, err := service.Create(context.Background(), instanceID, "5511@s.whatsapp.net", CreateInput{
		Name: "  Northern   Branches ", Description: " Operations ", GroupJIDs: []string{"120363000001@g.us"},
		Authorization:  AuthorizationInput{Source: "operator_attestation", EvidenceReference: "approval-ticket-123", AuthorizedAt: time.Unix(5, 0)},
		ActorReference: "secret-instance-token",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Name != "Northern   Branches" || repository.created.NormalizedName != "northern branches" || result.GroupCount != 1 {
		t.Fatalf("created result = %+v, input = %+v", result, repository.created)
	}
	if repository.created.Description == nil || *repository.created.Description != "Operations" {
		t.Fatalf("description = %v", repository.created.Description)
	}
	if len(repository.created.GroupJIDs) != 1 || repository.created.GroupJIDs[0] != "120363000001@g.us" {
		t.Fatalf("canonical groups = %+v", repository.created.GroupJIDs)
	}
	for _, stored := range []string{repository.created.AuthorizationReferenceHash, repository.created.ActorReferenceHash} {
		if len(stored) != 64 || stored == "approval-ticket-123" || stored == "secret-instance-token" {
			t.Fatalf("unsafe reference persisted: %q", stored)
		}
	}
}

func TestCreateMapsUnknownAndUnavailableEligibility(t *testing.T) {
	instanceID := uuid.NewString()
	base := CreateInput{Name: "Branches", GroupJIDs: []string{"120363000001@g.us"}, Authorization: AuthorizationInput{Source: "operator_attestation", EvidenceReference: "ticket", AuthorizedAt: time.Unix(5, 0)}, ActorReference: "token"}
	for _, test := range []struct {
		state string
		want  error
	}{
		{state: EligibilityUnknown, want: ErrProjectionNotReady},
		{state: EligibilityUnavailable, want: ErrGroupUnavailable},
	} {
		repository := &managementRepositoryStub{createErr: test.want}
		service := NewManagementService(repository, managementEligibilityStub{})
		service.now = func() time.Time { return time.Unix(10, 0) }
		_, err := service.Create(context.Background(), instanceID, "5511@s.whatsapp.net", base)
		if !errors.Is(err, test.want) {
			t.Fatalf("state %s error = %v, want %v", test.state, err, test.want)
		}
	}
}

func TestListCursorIsBoundToInstanceAndSearchScope(t *testing.T) {
	instanceID := uuid.NewString()
	nextID := uuid.NewString()
	repository := &managementRepositoryStub{listPage: group_list_repository.ListPage{NextCursor: &group_list_repository.ListCursor{NormalizedName: "north", ID: nextID}}}
	service := NewManagementService(repository, managementEligibilityStub{})
	first, err := service.List(context.Background(), instanceID, " North ", 10, "")
	if err != nil || first.NextCursor == "" {
		t.Fatalf("first page = %+v, %v", first, err)
	}
	if _, err := service.List(context.Background(), instanceID, "north", 10, first.NextCursor); err != nil {
		t.Fatalf("same-scope cursor rejected: %v", err)
	}
	if repository.listCursor == nil || repository.listCursor.ID != nextID {
		t.Fatalf("decoded cursor = %+v", repository.listCursor)
	}
	if _, err := service.List(context.Background(), uuid.NewString(), "north", 10, first.NextCursor); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("cross-instance cursor error = %v", err)
	}
	if _, err := service.List(context.Background(), instanceID, "south", 10, first.NextCursor); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("cross-search cursor error = %v", err)
	}
}

func TestCreateRejectsEmptyDuplicateAndDirectJIDs(t *testing.T) {
	service := NewManagementService(&managementRepositoryStub{}, managementEligibilityStub{})
	service.now = func() time.Time { return time.Unix(10, 0) }
	base := CreateInput{Name: "Branches", Authorization: AuthorizationInput{Source: "operator_attestation", EvidenceReference: "ticket", AuthorizedAt: time.Unix(5, 0)}, ActorReference: "token"}
	instanceID := uuid.NewString()
	for _, test := range []struct {
		groups []string
		want   error
	}{
		{groups: nil, want: ErrEmpty},
		{groups: []string{"5511@s.whatsapp.net"}, want: ErrInvalidGroup},
		{groups: []string{"120363000001@g.us", "120363000001@g.us"}, want: ErrInvalidGroup},
	} {
		input := base
		input.GroupJIDs = test.groups
		_, err := service.Create(context.Background(), instanceID, "5511@s.whatsapp.net", input)
		if !errors.Is(err, test.want) {
			t.Fatalf("groups %v error = %v, want %v", test.groups, err, test.want)
		}
	}
}

func TestEligibilityAcceptsOneAndOneHundredAndRejectsOneHundredOne(t *testing.T) {
	observer := &eligibilityObserverStub{}
	service := NewManagementService(&managementRepositoryStub{}, managementEligibilityStub{}, WithEligibilityObserver(observer))
	instanceID := uuid.NewString()
	for _, count := range []int{1, 100} {
		groups := make([]string, count)
		for index := range groups {
			groups[index] = fmt.Sprintf("120363%06d@g.us", index)
		}
		assessment, err := service.Eligibility(context.Background(), instanceID, "5511@s.whatsapp.net", groups)
		if err != nil || len(assessment.Results) != count {
			t.Fatalf("count=%d assessment=%+v err=%v", count, assessment, err)
		}
		for index := range groups {
			if assessment.Results[index].GroupJID != groups[index] {
				t.Fatalf("order changed at %d: %s", index, assessment.Results[index].GroupJID)
			}
		}
	}
	groups := make([]string, 101)
	if _, err := service.Eligibility(context.Background(), instanceID, "5511@s.whatsapp.net", groups); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("101-entry error = %v", err)
	}
	if len(observer.requests) != 2 || observer.requests[0] != "batch:1" || observer.requests[1] != "batch:100" {
		t.Fatalf("observations = %v", observer.requests)
	}
}

func TestAggregateEligibilityCountsReasonsAndChecksVersionBeforeAssessment(t *testing.T) {
	listID := uuid.NewString()
	entries := []group_list_model.Entry{{GroupJID: "120363000001@g.us"}, {GroupJID: "120363000002@g.us"}, {GroupJID: "120363000003@g.us"}}
	repository := &managementRepositoryStub{snapshot: &group_list_repository.Summary{GroupList: group_list_model.GroupList{ID: listID, Version: 4}}, entriesPage: group_list_repository.EntryPage{Items: entries}}
	reasonUnavailable, reasonUnknown := ReasonAccessLost, ReasonProjectionNotReady
	results := []EligibilityResult{
		{GroupJID: entries[0].GroupJID, Eligibility: EligibilityEligible, CanSend: true, CheckedAt: time.Unix(10, 0)},
		{GroupJID: entries[1].GroupJID, Eligibility: EligibilityUnavailable, EligibilityReason: &reasonUnavailable, CheckedAt: time.Unix(11, 0)},
		{GroupJID: entries[2].GroupJID, Eligibility: EligibilityUnknown, EligibilityReason: &reasonUnknown, CheckedAt: time.Unix(12, 0)},
	}
	calls := 0
	observer := &eligibilityObserverStub{}
	service := NewManagementService(repository, managementEligibilityStub{results: results, calls: &calls}, WithEligibilityObserver(observer))
	version := int64(4)
	assessment, err := service.AggregateEligibility(context.Background(), uuid.NewString(), "5511@s.whatsapp.net", listID, &version)
	if err != nil || assessment.Data.Total != 3 || assessment.Data.Eligible != 1 || assessment.Data.Unavailable != 1 || assessment.Data.Unknown != 1 || assessment.Data.ReadyToTarget || assessment.Data.ByReason[ReasonAccessLost] != 1 || assessment.Data.ByReason[ReasonProjectionNotReady] != 1 || !assessment.Data.CheckedAt.Equal(time.Unix(12, 0)) {
		t.Fatalf("aggregate=%+v err=%v", assessment, err)
	}
	if calls != 1 || len(observer.requests) != 1 || observer.requests[0] != "aggregate:3" {
		t.Fatalf("calls=%d observations=%v", calls, observer.requests)
	}
	stale := int64(3)
	if _, err := service.AggregateEligibility(context.Background(), uuid.NewString(), "5511@s.whatsapp.net", listID, &stale); !errors.Is(err, group_list_repository.ErrVersionConflict) {
		t.Fatalf("version error=%v", err)
	}
	if calls != 1 {
		t.Fatalf("assessment ran before version rejection: calls=%d", calls)
	}
}

func TestMutationRejectionUsesBoundedStableMetricCode(t *testing.T) {
	observer := &eligibilityObserverStub{}
	service := NewManagementService(&managementRepositoryStub{createErr: ErrProjectionNotReady}, managementEligibilityStub{}, WithEligibilityObserver(observer))
	service.now = func() time.Time { return time.Unix(10, 0) }
	_, _ = service.Create(context.Background(), uuid.NewString(), "5511@s.whatsapp.net", CreateInput{
		Name: "Branches", GroupJIDs: []string{"120363000001@g.us"}, ActorReference: "token",
		Authorization: AuthorizationInput{Source: "operator_attestation", EvidenceReference: "ticket", AuthorizedAt: time.Unix(5, 0)},
	})
	if len(observer.rejections) != 1 || observer.rejections[0] != "create:projection_not_ready" {
		t.Fatalf("rejections=%v", observer.rejections)
	}
}
