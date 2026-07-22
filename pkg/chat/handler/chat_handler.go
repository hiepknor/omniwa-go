package chat_handler

import (
	"errors"
	"net/http"
	"strconv"

	chat_service "github.com/evolution-foundation/evolution-go/pkg/chat/service"
	"github.com/evolution-foundation/evolution-go/pkg/httpapi"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	projection_service "github.com/evolution-foundation/evolution-go/pkg/projection/service"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type ChatHandler interface {
	ChatPin(ctx *gin.Context)
	ChatUnpin(ctx *gin.Context)
	ChatArchive(ctx *gin.Context)
	ChatUnarchive(ctx *gin.Context)
	ChatMute(ctx *gin.Context)
	ChatUnmute(ctx *gin.Context)
	HistorySyncRequest(ctx *gin.Context)
	List(ctx *gin.Context)
	Get(ctx *gin.Context)
	Messages(ctx *gin.Context)
}

type chatHandler struct {
	chatService chat_service.ChatService
	reader      *projection_service.ChatMessageReader
}

const defaultProjectionPageSize = 50

// List returns projection-backed chats without querying WhatsApp.
// @Summary List projected chats
// @Tags Chat
// @Produce json
// @Param limit query int false "Page size (1-200)"
// @Param cursor query string false "Opaque pagination cursor"
// @Success 200 {object} apidocs.SuccessResponse{data=[]projection_service.ProjectedChat} "success"
// @Failure 400 {object} apidocs.ErrorResponse "Invalid pagination"
// @Failure 503 {object} apidocs.ErrorResponse "Projection not ready"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Security ApiKeyAuth
// @Router /chat/list [get]
func (c *chatHandler) List(ctx *gin.Context) {
	instance, ok := projectionInstance(ctx)
	if !ok {
		return
	}
	limit, ok := projectionLimit(ctx)
	if !ok {
		return
	}
	items, meta, err := c.reader.ListChats(ctx.Request.Context(), instance.Id, limit, ctx.Query("cursor"))
	if err != nil {
		writeProjectionReadError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": items, "meta": meta})
}

// Get returns one projected chat without querying WhatsApp.
// @Summary Get a projected chat
// @Tags Chat
// @Produce json
// @Param chatId path string true "Chat JID"
// @Success 200 {object} apidocs.SuccessResponse{data=projection_service.ProjectedChat} "success"
// @Failure 404 {object} apidocs.ErrorResponse "Chat not found"
// @Failure 503 {object} apidocs.ErrorResponse "Projection not ready"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Security ApiKeyAuth
// @Router /chat/info/{chatId} [get]
func (c *chatHandler) Get(ctx *gin.Context) {
	instance, ok := projectionInstance(ctx)
	if !ok {
		return
	}
	item, meta, err := c.reader.GetChat(ctx.Request.Context(), instance.Id, ctx.Param("chatId"))
	if err != nil {
		writeProjectionReadError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": item, "meta": meta})
}

// Messages returns stable, projection-backed message history for a chat.
// @Summary List projected messages for a chat
// @Tags Chat
// @Produce json
// @Param chatId path string true "Chat JID"
// @Param limit query int false "Page size (1-200)"
// @Param cursor query string false "Opaque pagination cursor"
// @Success 200 {object} apidocs.SuccessResponse{data=[]projection_service.ProjectedMessage} "success"
// @Failure 400 {object} apidocs.ErrorResponse "Invalid pagination"
// @Failure 503 {object} apidocs.ErrorResponse "Projection not ready"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Security ApiKeyAuth
// @Router /chat/{chatId}/messages [get]
func (c *chatHandler) Messages(ctx *gin.Context) {
	instance, ok := projectionInstance(ctx)
	if !ok {
		return
	}
	limit, ok := projectionLimit(ctx)
	if !ok {
		return
	}
	items, meta, err := c.reader.ListMessages(ctx.Request.Context(), instance.Id, ctx.Param("chatId"), limit, ctx.Query("cursor"))
	if err != nil {
		writeProjectionReadError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": items, "meta": meta})
}

func projectionInstance(ctx *gin.Context) (*instance_model.Instance, bool) {
	instance, ok := ctx.MustGet("instance").(*instance_model.Instance)
	if !ok {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "instance not found"})
	}
	return instance, ok
}

func projectionLimit(ctx *gin.Context) (int, bool) {
	value := ctx.Query("limit")
	if value == "" {
		return defaultProjectionPageSize, true
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit < 1 || limit > 200 {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "limit must be between 1 and 200", "code": "invalid_pagination"})
		return 0, false
	}
	return limit, true
}

func writeProjectionReadError(ctx *gin.Context, err error) {
	switch {
	case errors.Is(err, projection_service.ErrInvalidProjectionCursor):
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error(), "code": "invalid_cursor"})
	case errors.Is(err, projection_service.ErrChatsProjectionNotReady), errors.Is(err, projection_service.ErrMessagesProjectionNotReady):
		ctx.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error(), "code": "projection_not_ready"})
	case errors.Is(err, gorm.ErrRecordNotFound):
		ctx.JSON(http.StatusNotFound, gin.H{"error": "projection record not found", "code": "not_found"})
	default:
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
	}
}

// Pin a chat
// @Summary Pin a chat
// @Description Pin a chat
// @Tags Chat
// @Accept json
// @Produce json
// @Param message body chat_service.BodyStruct true "Chat"
// @Success 200 {object} apidocs.SuccessResponse{data=apidocs.TimestampData} "success"
// @Failure 400 {object} apidocs.ErrorResponse "Error on validation"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Security ApiKeyAuth
// @Router /chat/pin [post]
func (c *chatHandler) ChatPin(ctx *gin.Context) {
	getInstance := ctx.MustGet("instance")

	instance, ok := getInstance.(*instance_model.Instance)
	if !ok {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "instance not found"})
		return
	}

	var data *chat_service.BodyStruct
	err := ctx.ShouldBindBodyWithJSON(&data)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if data.Chat == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "chat is required"})
		return
	}

	ts, err := c.chatService.ChatPin(data, instance)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	responseData := gin.H{
		"timestamp": ts,
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": responseData})
}

// Unpin a chat
// @Summary Unpin a chat
// @Description Unpin a chat
// @Tags Chat
// @Accept json
// @Produce json
// @Param message body chat_service.BodyStruct true "Chat"
// @Success 200 {object} apidocs.SuccessResponse{data=apidocs.TimestampData} "success"
// @Failure 400 {object} apidocs.ErrorResponse "Error on validation"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Security ApiKeyAuth
// @Router /chat/unpin [post]
func (c *chatHandler) ChatUnpin(ctx *gin.Context) {
	getInstance := ctx.MustGet("instance")

	instance, ok := getInstance.(*instance_model.Instance)
	if !ok {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "instance not found"})
		return
	}

	var data *chat_service.BodyStruct
	err := ctx.ShouldBindBodyWithJSON(&data)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if data.Chat == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "chat is required"})
		return
	}

	ts, err := c.chatService.ChatUnpin(data, instance)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	responseData := gin.H{
		"timestamp": ts,
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": responseData})
}

// Archive a chat
// @Summary Archive a chat
// @Description Archive a chat
// @Tags Chat
// @Accept json
// @Produce json
// @Param message body chat_service.BodyStruct true "Chat"
// @Success 200 {object} apidocs.SuccessResponse{data=apidocs.TimestampData} "success"
// @Failure 400 {object} apidocs.ErrorResponse "Error on validation"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Security ApiKeyAuth
// @Router /chat/archive [post]
func (c *chatHandler) ChatArchive(ctx *gin.Context) {
	getInstance := ctx.MustGet("instance")

	instance, ok := getInstance.(*instance_model.Instance)
	if !ok {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "instance not found"})
		return
	}

	var data *chat_service.BodyStruct
	err := ctx.ShouldBindBodyWithJSON(&data)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if data.Chat == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "chat is required"})
		return
	}

	ts, err := c.chatService.ChatArchive(data, instance)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	responseData := gin.H{
		"timestamp": ts,
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": responseData})
}

// Unarchive a chat
// @Summary Unarchive a chat
// @Description Unarchive a chat
// @Tags Chat
// @Accept json
// @Produce json
// @Param message body chat_service.BodyStruct true "Chat"
// @Success 200 {object} apidocs.SuccessResponse{data=apidocs.TimestampData} "success"
// @Failure 400 {object} apidocs.ErrorResponse "Error on validation"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Security ApiKeyAuth
// @Router /chat/unarchive [post]
func (c *chatHandler) ChatUnarchive(ctx *gin.Context) {
	getInstance := ctx.MustGet("instance")

	instance, ok := getInstance.(*instance_model.Instance)
	if !ok {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "instance not found"})
		return
	}

	var data *chat_service.BodyStruct
	err := ctx.ShouldBindBodyWithJSON(&data)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if data.Chat == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "chat is required"})
		return
	}

	ts, err := c.chatService.ChatUnarchive(data, instance)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	responseData := gin.H{
		"timestamp": ts,
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": responseData})
}

// Mute a chat
// @Summary Mute a chat
// @Description Mute a chat
// @Tags Chat
// @Accept json
// @Produce json
// @Param message body chat_service.BodyStruct true "Chat"
// @Success 200 {object} apidocs.SuccessResponse{data=apidocs.TimestampData} "success"
// @Failure 400 {object} apidocs.ErrorResponse "Error on validation"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Security ApiKeyAuth
// @Router /chat/mute [post]
func (c *chatHandler) ChatMute(ctx *gin.Context) {
	getInstance := ctx.MustGet("instance")

	instance, ok := getInstance.(*instance_model.Instance)
	if !ok {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "instance not found"})
		return
	}

	var data *chat_service.BodyStruct
	err := ctx.ShouldBindBodyWithJSON(&data)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if data.Chat == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "chat is required"})
		return
	}

	ts, err := c.chatService.ChatMute(data, instance)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	responseData := gin.H{
		"timestamp": ts,
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": responseData})
}

// Unmute a chat
// @Summary Unmute a chat
// @Description Unmute a chat
// @Tags Chat
// @Accept json
// @Produce json
// @Param message body chat_service.BodyStruct true "Chat"
// @Success 200 {object} apidocs.SuccessResponse{data=apidocs.TimestampData} "success"
// @Failure 400 {object} apidocs.ErrorResponse "Error on validation"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Security ApiKeyAuth
// @Router /chat/unmute [post]
func (c *chatHandler) ChatUnmute(ctx *gin.Context) {
	getInstance := ctx.MustGet("instance")

	instance, ok := getInstance.(*instance_model.Instance)
	if !ok {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "instance not found"})
		return
	}

	var data *chat_service.BodyStruct
	err := ctx.ShouldBindBodyWithJSON(&data)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if data.Chat == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "chat is required"})
		return
	}

	ts, err := c.chatService.ChatUnmute(data, instance)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	responseData := gin.H{
		"timestamp": ts,
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": responseData})
}

// HistorySyncRequest a chat
// @Summary HistorySyncRequest a chat
// @Description HistorySyncRequest a chat
// @Tags Chat
// @Accept json
// @Produce json
// @Param message body chat_service.HistorySyncRequestStruct true "Chat"
// @Success 200 {object} apidocs.SuccessResponse "success"
// @Failure 400 {object} apidocs.ErrorResponse "Error on validation"
// @Failure 429 {object} apidocs.OutboundRateLimitResponse "Outbound rate limited"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Security ApiKeyAuth
// @Router /chat/history-sync [post]
func (c *chatHandler) HistorySyncRequest(ctx *gin.Context) {
	getInstance := ctx.MustGet("instance")

	instance, ok := getInstance.(*instance_model.Instance)
	if !ok {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "instance not found"})
		return
	}

	var data *chat_service.HistorySyncRequestStruct
	err := ctx.ShouldBindBodyWithJSON(&data)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	resp, err := c.chatService.HistorySyncRequest(data, instance)
	if err != nil {
		if httpapi.WriteRateLimit(ctx, err) {
			return
		}
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": resp})
}

func NewChatHandler(
	chatService chat_service.ChatService,
	reader *projection_service.ChatMessageReader,
) ChatHandler {
	return &chatHandler{
		chatService: chatService,
		reader:      reader,
	}
}
