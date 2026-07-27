package group_list_service

import (
	"context"
	"errors"
	"testing"
	"time"

	group_list_model "github.com/evolution-foundation/evolution-go/pkg/groupList/model"
	group_list_repository "github.com/evolution-foundation/evolution-go/pkg/groupList/repository"
	"github.com/google/uuid"
)

type managementRepositoryStub struct {
	created     *group_list_repository.CreateInput
	listCursor  *group_list_repository.ListCursor
	listPage    group_list_repository.ListPage
	entriesPage group_list_repository.EntryPage
	auditPage   group_list_repository.AuditPage
}

func (stub *managementRepositoryStub) Create(_ context.Context, input group_list_repository.CreateInput) (*group_list_model.GroupList, error) {
	stub.created = &input
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
	if repository.created.Entries[0].GroupNameSnapshot != "Branch" {
		t.Fatalf("snapshot = %+v", repository.created.Entries[0])
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
		service := NewManagementService(&managementRepositoryStub{}, managementEligibilityStub{results: []EligibilityResult{{GroupJID: base.GroupJIDs[0], Eligibility: test.state}}})
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
