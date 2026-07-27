package group_list_handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	group_list_repository "github.com/evolution-foundation/evolution-go/pkg/groupList/repository"
	group_list_service "github.com/evolution-foundation/evolution-go/pkg/groupList/service"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type managementHandlerStub struct {
	createCalls int
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
