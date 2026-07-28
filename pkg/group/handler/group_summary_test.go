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
	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_service "github.com/evolution-foundation/evolution-go/pkg/projection/service"
	"github.com/gin-gonic/gin"
)

type groupSummaryServiceStub struct {
	group_service.GroupService
	err error
}

func (s *groupSummaryServiceStub) GetManagementGroupSummary(context.Context, *instance_model.Instance) (*group_service.GroupDirectorySummary, *projection_service.ProjectionReadMeta, error) {
	if s.err != nil {
		return nil, nil, s.err
	}
	updatedAt := time.Unix(100, 0).UTC()
	return &group_service.GroupDirectorySummary{
		Total: 20, Active: 15, Suspended: 1, Communities: 2, Subgroups: 5, AdminsOnlySend: 4, UpdatedAt: updatedAt,
	}, &projection_service.ProjectionReadMeta{Source: "projection", SyncStatus: projection_model.SyncStatusReady, LastSyncedAt: &updatedAt}, nil
}

func groupSummaryContext() (*gin.Context, *httptest.ResponseRecorder) {
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/group/summary", nil)
	ctx.Set("instance", &instance_model.Instance{Id: "instance-a"})
	return ctx, response
}

func TestGroupSummaryReturnsWholeProjectionAggregate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, response := groupSummaryContext()
	(&groupHandler{groupService: &groupSummaryServiceStub{}, managementContract: true}).GroupSummary(ctx)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"total":20`) ||
		!strings.Contains(response.Body.String(), `"adminsOnlySend":4`) || !strings.Contains(response.Body.String(), `"source":"projection"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestGroupSummaryIsUnavailableWithoutCapabilitySurface(t *testing.T) {
	ctx, response := groupSummaryContext()
	(&groupHandler{groupService: &groupSummaryServiceStub{}}).GroupSummary(ctx)
	if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), `"code":"not_found"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestGroupSummaryFailsClosedForUnreadyProjection(t *testing.T) {
	ctx, response := groupSummaryContext()
	(&groupHandler{groupService: &groupSummaryServiceStub{err: projection_service.ErrGroupsProjectionNotReady}, managementContract: true}).GroupSummary(ctx)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), `"code":"projection_not_ready"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
