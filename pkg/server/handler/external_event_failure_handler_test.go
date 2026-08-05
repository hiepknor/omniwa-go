package server_handler

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/evolution-foundation/evolution-go/pkg/events/outbox"
	"github.com/evolution-foundation/evolution-go/pkg/httpapi"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type externalEventFailureRepositoryStub struct {
	page      *outbox.DeadLetterPage
	operation outbox.ReplayOperation
}

func (r *externalEventFailureRepositoryStub) ListDeadLetters(context.Context, string, outbox.Transport, int, *outbox.DeadLetterCursor) (*outbox.DeadLetterPage, error) {
	return r.page, nil
}
func (r *externalEventFailureRepositoryStub) ReplayDeadLetter(_ context.Context, operation outbox.ReplayOperation) error {
	r.operation = operation
	return nil
}

func TestExternalEventFailureHandlersReturnSafeMetadataAndAuditReplay(t *testing.T) {
	gin.SetMode(gin.TestMode)
	when := time.Date(2026, 8, 5, 2, 0, 0, 0, time.UTC)
	repository := &externalEventFailureRepositoryStub{page: &outbox.DeadLetterPage{Items: []outbox.DeadLetterRecord{{
		ID: uuid.NewString(), InstanceID: uuid.NewString(), Transport: outbox.TransportWebhook,
		Destination: outbox.DestinationInstance, AttemptCount: 12, MaxAttempts: 12, DeadLetteredAt: &when, CreatedAt: when,
	}}}}
	service := outbox.NewFailureService(repository)
	handler := NewServerHandler("test", "revision", nil, nil, nil, WithExternalEventFailureService(service)).(*serverHandler)

	listResponse := httptest.NewRecorder()
	listContext, _ := gin.CreateTestContext(listResponse)
	listContext.Request = httptest.NewRequest(http.MethodGet, "/server/external-event-failures?limit=50", nil)
	handler.ExternalEventFailures(listContext)
	if listResponse.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", listResponse.Code, listResponse.Body.String())
	}
	for _, forbidden := range []string{"payload", "routingKey", "claimToken", "leaseUntil"} {
		if strings.Contains(listResponse.Body.String(), forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, listResponse.Body.String())
		}
	}

	replayResponse := httptest.NewRecorder()
	replayContext, _ := gin.CreateTestContext(replayResponse)
	replayContext.Request = httptest.NewRequest(http.MethodPost, "/server/external-event-failures/replay", bytes.NewBufferString(`{"id":"`+repository.page.Items[0].ID+`","reason":"transport recovered"}`))
	replayContext.Request.Header.Set("Content-Type", "application/json")
	replayContext.Request.Header.Set("apikey", "admin-secret")
	replayContext.Request.Header.Set(httpapi.RequestIDHeader, "request-replay-0003")
	httpapi.RequestIdentity()(replayContext)
	handler.ReplayExternalEventFailure(replayContext)
	if replayResponse.Code != http.StatusOK || repository.operation.DeliveryID != repository.page.Items[0].ID ||
		repository.operation.ActorReferenceHash == "" || strings.Contains(repository.operation.ActorReferenceHash, "admin-secret") {
		t.Fatalf("status=%d operation=%#v body=%s", replayResponse.Code, repository.operation, replayResponse.Body.String())
	}
}
