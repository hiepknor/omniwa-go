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
	"go.mau.fi/whatsmeow/types"
)

type groupSearchServiceStub struct {
	group_service.GroupService
	term, cursor      string
	limit             int
	managementFilters group_service.GroupManagementFilters
}

func (s *groupSearchServiceStub) SearchGroupsRead(_ context.Context, _ *instance_model.Instance, term string, limit int, cursor string) ([]*types.GroupInfo, *projection_service.ProjectionReadMeta, error) {
	s.term, s.limit, s.cursor = term, limit, cursor
	jid, _ := types.ParseJID("123@g.us")
	return []*types.GroupInfo{{JID: jid}}, &projection_service.ProjectionReadMeta{Source: "projection", NextCursor: "next"}, nil
}

func (s *groupSearchServiceStub) SearchManagementGroups(_ context.Context, _ *instance_model.Instance, filters group_service.GroupManagementFilters, limit int, cursor string) ([]group_service.GroupSummary, *projection_service.ProjectionReadMeta, error) {
	s.managementFilters, s.limit, s.cursor = filters, limit, cursor
	return []group_service.GroupSummary{{GroupJID: "123@g.us", Type: "group", State: "active", MembershipState: "joined", MyRole: "admin", SendMode: "all_members"}},
		&projection_service.ProjectionReadMeta{Source: "projection", NextCursor: "next"}, nil
}

func TestSearchGroupsReturnsProjectionPageAndForwardsFilters(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &groupSearchServiceStub{}
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/group/search?q=Alpha&limit=25&cursor=current", nil)
	ctx.Set("instance", &instance_model.Instance{Id: "instance-a"})
	(&groupHandler{groupService: service}).SearchGroups(ctx)
	if response.Code != http.StatusOK || service.term != "Alpha" || service.limit != 25 || service.cursor != "current" ||
		!strings.Contains(response.Body.String(), `"nextCursor":"next"`) || !strings.Contains(response.Body.String(), `"source":"projection"`) {
		t.Fatalf("status=%d body=%s service=%#v", response.Code, response.Body.String(), service)
	}
}

func TestSearchGroupsRejectsInvalidLimitBeforeService(t *testing.T) {
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/group/search?limit=0", nil)
	ctx.Set("instance", &instance_model.Instance{Id: "instance-a"})
	(&groupHandler{groupService: &groupSearchServiceStub{}}).SearchGroups(ctx)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"invalid_pagination"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestManagementSearchForwardsAuthoritativeFilters(t *testing.T) {
	service := &groupSearchServiceStub{}
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/group/search?q=Branch&type=subgroup&myRole=admin&sendMode=admins_only&state=active&membershipState=joined&limit=25&cursor=current", nil)
	ctx.Set("instance", &instance_model.Instance{Id: "instance-a", Jid: "actor@s.whatsapp.net"})
	(&groupHandler{groupService: service, managementContract: true}).SearchGroups(ctx)
	filters := service.managementFilters
	if response.Code != http.StatusOK || filters.Query != "Branch" || filters.Type != "subgroup" || filters.MyRole != "admin" || filters.SendMode != "admins_only" || filters.State != "active" || filters.MembershipState != "joined" || service.limit != 25 || service.cursor != "current" {
		t.Fatalf("status=%d body=%s filters=%#v", response.Code, response.Body.String(), filters)
	}
	if strings.Contains(response.Body.String(), "participants") || !strings.Contains(response.Body.String(), `"groupJid":"123@g.us"`) {
		t.Fatalf("unexpected public contract: %s", response.Body.String())
	}
}
