package group_handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	group_service "github.com/evolution-foundation/evolution-go/pkg/group/service"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	"github.com/gin-gonic/gin"
	"go.mau.fi/whatsmeow/types"
)

type legacyMyAllServiceStub struct {
	group_service.GroupService
	calls int
}

func (s *legacyMyAllServiceStub) GetMyGroups(context.Context, *instance_model.Instance) ([]types.GroupInfo, error) {
	s.calls++
	jid, _ := types.ParseJID("123@g.us")
	return []types.GroupInfo{{JID: jid}}, nil
}

func TestLegacyMyAllReturnsDeprecationAndSuccessorHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &legacyMyAllServiceStub{}
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/group/myall", nil)
	ctx.Set("instance", &instance_model.Instance{Id: "instance-a"})
	NewGroupHandler(service).GetMyGroups(ctx)
	if response.Code != http.StatusOK || service.calls != 1 || response.Header().Get("Deprecation") != "@1785888000" ||
		response.Header().Get("Sunset") == "" || !strings.Contains(response.Header().Get("Link"), "/group/search") {
		t.Fatalf("status=%d calls=%d headers=%v body=%s", response.Code, service.calls, response.Header(), response.Body.String())
	}
}

func TestLegacyMyAllKillSwitchReturnsGoneWithoutProviderCall(t *testing.T) {
	service := &legacyMyAllServiceStub{}
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/group/myall", nil)
	NewGroupHandler(service, WithLegacyMyAll(false)).GetMyGroups(ctx)
	if response.Code != http.StatusGone || service.calls != 0 || !strings.Contains(response.Body.String(), `"code":"endpoint_retired"`) ||
		!strings.Contains(response.Header().Get("Link"), "/group/search") {
		t.Fatalf("status=%d calls=%d headers=%v body=%s", response.Code, service.calls, response.Header(), response.Body.String())
	}
}
