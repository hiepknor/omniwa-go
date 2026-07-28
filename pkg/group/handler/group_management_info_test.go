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
	projection_service "github.com/evolution-foundation/evolution-go/pkg/projection/service"
	"github.com/gin-gonic/gin"
)

type groupManagementInfoServiceStub struct {
	group_service.GroupService
	groupJID string
}

func (s *groupManagementInfoServiceStub) GetManagementGroupInfo(_ context.Context, _ *group_service.GetGroupInfoStruct, _ *instance_model.Instance) (*group_service.GroupDetail, *projection_service.ProjectionReadMeta, error) {
	checkedAt := time.Unix(100, 0).UTC()
	return &group_service.GroupDetail{
		GroupSummary:  group_service.GroupSummary{GroupJID: "123@g.us", Type: "group", State: "active", MembershipState: "joined", MyRole: "admin", SendMode: "all_members"},
		MemberAddMode: "admins_only", Actions: group_service.GroupActions{SendMessage: group_service.ActionDecision{State: "allowed", CheckedAt: checkedAt}},
	}, &projection_service.ProjectionReadMeta{Source: "projection"}, nil
}

func TestManagementGroupInfoReturnsNormalizedDetailWithoutParticipants(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/group/info", strings.NewReader(`{"groupJid":"123@g.us"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Set("instance", &instance_model.Instance{Id: "instance-a", Jid: "actor@s.whatsapp.net"})
	(&groupHandler{groupService: &groupManagementInfoServiceStub{}, managementContract: true}).GetGroupInfo(ctx)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"actions"`) || !strings.Contains(response.Body.String(), `"state":"allowed"`) || strings.Contains(response.Body.String(), "participants") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestManagementGroupInfoRejectsUnknownJSONFields(t *testing.T) {
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/group/info", strings.NewReader(`{"groupJid":"123@g.us","unexpected":true}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Set("instance", &instance_model.Instance{Id: "instance-a", Jid: "actor@s.whatsapp.net"})
	(&groupHandler{groupService: &groupManagementInfoServiceStub{}, managementContract: true}).GetGroupInfo(ctx)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"invalid_filter"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestManagementGroupInfoRejectsOversizedBody(t *testing.T) {
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	body := `{"groupJid":"` + strings.Repeat("1", int(maxGroupInfoBodyBytes)) + `@g.us"}`
	ctx.Request = httptest.NewRequest(http.MethodPost, "/group/info", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Set("instance", &instance_model.Instance{Id: "instance-a", Jid: "actor@s.whatsapp.net"})
	(&groupHandler{groupService: &groupManagementInfoServiceStub{}, managementContract: true}).GetGroupInfo(ctx)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"invalid_filter"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
