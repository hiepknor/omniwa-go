package server_handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/evolution-foundation/evolution-go/pkg/events/outbox"
	"github.com/evolution-foundation/evolution-go/pkg/httpapi"
	"github.com/gin-gonic/gin"
)

const maxExternalEventReplayBodySize = 4 << 10

type ExternalEventReplayRequest struct {
	ID     string `json:"id" binding:"required"`
	Reason string `json:"reason" binding:"required"`
}

// ExternalEventFailures lists payload-free external delivery dead letters.
// @Summary List external event delivery dead letters
// @Tags Server
// @Produce json
// @Param instanceId query string false "Instance ID filter"
// @Param transport query string false "Transport filter (webhook, rabbitmq, nats)"
// @Param limit query int false "Page size (1-200)" default(50)
// @Param cursor query string false "Opaque pagination cursor"
// @Success 200 {object} apidocs.SuccessResponse{data=outbox.DeadLetterList}
// @Failure 400 {object} apidocs.ErrorResponse
// @Failure 401 {object} apidocs.ErrorResponse
// @Failure 500 {object} apidocs.ErrorResponse
// @Security ApiKeyAuth
// @Router /server/external-event-failures [get]
func (s *serverHandler) ExternalEventFailures(ctx *gin.Context) {
	if s.externalEventFailures == nil {
		httpapi.WriteInternal(ctx, nil)
		return
	}
	limit := 50
	if raw := ctx.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			httpapi.WriteError(ctx, http.StatusBadRequest, "invalid_request", "invalid external event failure request")
			return
		}
		limit = parsed
	}
	page, err := s.externalEventFailures.List(ctx.Request.Context(), ctx.Query("instanceId"), outbox.Transport(ctx.Query("transport")), limit, ctx.Query("cursor"))
	if err != nil {
		writeExternalEventFailureError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": page})
}

// ReplayExternalEventFailure atomically requeues one dead letter and records an audit.
// @Summary Replay external event delivery dead letter
// @Tags Server
// @Accept json
// @Produce json
// @Param request body ExternalEventReplayRequest true "Delivery identity and audit reason"
// @Success 200 {object} apidocs.SuccessResponse{data=outbox.ReplayResult}
// @Failure 400 {object} apidocs.ErrorResponse
// @Failure 401 {object} apidocs.ErrorResponse
// @Failure 404 {object} apidocs.ErrorResponse
// @Failure 409 {object} apidocs.ErrorResponse
// @Failure 500 {object} apidocs.ErrorResponse
// @Security ApiKeyAuth
// @Router /server/external-event-failures/replay [post]
func (s *serverHandler) ReplayExternalEventFailure(ctx *gin.Context) {
	if s.externalEventFailures == nil {
		httpapi.WriteInternal(ctx, nil)
		return
	}
	ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, maxExternalEventReplayBodySize)
	var request ExternalEventReplayRequest
	if err := ctx.ShouldBindJSON(&request); err != nil {
		httpapi.WriteError(ctx, http.StatusBadRequest, "invalid_request", "invalid external event failure request")
		return
	}
	result, err := s.externalEventFailures.Replay(ctx.Request.Context(), request.ID, request.Reason, ctx.GetHeader("apikey"), httpapi.RequestID(ctx))
	if err != nil {
		writeExternalEventFailureError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": result})
}

func writeExternalEventFailureError(ctx *gin.Context, err error) {
	switch {
	case errors.Is(err, outbox.ErrInvalidDeadLetterCursor):
		httpapi.WriteError(ctx, http.StatusBadRequest, "invalid_cursor", "invalid external event failure cursor")
	case errors.Is(err, outbox.ErrInvalidDeadLetterRequest):
		httpapi.WriteError(ctx, http.StatusBadRequest, "invalid_request", "invalid external event failure request")
	case errors.Is(err, outbox.ErrDeadLetterNotFound):
		httpapi.WriteError(ctx, http.StatusNotFound, "external_event_failure_not_found", "external event failure was not found")
	case errors.Is(err, outbox.ErrDeadLetterNotActionable):
		httpapi.WriteError(ctx, http.StatusConflict, "external_event_failure_not_actionable", "external event failure is no longer actionable")
	default:
		httpapi.WriteInternal(ctx, err)
	}
}
