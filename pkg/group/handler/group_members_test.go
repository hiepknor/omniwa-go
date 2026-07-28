package group_handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	group_service "github.com/evolution-foundation/evolution-go/pkg/group/service"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	projection_service "github.com/evolution-foundation/evolution-go/pkg/projection/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type groupMembersServiceStub struct {
	group_service.GroupService
	groupJID string
	filters  group_service.GroupMemberFilters
	limit    int
	cursor   string
	calls    int
}

func (s *groupMembersServiceStub) ListManagementGroupMembers(_ context.Context, _ *instance_model.Instance, groupJID string, filters group_service.GroupMemberFilters, limit int, cursor string) ([]group_service.GroupMember, *projection_service.ProjectionReadMeta, error) {
	s.calls++
	s.groupJID, s.filters, s.limit, s.cursor = groupJID, filters, limit, cursor
	return []group_service.GroupMember{{MemberID: uuid.NewString(), Role: "member", MembershipState: "active"}}, &projection_service.ProjectionReadMeta{Source: "projection", NextCursor: "next"}, nil
}

func TestListGroupMembersForwardsBoundedProjectionFilters(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &groupMembersServiceStub{}
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/group/123@g.us/members?q=Ali&role=member&limit=25&cursor=current", nil)
	ctx.Params = gin.Params{{Key: "groupJid", Value: "123@g.us"}}
	ctx.Set("instance", &instance_model.Instance{Id: "instance-a", Jid: "actor@s.whatsapp.net"})
	(&groupHandler{groupService: service, managementContract: true}).ListGroupMembers(ctx)
	if response.Code != http.StatusOK || service.calls != 1 || service.groupJID != "123@g.us" || service.filters.Query != "Ali" || service.filters.Role != "member" || service.limit != 25 || service.cursor != "current" || !strings.Contains(response.Body.String(), `"membershipState":"active"`) || strings.Contains(response.Body.String(), "participantId") {
		t.Fatalf("status=%d body=%s service=%#v", response.Code, response.Body.String(), service)
	}
}

func TestListGroupMembersIsUnavailableWhenCapabilitySurfaceIsDisabled(t *testing.T) {
	service := &groupMembersServiceStub{}
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/group/123@g.us/members", nil)
	ctx.Params = gin.Params{{Key: "groupJid", Value: "123@g.us"}}
	(&groupHandler{groupService: service}).ListGroupMembers(ctx)
	if response.Code != http.StatusNotFound || service.calls != 0 || !strings.Contains(response.Body.String(), `"code":"not_found"`) {
		t.Fatalf("status=%d body=%s calls=%d", response.Code, response.Body.String(), service.calls)
	}
}
