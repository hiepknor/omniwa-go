package server_handler

import (
	"net/http"

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	projection_service "github.com/evolution-foundation/evolution-go/pkg/projection/service"
	"github.com/gin-gonic/gin"
)

type ServerHandler interface {
	ServerOk(ctx *gin.Context)
	Capabilities(ctx *gin.Context)
	ProjectionHealth(ctx *gin.Context)
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

func NewServerHandler(version string, projectionState projection_service.StateService) ServerHandler {
	return &serverHandler{version: version, projectionState: projectionState}
}
