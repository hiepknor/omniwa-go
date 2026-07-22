package server_handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/evolution-foundation/evolution-go/pkg/httpapi"
	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
	projection_service "github.com/evolution-foundation/evolution-go/pkg/projection/service"
	"github.com/gin-gonic/gin"
)

const (
	defaultProjectionFailurePageSize = 50
	maxProjectionFailureBodySize     = 4 << 10
)

type ProjectionFailureOperationRequest struct {
	InstanceID string `json:"instanceId" binding:"required"`
	Resource   string `json:"resource" binding:"required"`
	EventKey   string `json:"eventKey" binding:"required"`
	Reason     string `json:"reason" binding:"required"`
}

// ProjectionFailures lists safe dead-letter summaries without payloads or entity identifiers.
// @Summary List projection dead letters
// @Tags Server
// @Produce json
// @Param instanceId query string false "Instance ID filter"
// @Param resource query string false "Projection resource filter"
// @Param limit query int false "Page size (1-200)" default(50)
// @Param cursor query string false "Opaque pagination cursor"
// @Success 200 {object} apidocs.SuccessResponse{data=projection_service.ProjectionFailurePage}
// @Failure 400 {object} apidocs.ErrorResponse
// @Failure 401 {object} apidocs.ErrorResponse
// @Failure 500 {object} apidocs.ErrorResponse
// @Security ApiKeyAuth
// @Router /server/projection-failures [get]
func (s *serverHandler) ProjectionFailures(ctx *gin.Context) {
	if s.failures == nil {
		httpapi.WriteInternal(ctx, nil)
		return
	}
	limit := defaultProjectionFailurePageSize
	if raw := ctx.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			httpapi.WriteError(ctx, http.StatusBadRequest, "invalid_request", "invalid projection failure request")
			return
		}
		limit = parsed
	}
	page, err := s.failures.List(ctx.Request.Context(), ctx.Query("instanceId"), ctx.Query("resource"), limit, ctx.Query("cursor"))
	if err != nil {
		writeProjectionFailureError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": page})
}

// ReplayProjectionFailure requeues one dead letter and resets its retry budget atomically with an audit record.
// @Summary Replay projection dead letter
// @Tags Server
// @Accept json
// @Produce json
// @Param request body ProjectionFailureOperationRequest true "Failure identity and audit reason"
// @Success 200 {object} apidocs.SuccessResponse{data=projection_service.ProjectionFailureOperationResult}
// @Failure 400 {object} apidocs.ErrorResponse
// @Failure 401 {object} apidocs.ErrorResponse
// @Failure 404 {object} apidocs.ErrorResponse
// @Failure 409 {object} apidocs.ErrorResponse
// @Failure 500 {object} apidocs.ErrorResponse
// @Security ApiKeyAuth
// @Router /server/projection-failures/replay [post]
func (s *serverHandler) ReplayProjectionFailure(ctx *gin.Context) {
	s.projectionFailureOperation(ctx, projection_model.FailureActionReplay)
}

// DiscardProjectionFailure terminally discards one dead letter atomically with an audit record.
// @Summary Discard projection dead letter
// @Tags Server
// @Accept json
// @Produce json
// @Param request body ProjectionFailureOperationRequest true "Failure identity and audit reason"
// @Success 200 {object} apidocs.SuccessResponse{data=projection_service.ProjectionFailureOperationResult}
// @Failure 400 {object} apidocs.ErrorResponse
// @Failure 401 {object} apidocs.ErrorResponse
// @Failure 404 {object} apidocs.ErrorResponse
// @Failure 409 {object} apidocs.ErrorResponse
// @Failure 500 {object} apidocs.ErrorResponse
// @Security ApiKeyAuth
// @Router /server/projection-failures/discard [post]
func (s *serverHandler) DiscardProjectionFailure(ctx *gin.Context) {
	s.projectionFailureOperation(ctx, projection_model.FailureActionDiscard)
}

func (s *serverHandler) projectionFailureOperation(ctx *gin.Context, action projection_model.FailureAction) {
	if s.failures == nil {
		httpapi.WriteInternal(ctx, nil)
		return
	}
	ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, maxProjectionFailureBodySize)
	var request ProjectionFailureOperationRequest
	if err := ctx.ShouldBindJSON(&request); err != nil {
		httpapi.WriteError(ctx, http.StatusBadRequest, "invalid_request", "invalid projection failure request")
		return
	}
	result, err := s.failures.Operate(
		ctx.Request.Context(), request.InstanceID, request.Resource, request.EventKey, action, request.Reason,
		ctx.GetHeader("apikey"), httpapi.RequestID(ctx),
	)
	if err != nil {
		writeProjectionFailureError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": result})
}

func writeProjectionFailureError(ctx *gin.Context, err error) {
	switch {
	case errors.Is(err, projection_service.ErrInvalidProjectionFailureCursor):
		httpapi.WriteError(ctx, http.StatusBadRequest, "invalid_cursor", "invalid projection failure cursor")
	case errors.Is(err, projection_service.ErrInvalidProjectionFailureRequest):
		httpapi.WriteError(ctx, http.StatusBadRequest, "invalid_request", "invalid projection failure request")
	case errors.Is(err, projection_repository.ErrProjectionFailureNotFound):
		httpapi.WriteError(ctx, http.StatusNotFound, "projection_failure_not_found", "projection failure was not found")
	case errors.Is(err, projection_repository.ErrProjectionFailureNotActionable):
		httpapi.WriteError(ctx, http.StatusConflict, "projection_failure_not_actionable", "projection failure is no longer actionable")
	default:
		httpapi.WriteInternal(ctx, err)
	}
}
