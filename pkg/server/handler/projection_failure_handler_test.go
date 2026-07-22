package server_handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/evolution-foundation/evolution-go/pkg/httpapi"
	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
	projection_service "github.com/evolution-foundation/evolution-go/pkg/projection/service"
	"github.com/gin-gonic/gin"
)

const failureHandlerTestInstanceID = "00000000-0000-0000-0000-000000000001"

type failureHandlerRepositoryStub struct {
	page      *projection_repository.FailurePage
	operation projection_repository.FailureOperation
	err       error
}

func (r *failureHandlerRepositoryStub) ListDeadLetters(context.Context, string, string, int, *projection_repository.FailureCursor) (*projection_repository.FailurePage, error) {
	if r.err != nil {
		return nil, r.err
	}
	return r.page, nil
}

func (r *failureHandlerRepositoryStub) ApplyOperation(_ context.Context, operation projection_repository.FailureOperation) error {
	r.operation = operation
	return r.err
}

func TestProjectionFailureHandlersListAndReplayWithoutLeakingCredential(t *testing.T) {
	gin.SetMode(gin.TestMode)
	deadLetteredAt := time.Unix(200, 0).UTC()
	repository := &failureHandlerRepositoryStub{page: &projection_repository.FailurePage{Items: []projection_repository.FailureRecord{{
		InstanceID: failureHandlerTestInstanceID, Resource: "groups", EventKey: "event-1", EventType: "group_info",
		OccurredAt: time.Unix(100, 0), IngestedAt: time.Unix(150, 0), DeadLetteredAt: &deadLetteredAt,
	}}}}
	failures := projection_service.NewFailureService(repository)
	handler := NewServerHandler("test", "revision", &projectionStateHandlerStub{}, nil, nil, WithFailureService(failures))

	listResponse := httptest.NewRecorder()
	listContext, _ := gin.CreateTestContext(listResponse)
	listContext.Request = httptest.NewRequest(http.MethodGet, "/server/projection-failures?instanceId="+failureHandlerTestInstanceID+"&limit=50", nil)
	handler.ProjectionFailures(listContext)
	if listResponse.Code != http.StatusOK || !strings.Contains(listResponse.Body.String(), `"eventKey":"event-1"`) {
		t.Fatalf("ProjectionFailures() status=%d body=%s", listResponse.Code, listResponse.Body.String())
	}

	requestBody, _ := json.Marshal(ProjectionFailureOperationRequest{
		InstanceID: failureHandlerTestInstanceID, Resource: "groups", EventKey: "event-1", Reason: "projector fix deployed",
	})
	replayResponse := httptest.NewRecorder()
	replayContext, _ := gin.CreateTestContext(replayResponse)
	replayContext.Request = httptest.NewRequest(http.MethodPost, "/server/projection-failures/replay", bytes.NewReader(requestBody))
	replayContext.Request.Header.Set("Content-Type", "application/json")
	replayContext.Request.Header.Set("apikey", "admin-secret")
	replayContext.Request.Header.Set(httpapi.RequestIDHeader, "request-identity-0001")
	httpapi.RequestIdentity()(replayContext)
	handler.ReplayProjectionFailure(replayContext)
	if replayResponse.Code != http.StatusOK || repository.operation.Action != projection_model.FailureActionReplay ||
		repository.operation.ActorReferenceHash == "admin-secret" || strings.Contains(replayResponse.Body.String(), "admin-secret") || strings.Contains(replayResponse.Body.String(), "projector fix deployed") {
		t.Fatalf("ReplayProjectionFailure() status=%d operation=%#v body=%s", replayResponse.Code, repository.operation, replayResponse.Body.String())
	}
}

func TestProjectionFailureHandlerMapsKnownErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "not found", err: projection_repository.ErrProjectionFailureNotFound, status: http.StatusNotFound, code: "projection_failure_not_found"},
		{name: "not actionable", err: projection_repository.ErrProjectionFailureNotActionable, status: http.StatusConflict, code: "projection_failure_not_actionable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &failureHandlerRepositoryStub{err: test.err}
			handler := NewServerHandler("test", "revision", &projectionStateHandlerStub{}, nil, nil, WithFailureService(projection_service.NewFailureService(repository)))
			body := `{"instanceId":"` + failureHandlerTestInstanceID + `","resource":"groups","eventKey":"event-1","reason":"operator decision"}`
			response := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(response)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/server/projection-failures/discard", strings.NewReader(body))
			ctx.Request.Header.Set("Content-Type", "application/json")
			ctx.Request.Header.Set("apikey", "admin-secret")
			ctx.Request.Header.Set(httpapi.RequestIDHeader, "request-identity-0002")
			httpapi.RequestIdentity()(ctx)
			handler.DiscardProjectionFailure(ctx)
			if response.Code != test.status || !strings.Contains(response.Body.String(), `"code":"`+test.code+`"`) {
				t.Fatalf("DiscardProjectionFailure() status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}
