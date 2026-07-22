package label_handler

import (
	"errors"
	"net/http"

	"github.com/evolution-foundation/evolution-go/pkg/httpapi"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	label_service "github.com/evolution-foundation/evolution-go/pkg/label/service"
	projection_service "github.com/evolution-foundation/evolution-go/pkg/projection/service"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type LabelHandler interface {
	ChatLabel(ctx *gin.Context)
	MessageLabel(ctx *gin.Context)
	EditLabel(ctx *gin.Context)
	ChatUnlabel(ctx *gin.Context)
	MessageUnlabel(ctx *gin.Context)
	GetLabels(ctx *gin.Context)
	GetLabel(ctx *gin.Context)
}

type labelHandler struct {
	labelService label_service.LabelService
}

// Add label to chat
// @Summary Add label to chat
// @Description Add label to chat
// @Tags Label
// @Accept json
// @Produce json
// @Param message body label_service.ChatLabelStruct true "Label data"
// @Success 200 {object} apidocs.SuccessResponse "success"
// @Failure 400 {object} apidocs.ErrorResponse "Error on validation"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Security ApiKeyAuth
// @Router /label/chat [post]
func (l *labelHandler) ChatLabel(ctx *gin.Context) {
	getInstance := ctx.MustGet("instance")

	instance, ok := getInstance.(*instance_model.Instance)
	if !ok {
		httpapi.WriteInternal(ctx, nil)
		return
	}

	var data *label_service.ChatLabelStruct
	err := ctx.ShouldBindBodyWithJSON(&data)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if data.JID == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "jid is required"})
		return
	}

	if data.LabelID == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "label id is required"})
		return
	}

	err = l.labelService.ChatLabel(data, instance)
	if err != nil {
		httpapi.WriteInternal(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "success"})
}

// Add label to message
// @Summary Add label to message
// @Description Add label to message
// @Tags Label
// @Accept json
// @Produce json
// @Param message body label_service.MessageLabelStruct true "Label data"
// @Success 200 {object} apidocs.SuccessResponse "success"
// @Failure 400 {object} apidocs.ErrorResponse "Error on validation"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Security ApiKeyAuth
// @Router /label/message [post]
func (l *labelHandler) MessageLabel(ctx *gin.Context) {
	getInstance := ctx.MustGet("instance")

	instance, ok := getInstance.(*instance_model.Instance)
	if !ok {
		httpapi.WriteInternal(ctx, nil)
		return
	}

	var data *label_service.MessageLabelStruct
	err := ctx.ShouldBindBodyWithJSON(&data)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if data.JID == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "jid is required"})
		return
	}

	if data.LabelID == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "label id is required"})
		return
	}

	if data.MessageID == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "message id is required"})
		return
	}

	err = l.labelService.MessageLabel(data, instance)
	if err != nil {
		httpapi.WriteInternal(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "success"})
}

// Edit label
// @Summary Edit label
// @Description Edit label
// @Tags Label
// @Accept json
// @Produce json
// @Param message body label_service.EditLabelStruct true "Label data"
// @Success 200 {object} apidocs.SuccessResponse "success"
// @Failure 400 {object} apidocs.ErrorResponse "Error on validation"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Security ApiKeyAuth
// @Router /label/edit [post]
func (l *labelHandler) EditLabel(ctx *gin.Context) {
	getInstance := ctx.MustGet("instance")

	instance, ok := getInstance.(*instance_model.Instance)
	if !ok {
		httpapi.WriteInternal(ctx, nil)
		return
	}

	var data *label_service.EditLabelStruct
	err := ctx.ShouldBindBodyWithJSON(&data)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if data.LabelID == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "label id is required"})
		return
	}

	if data.Name == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}

	err = l.labelService.EditLabel(data, instance)
	if err != nil {
		httpapi.WriteInternal(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "success"})
}

// Remove label from chat
// @Summary Remove label from chat
// @Description Remove label from chat
// @Tags Label
// @Accept json
// @Produce json
// @Param message body label_service.ChatLabelStruct true "Label data"
// @Success 200 {object} apidocs.SuccessResponse "success"
// @Failure 400 {object} apidocs.ErrorResponse "Error on validation"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Security ApiKeyAuth
// @Router /unlabel/chat [post]
func (l *labelHandler) ChatUnlabel(ctx *gin.Context) {
	getInstance := ctx.MustGet("instance")

	instance, ok := getInstance.(*instance_model.Instance)
	if !ok {
		httpapi.WriteInternal(ctx, nil)
		return
	}

	var data *label_service.ChatLabelStruct
	err := ctx.ShouldBindBodyWithJSON(&data)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if data.JID == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "jid is required"})
		return
	}

	if data.LabelID == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "label id is required"})
		return
	}

	err = l.labelService.ChatUnlabel(data, instance)
	if err != nil {
		httpapi.WriteInternal(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "success"})
}

// Remove label from message
// @Summary Remove label from message
// @Description Remove label from message
// @Tags Label
// @Accept json
// @Produce json
// @Param message body label_service.MessageLabelStruct true "Label data"
// @Success 200 {object} apidocs.SuccessResponse "success"
// @Failure 400 {object} apidocs.ErrorResponse "Error on validation"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Security ApiKeyAuth
// @Router /unlabel/message [post]
func (l *labelHandler) MessageUnlabel(ctx *gin.Context) {
	getInstance := ctx.MustGet("instance")

	instance, ok := getInstance.(*instance_model.Instance)
	if !ok {
		httpapi.WriteInternal(ctx, nil)
		return
	}

	var data *label_service.MessageLabelStruct
	err := ctx.ShouldBindBodyWithJSON(&data)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if data.JID == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "jid is required"})
		return
	}

	if data.LabelID == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "label id is required"})
		return
	}

	if data.MessageID == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "message id is required"})
		return
	}

	err = l.labelService.MessageUnlabel(data, instance)
	if err != nil {
		httpapi.WriteInternal(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "success"})
}

// Get all labels
// @Summary Get all labels
// @Description Get all labels
// @Tags Label
// @Accept json
// @Produce json
// @Success 200 {array} apidocs.LabelItem "success"
// @Failure 503 {object} apidocs.ErrorResponse "Projection not ready"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Security ApiKeyAuth
// @Router /label/list [get]
func (l *labelHandler) GetLabels(ctx *gin.Context) {
	getInstance := ctx.MustGet("instance")

	instance, ok := getInstance.(*instance_model.Instance)
	if !ok {
		httpapi.WriteInternal(ctx, nil)
		return
	}

	labels, err := l.labelService.GetLabels(ctx.Request.Context(), instance)
	if err != nil {
		writeLabelReadError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, labels)
}

// Get label details
// @Summary Get label details
// @Description Get one label from the persisted instance projection
// @Tags Label
// @Produce json
// @Param labelId path string true "Label ID"
// @Success 200 {object} apidocs.SuccessResponse{data=apidocs.LabelItem} "success"
// @Failure 404 {object} apidocs.ErrorResponse "Label not found"
// @Failure 503 {object} apidocs.ErrorResponse "Projection not ready"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Security ApiKeyAuth
// @Router /label/info/{labelId} [get]
func (l *labelHandler) GetLabel(ctx *gin.Context) {
	instance, ok := ctx.MustGet("instance").(*instance_model.Instance)
	if !ok {
		httpapi.WriteInternal(ctx, nil)
		return
	}
	labelID := ctx.Param("labelId")
	if labelID == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "label id is required"})
		return
	}
	label, meta, err := l.labelService.GetLabel(ctx.Request.Context(), instance, labelID)
	if err != nil {
		writeLabelReadError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": label, "meta": meta})
}

func writeLabelReadError(ctx *gin.Context, err error) {
	switch {
	case errors.Is(err, projection_service.ErrLabelsProjectionNotReady):
		httpapi.WriteError(ctx, http.StatusServiceUnavailable, "projection_not_ready", "labels projection is not ready")
	case errors.Is(err, gorm.ErrRecordNotFound):
		httpapi.WriteError(ctx, http.StatusNotFound, "not_found", "label not found")
	default:
		httpapi.WriteInternal(ctx, err)
	}
}

func NewLabelHandler(
	labelService label_service.LabelService,
) LabelHandler {
	return &labelHandler{
		labelService: labelService,
	}
}
