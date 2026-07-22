package server_handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
	projection_service "github.com/evolution-foundation/evolution-go/pkg/projection/service"
	"github.com/gin-gonic/gin"
)

type projectionStateHandlerStub struct{ healthInstanceID string }

func (s *projectionStateHandlerStub) Get(string, string) (*projection_model.State, error) {
	return nil, nil
}

type eventHistoryRepositoryStub struct {
	instanceID string
	page       *projection_repository.DurableEventPage
}

func (s *eventHistoryRepositoryStub) List(_ context.Context, instanceID, _ string, _ int, _ *projection_repository.DurableEventCursor) (*projection_repository.DurableEventPage, error) {
	s.instanceID = instanceID
	return s.page, nil
}
func (s *projectionStateHandlerStub) Ensure(string, string, int64) (*projection_model.State, error) {
	return nil, nil
}

func TestEventHistoryIsInstanceScopedAndRejectsInvalidPagination(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := &eventHistoryRepositoryStub{page: &projection_repository.DurableEventPage{Items: []projection_model.DurableEvent{}}}
	reader := projection_service.NewDurableEventReader(repository, 30*24*time.Hour)
	handler := NewServerHandler("test", &projectionStateHandlerStub{}, reader)

	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/events?limit=10&type=Message", nil)
	ctx.Set("instance", &instance_model.Instance{Id: "instance-a"})
	handler.EventHistory(ctx)
	if response.Code != http.StatusOK || repository.instanceID != "instance-a" || !strings.Contains(response.Body.String(), `"data":[]`) || !strings.Contains(response.Body.String(), `"backfill":false`) {
		t.Fatalf("EventHistory() status=%d instance=%q body=%s", response.Code, repository.instanceID, response.Body.String())
	}

	response = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(response)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/events?cursor=forged", nil)
	ctx.Set("instance", &instance_model.Instance{Id: "instance-a"})
	handler.EventHistory(ctx)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"invalid_cursor"`) {
		t.Fatalf("invalid cursor status=%d body=%s", response.Code, response.Body.String())
	}
}
func (s *projectionStateHandlerStub) RecordEvent(string, string, int64, time.Time) error { return nil }
func (s *projectionStateHandlerStub) MarkSyncing(string, string, int64) error            { return nil }
func (s *projectionStateHandlerStub) MarkReady(string, string, int64, time.Time) error   { return nil }
func (s *projectionStateHandlerStub) MarkStale(string, string, int64) error              { return nil }
func (s *projectionStateHandlerStub) MarkFailed(string, string, int64) error             { return nil }
func (s *projectionStateHandlerStub) Capabilities(string) ([]string, error)              { return nil, nil }
func (s *projectionStateHandlerStub) Health(instanceID string) (*projection_service.ProjectionHealth, error) {
	s.healthInstanceID = instanceID
	return &projection_service.ProjectionHealth{Status: "healthy", GeneratedAt: time.Unix(100, 0), ByStatus: map[string]int{}, Resources: []projection_service.ProjectionResourceHealth{}}, nil
}

func TestProjectionHealthUsesAuthenticationScope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name     string
		instance *instance_model.Instance
		wantID   string
	}{
		{name: "admin scope", wantID: ""},
		{name: "instance scope", instance: &instance_model.Instance{Id: "instance-a"}, wantID: "instance-a"},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := &projectionStateHandlerStub{}
			handler := NewServerHandler("test", state, nil)
			response := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(response)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/server/projection-health", nil)
			if test.instance != nil {
				ctx.Set("instance", test.instance)
			}

			handler.ProjectionHealth(ctx)

			if response.Code != http.StatusOK || state.healthInstanceID != test.wantID {
				t.Fatalf("ProjectionHealth() status=%d scope=%q body=%s", response.Code, state.healthInstanceID, response.Body.String())
			}
		})
	}
}
