package group_list_handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	group_list_repository "github.com/evolution-foundation/evolution-go/pkg/groupList/repository"
	group_list_service "github.com/evolution-foundation/evolution-go/pkg/groupList/service"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type managementHandlerStub struct {
	createCalls          int
	eligibilityCalls     int
	eligibilityGroupJIDs []string
	eligibilityResult    *group_list_service.EligibilityAssessment
	eligibilityErr       error
	aggregateVersion     *int64
}

func (stub *managementHandlerStub) Create(context.Context, string, string, group_list_service.CreateInput) (*group_list_repository.Summary, error) {
	stub.createCalls++
	return &group_list_repository.Summary{}, nil
}
func (*managementHandlerStub) Update(context.Context, string, string, string, group_list_service.UpdateInput) (*group_list_repository.Summary, error) {
	return &group_list_repository.Summary{}, nil
}
func (*managementHandlerStub) Get(context.Context, string, string) (*group_list_repository.Summary, error) {
	return &group_list_repository.Summary{}, nil
}
func (*managementHandlerStub) List(context.Context, string, string, int, string) (*group_list_service.ListResult, error) {
	return &group_list_service.ListResult{}, nil
}
func (*managementHandlerStub) Entries(context.Context, string, string, string, int, string) (*group_list_service.EntryList, error) {
	return &group_list_service.EntryList{}, nil
}
func (*managementHandlerStub) Delete(context.Context, string, string, string) error { return nil }
func (*managementHandlerStub) Audit(context.Context, string, string, int, string) (*group_list_service.AuditList, error) {
	return &group_list_service.AuditList{}, nil
}
func (stub *managementHandlerStub) Eligibility(_ context.Context, _, _ string, groupJIDs []string) (*group_list_service.EligibilityAssessment, error) {
	stub.eligibilityCalls++
	stub.eligibilityGroupJIDs = append([]string(nil), groupJIDs...)
	if stub.eligibilityErr != nil {
		return nil, stub.eligibilityErr
	}
	return stub.eligibilityResult, nil
}
func (stub *managementHandlerStub) AggregateEligibility(_ context.Context, _, _, _ string, expectedVersion *int64) (*group_list_service.AggregateAssessment, error) {
	stub.aggregateVersion = expectedVersion
	return &group_list_service.AggregateAssessment{Data: group_list_service.EligibilityAggregate{}, Meta: group_list_service.EligibilityMeta{Source: "groups_projection"}}, nil
}

func TestCreateRejectsUnknownFieldsBeforeService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &managementHandlerStub{}
	request := httptest.NewRequest(http.MethodPost, "/group-lists", strings.NewReader(`{
		"name":"Branches",
		"groupJids":["120363000001@g.us"],
		"authorization":{"source":"operator_attestation","evidenceReference":"ticket","authorizedAt":"2026-07-26T10:00:00Z"},
		"expectedVersion":3
	}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = request
	ctx.Set("instance", &instance_model.Instance{Id: uuid.NewString(), Jid: "5511@s.whatsapp.net"})

	New(service).Create(ctx)

	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "invalid_group_list_input") {
		t.Fatalf("response = %d %s", recorder.Code, recorder.Body.String())
	}
	if service.createCalls != 0 {
		t.Fatalf("service called %d times", service.createCalls)
	}
}

func TestEligibilityEndpointsAreDisabledByDefault(t *testing.T) {
	if New(&managementHandlerStub{}).EligibilityEnabled() {
		t.Fatal("eligibility endpoint enabled without feature flag")
	}
	if !New(&managementHandlerStub{}, WithEligibilityEndpoints(true)).EligibilityEnabled() {
		t.Fatal("eligibility endpoint missing with feature flag")
	}
}

func TestUpdateRequiresExpectedVersion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	request := httptest.NewRequest(http.MethodPut, "/group-lists/"+uuid.NewString(), strings.NewReader(`{
		"name":"Branches",
		"groupJids":["120363000001@g.us"],
		"authorization":{"source":"operator_attestation","evidenceReference":"ticket","authorizedAt":"2026-07-26T10:00:00Z"}
	}`))
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = request
	ctx.Params = gin.Params{{Key: "groupListId", Value: uuid.NewString()}}
	ctx.Set("instance", &instance_model.Instance{Id: uuid.NewString(), Jid: "5511@s.whatsapp.net"})

	New(&managementHandlerStub{}).Update(ctx)

	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "invalid_group_list_input") {
		t.Fatalf("response = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestWriteErrorUsesStableGroupListCodes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		err  error
		code int
		body string
	}{
		{group_list_repository.ErrNotFound, http.StatusNotFound, "group_list_not_found"},
		{group_list_repository.ErrNameConflict, http.StatusConflict, "group_list_name_conflict"},
		{group_list_repository.ErrVersionConflict, http.StatusConflict, "group_list_version_conflict"},
		{group_list_service.ErrEmpty, http.StatusBadRequest, "group_list_empty"},
		{group_list_service.ErrInvalidGroup, http.StatusBadRequest, "group_list_invalid_group"},
		{group_list_service.ErrGroupUnavailable, http.StatusConflict, "group_list_group_unavailable"},
		{group_list_service.ErrProjectionNotReady, http.StatusServiceUnavailable, "projection_not_ready"},
	} {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		writeError(ctx, test.err)
		if recorder.Code != test.code || !strings.Contains(recorder.Body.String(), test.body) {
			t.Fatalf("error %v response = %d %s", test.err, recorder.Code, recorder.Body.String())
		}
	}
}

func TestEligibilityReturnsOrderedProjectionContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	checkedAt := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	service := &managementHandlerStub{eligibilityResult: &group_list_service.EligibilityAssessment{
		Results: []group_list_service.EligibilityResult{{GroupJID: "120363000002@g.us", Eligibility: group_list_service.EligibilityEligible, CanSend: true, CheckedAt: checkedAt}, {GroupJID: "120363000001@g.us", Eligibility: group_list_service.EligibilityUnavailable, CheckedAt: checkedAt}},
		Meta:    group_list_service.EligibilityMeta{Source: "groups_projection", SyncStatus: "ready", LastSyncedAt: &checkedAt},
	}}
	request := httptest.NewRequest(http.MethodPost, "/group-lists/eligibility", strings.NewReader(`{"groupJids":["120363000002@g.us","120363000001@g.us"]}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = request
	ctx.Set("instance", &instance_model.Instance{Id: uuid.NewString(), Jid: "5511@s.whatsapp.net"})

	New(service, WithEligibilityEndpoints(true)).Eligibility(ctx)

	if recorder.Code != http.StatusOK || service.eligibilityCalls != 1 || len(service.eligibilityGroupJIDs) != 2 {
		t.Fatalf("response=%d %s calls=%d groups=%v", recorder.Code, recorder.Body.String(), service.eligibilityCalls, service.eligibilityGroupJIDs)
	}
	body := recorder.Body.String()
	if strings.Index(body, "120363000002@g.us") > strings.Index(body, "120363000001@g.us") || !strings.Contains(body, `"source":"groups_projection"`) || !strings.Contains(body, `"syncStatus":"ready"`) {
		t.Fatalf("unexpected eligibility response: %s", body)
	}
}

func TestEligibilityRejectsUnknownFieldsAndOversizedBodyBeforeService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, body := range []string{`{"groupJids":["120363000001@g.us"],"unexpected":true}`, `{"groupJids":["` + strings.Repeat("1", maxEligibilityBodyBytes) + `@g.us"]}`} {
		service := &managementHandlerStub{}
		request := httptest.NewRequest(http.MethodPost, "/group-lists/eligibility", strings.NewReader(body))
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = request
		ctx.Set("instance", &instance_model.Instance{Id: uuid.NewString(), Jid: "5511@s.whatsapp.net"})
		New(service, WithEligibilityEndpoints(true)).Eligibility(ctx)
		if recorder.Code != http.StatusBadRequest || service.eligibilityCalls != 0 {
			t.Fatalf("response=%d %s calls=%d", recorder.Code, recorder.Body.String(), service.eligibilityCalls)
		}
	}
}

func TestAggregateEligibilityParsesExpectedVersion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &managementHandlerStub{}
	request := httptest.NewRequest(http.MethodGet, "/group-lists/id/eligibility?expectedVersion=4", nil)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = request
	ctx.Params = gin.Params{{Key: "groupListId", Value: uuid.NewString()}}
	ctx.Set("instance", &instance_model.Instance{Id: uuid.NewString(), Jid: "5511@s.whatsapp.net"})
	New(service, WithEligibilityEndpoints(true)).AggregateEligibility(ctx)
	if recorder.Code != http.StatusOK || service.aggregateVersion == nil || *service.aggregateVersion != 4 {
		t.Fatalf("response=%d %s expectedVersion=%v", recorder.Code, recorder.Body.String(), service.aggregateVersion)
	}
}

func TestWriteErrorReturnsBoundedEligibilityIssues(t *testing.T) {
	gin.SetMode(gin.TestMode)
	reason := group_list_service.ReasonAccessLost
	err := &group_list_service.EligibilityIssuesError{Cause: group_list_service.ErrGroupUnavailable, Details: group_list_service.EligibilityIssueDetails{
		IssueCount: 1, Issues: []group_list_service.EligibilityIssue{{GroupJID: "120363000001@g.us", Eligibility: group_list_service.EligibilityUnavailable, EligibilityReason: &reason}},
	}}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("requestId", "request-123")
	writeError(ctx, err)
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), `"issueCount":1`) || !strings.Contains(recorder.Body.String(), "group_access_lost") {
		t.Fatalf("response=%d %s", recorder.Code, recorder.Body.String())
	}
	if !errors.Is(err, group_list_service.ErrGroupUnavailable) {
		t.Fatal("structured error lost its stable cause")
	}
}
