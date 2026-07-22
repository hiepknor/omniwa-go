package server_handler

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	projection_service "github.com/evolution-foundation/evolution-go/pkg/projection/service"
	"github.com/gin-gonic/gin"
)

type ServerHandler interface {
	ServerOk(ctx *gin.Context)
	Capabilities(ctx *gin.Context)
	ProjectionHealth(ctx *gin.Context)
	EventHistory(ctx *gin.Context)
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
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": health})
}

type serverHandler struct {
	version         string
	projectionState projection_service.StateService
	eventReader     *projection_service.DurableEventReader
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
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "instance not found"})
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
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	items, meta, err := s.eventReader.List(ctx.Request.Context(), instance.Id, eventType, limit, ctx.Query("cursor"))
	if errors.Is(err, projection_service.ErrInvalidDurableEventCursor) {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error(), "code": "invalid_cursor"})
		return
	}
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
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
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": gin.H{"version": s.version, "capabilities": capabilities}})
}

func NewServerHandler(version string, projectionState projection_service.StateService, eventReaders ...*projection_service.DurableEventReader) ServerHandler {
	var eventReader *projection_service.DurableEventReader
	if len(eventReaders) > 0 {
		eventReader = eventReaders[0]
	}
	return &serverHandler{version: version, projectionState: projectionState, eventReader: eventReader}
}
