package group_handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	group_service "github.com/evolution-foundation/evolution-go/pkg/group/service"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	"github.com/evolution-foundation/evolution-go/pkg/outbound"
	"github.com/gin-gonic/gin"
)

type groupManagementCommandServiceStub struct {
	group_service.GroupService
	result   *group_service.CommandAcknowledgement
	err      error
	metadata group_service.ManagementCommandMetadata
}

func (s *groupManagementCommandServiceStub) ExecuteSetGroupName(_ context.Context, _ *group_service.SetGroupNameStruct, _ *instance_model.Instance, metadata group_service.ManagementCommandMetadata) (*group_service.CommandAcknowledgement, error) {
	s.metadata = metadata
	return s.result, s.err
}

func managementCommandContext(body string) (*gin.Context, *httptest.ResponseRecorder) {
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/group/name", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Request.Header.Set("apikey", "instance-secret")
	ctx.Request.Header.Set("Idempotency-Key", "operator-action-1")
	ctx.Set("instance", &instance_model.Instance{Id: "instance-a", Jid: "actor@s.whatsapp.net"})
	return ctx, response
}

func TestManagementCommandHandlerReturnsTypedAcknowledgement(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &groupManagementCommandServiceStub{result: &group_service.CommandAcknowledgement{
		CommandID: "f6ad67ec-23dc-4130-a898-233cc67df2a2", Command: "name_updated", GroupJID: "123@g.us", Status: "completed", ProjectionRefreshExpected: true,
	}}
	ctx, response := managementCommandContext(`{"groupJid":"123@g.us","name":"Updated"}`)
	(&groupHandler{groupService: service, managementContract: true}).SetGroupName(ctx)

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"completed"`) || strings.Contains(response.Body.String(), "instance-secret") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if service.metadata.ActorReference != "instance-secret" || service.metadata.IdempotencyKey != "operator-action-1" {
		t.Fatalf("metadata = %#v", service.metadata)
	}
}

func TestManagementCommandHandlerRejectsUnknownFields(t *testing.T) {
	ctx, response := managementCommandContext(`{"groupJid":"123@g.us","name":"Updated","retry":true}`)
	(&groupHandler{groupService: &groupManagementCommandServiceStub{}, managementContract: true}).SetGroupName(ctx)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"invalid_request"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestManagementCommandHandlerReturnsRetryAfter(t *testing.T) {
	ctx, response := managementCommandContext(`{"groupJid":"123@g.us","name":"Updated"}`)
	service := &groupManagementCommandServiceStub{err: &outbound.RateLimitError{RetryAfter: 2 * time.Second}}
	(&groupHandler{groupService: service, managementContract: true}).SetGroupName(ctx)
	if response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") != "2" || !strings.Contains(response.Body.String(), `"code":"outbound_rate_limited"`) {
		t.Fatalf("status=%d retry=%q body=%s", response.Code, response.Header().Get("Retry-After"), response.Body.String())
	}
}
