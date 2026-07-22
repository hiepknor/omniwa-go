package server_handler

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/evolution-foundation/evolution-go/pkg/httpapi"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	projection_service "github.com/evolution-foundation/evolution-go/pkg/projection/service"
	"github.com/gin-gonic/gin"
)

type ServerHandler interface {
	ServerOk(ctx *gin.Context)
	Capabilities(ctx *gin.Context)
	ProjectionHealth(ctx *gin.Context)
	EventHistory(ctx *gin.Context)
	Overview(ctx *gin.Context)
	Health(ctx *gin.Context)
	ProjectionFailures(ctx *gin.Context)
	ReplayProjectionFailure(ctx *gin.Context)
	DiscardProjectionFailure(ctx *gin.Context)
}

// ProjectionHealth returns persisted projection synchronization metrics.
// @Summary Get projection health metrics
// @Tags Server
// @Produce json
// @Success 200 {object} apidocs.SuccessResponse{data=projection_service.ProjectionHealth} "success"
// @Failure 401 {object} apidocs.ErrorResponse "Not authorized"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Security ApiKeyAuth
// @Router /server/projection-health [get]
func (s *serverHandler) ProjectionHealth(ctx *gin.Context) {
	instanceID := ""
	if value, exists := ctx.Get("instance"); exists {
		if instance, ok := value.(*instance_model.Instance); ok {
			instanceID = instance.Id
		}
	}
	health, err := s.projectionState.Health(instanceID)
	if err != nil {
		httpapi.WriteInternal(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": health})
}

type serverHandler struct {
	version           string
	revision          string
	projectionState   projection_service.StateService
	eventReader       *projection_service.DurableEventReader
	overview          *projection_service.OverviewService
	health            *projection_service.ServerHealthService
	failures          *projection_service.FailureService
	adminCapabilities []string
}

// Health returns independent API, connection, projection, and throttling dimensions.
// @Summary Get split server health
// @Tags Server
// @Produce json
// @Success 200 {object} apidocs.SuccessResponse{data=projection_service.ServerHealth} "success"
// @Failure 401 {object} apidocs.ErrorResponse "Not authorized"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Security ApiKeyAuth
// @Router /server/health [get]
func (s *serverHandler) Health(ctx *gin.Context) {
	instanceID := ""
	if value, exists := ctx.Get("instance"); exists {
		if instance, ok := value.(*instance_model.Instance); ok {
			instanceID = instance.Id
		}
	}
	if s.health == nil {
		httpapi.WriteInternal(ctx, nil)
		return
	}
	health, err := s.health.Snapshot(ctx.Request.Context(), instanceID)
	if err != nil {
		httpapi.WriteInternal(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": health})
}

// Overview returns persisted metrics without querying WhatsApp.
// @Summary Get persisted overview metrics
// @Tags Server
// @Produce json
// @Param window query string false "Metric window as a Go duration (maximum 720h)" default(24h)
// @Success 200 {object} apidocs.SuccessResponse{data=projection_service.Overview} "success"
// @Failure 400 {object} apidocs.ErrorResponse "Invalid metric window"
// @Failure 401 {object} apidocs.ErrorResponse "Not authorized"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Security ApiKeyAuth
// @Router /server/overview [get]
func (s *serverHandler) Overview(ctx *gin.Context) {
	window := projection_service.DefaultOverviewWindow
	if value := ctx.Query("window"); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed <= 0 || parsed > projection_service.MaximumOverviewWindow {
			ctx.JSON(http.StatusBadRequest, gin.H{"error": "window must be a positive duration no greater than 720h", "code": "invalid_window"})
			return
		}
		window = parsed
	}
	instanceID := ""
	if value, exists := ctx.Get("instance"); exists {
		if instance, ok := value.(*instance_model.Instance); ok {
			instanceID = instance.Id
		}
	}
	if s.overview == nil {
		httpapi.WriteInternal(ctx, nil)
		return
	}
	overview, err := s.overview.Snapshot(ctx.Request.Context(), instanceID, window)
	if err != nil {
		httpapi.WriteInternal(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": overview})
}

// EventHistory returns retention-bound normalized events without querying WhatsApp.
// @Summary List durable event history
// @Tags Events
// @Produce json
// @Param limit query int false "Page size (1-200)" default(50)
// @Param cursor query string false "Opaque pagination cursor"
// @Param type query string false "Exact event type"
// @Success 200 {object} apidocs.SuccessResponse{data=[]projection_service.DurableEventHistoryItem,meta=projection_service.DurableEventHistoryMeta} "success"
// @Failure 400 {object} apidocs.ErrorResponse "Invalid pagination"
// @Failure 401 {object} apidocs.ErrorResponse "Not authorized"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Security ApiKeyAuth
// @Router /events [get]
func (s *serverHandler) EventHistory(ctx *gin.Context) {
	value, exists := ctx.Get("instance")
	instance, ok := value.(*instance_model.Instance)
	if !exists || !ok {
		httpapi.WriteInternal(ctx, nil)
		return
	}
	limit := 50
	if value := ctx.Query("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 200 {
			ctx.JSON(http.StatusBadRequest, gin.H{"error": "limit must be between 1 and 200", "code": "invalid_pagination"})
			return
		}
		limit = parsed
	}
	eventType := strings.TrimSpace(ctx.Query("type"))
	if len(eventType) > 64 {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "type must not exceed 64 characters", "code": "invalid_filter"})
		return
	}
	if s.eventReader == nil {
		httpapi.WriteInternal(ctx, nil)
		return
	}
	items, meta, err := s.eventReader.List(ctx.Request.Context(), instance.Id, eventType, limit, ctx.Query("cursor"))
	if errors.Is(err, projection_service.ErrInvalidDurableEventCursor) {
		httpapi.WriteError(ctx, http.StatusBadRequest, "invalid_cursor", "invalid event cursor")
		return
	}
	if err != nil {
		httpapi.WriteInternal(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": items, "meta": meta})
}

// ServerOk implements ServerHandler.
func (s *serverHandler) ServerOk(ctx *gin.Context) {
	ctx.JSON(200, gin.H{
		"status": "ok",
	})
}

// Capabilities returns non-sensitive server and instance capability metadata.
// @Summary Get server capabilities
// @Tags Server
// @Produce json
// @Success 200 {object} apidocs.CapabilitiesResponse "success"
// @Failure 401 {object} apidocs.ErrorResponse "Not authorized"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Security ApiKeyAuth
// @Router /server/capabilities [get]
func (s *serverHandler) Capabilities(ctx *gin.Context) {
	instanceID := ""
	if value, exists := ctx.Get("instance"); exists {
		if instance, ok := value.(*instance_model.Instance); ok {
			instanceID = instance.Id
		}
	}
	capabilities, err := s.projectionState.Capabilities(instanceID)
	if err != nil {
		httpapi.WriteInternal(ctx, err)
		return
	}
	if ctx.GetString("auth_scope") == "admin" {
		capabilities = append(capabilities, s.adminCapabilities...)
		sort.Strings(capabilities)
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": gin.H{"version": s.version, "revision": s.revision, "capabilities": capabilities}})
}

type ServerOption func(*serverHandler)

func WithHealthService(health *projection_service.ServerHealthService) ServerOption {
	return func(handler *serverHandler) { handler.health = health }
}

func WithFailureService(failures *projection_service.FailureService) ServerOption {
	return func(handler *serverHandler) { handler.failures = failures }
}

func WithAdminCapabilities(capabilities ...string) ServerOption {
	return func(handler *serverHandler) { handler.adminCapabilities = append([]string(nil), capabilities...) }
}

func NewServerHandler(version, revision string, projectionState projection_service.StateService, eventReader *projection_service.DurableEventReader, overview *projection_service.OverviewService, options ...ServerOption) ServerHandler {
	handler := &serverHandler{version: version, revision: revision, projectionState: projectionState, eventReader: eventReader, overview: overview}
	for _, option := range options {
		if option != nil {
			option(handler)
		}
	}
	return handler
}
