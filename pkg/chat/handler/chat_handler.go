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
	ListConversations(ctx *gin.Context)
	GetConversation(ctx *gin.Context)
	ConversationMessages(ctx *gin.Context)
}

type chatHandler struct {
	chatService chat_service.ChatService
	reader      *projection_service.ChatMessageReader
}

const defaultProjectionPageSize = 50

// List returns projection-backed chats without querying WhatsApp.
// @Summary List projected chats
// @Description Cursor-page projected chats without live reads. With canonical_chat_identity, direct aliases collapse to one conversation, meta.total counts canonical conversations, and cursors are conversation-scoped. Without it, legacy provider-chat rows and cursors are preserved.
// @Tags Chat
// @Produce json
// @Param limit query int false "Page size (1-200)"
// @Param cursor query string false "Opaque pagination cursor"
// @Success 200 {object} apidocs.SuccessResponse{data=[]projection_service.ProjectedChat} "success"
// @Failure 400 {object} apidocs.ErrorResponse "Invalid pagination"
// @Failure 503 {object} apidocs.ErrorResponse "Projection not ready"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Security ApiKeyAuth
// @ID listChats
// @Deprecated
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
// @Description Return a projected chat with locally denormalized contact or type-specific display-name metadata. With canonical_chat_identity, chatId accepts a canonical conversation UUID or any absorbed provider Chat ID and the response contains the canonical conversationId.
// @Tags Chat
// @Produce json
// @Param chatId path string true "Canonical conversation UUID or provider Chat ID"
// @Success 200 {object} apidocs.SuccessResponse{data=projection_service.ProjectedChat} "success"
// @Failure 404 {object} apidocs.ErrorResponse "Chat not found"
// @Failure 503 {object} apidocs.ErrorResponse "Projection not ready"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Security ApiKeyAuth
// @ID getChat
// @Deprecated
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
// @Description With canonical_chat_identity, resolves canonical or absorbed Chat IDs and returns deduplicated provider-message history across all aliases. Cursors are opaque and scoped to the canonical conversation.
// @Tags Chat
// @Produce json
// @Param chatId path string true "Canonical conversation UUID or provider Chat ID"
// @Param limit query int false "Page size (1-200)"
// @Param cursor query string false "Opaque pagination cursor"
// @Success 200 {object} apidocs.SuccessResponse{data=[]projection_service.ProjectedMessage} "success"
// @Failure 400 {object} apidocs.ErrorResponse "Invalid pagination"
// @Failure 503 {object} apidocs.ErrorResponse "Projection not ready"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Security ApiKeyAuth
// @ID listChatMessages
// @Deprecated
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

// ListConversations returns canonical projected conversations.
// @Summary List canonical conversations
// @Description Cursor-page canonical conversations without live WhatsApp reads. conversationId is the entity identity; addressingJid and aliases are provider-addressing metadata. Requires canonical_conversation_identity readiness, and meta.total counts canonical conversations.
// @Tags Conversation
// @Produce json
// @Param limit query int false "Page size (1-200)"
// @Param cursor query string false "Opaque canonical-conversation cursor"
// @Success 200 {object} apidocs.SuccessResponse{data=[]projection_service.ProjectedConversation} "success"
// @Failure 400 {object} apidocs.ErrorResponse "Invalid pagination or cursor"
// @Failure 503 {object} apidocs.ErrorResponse "Canonical conversation projection not ready"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Security ApiKeyAuth
// @ID listConversations
// @Router /conversations [get]
func (c *chatHandler) ListConversations(ctx *gin.Context) {
	instance, ok := projectionInstance(ctx)
	if !ok {
		return
	}
	limit, ok := projectionLimit(ctx)
	if !ok {
		return
	}
	items, meta, err := c.reader.ListConversations(ctx.Request.Context(), instance.Id, limit, ctx.Query("cursor"))
	if err != nil {
		writeProjectionReadError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": items, "meta": meta})
}

// GetConversation returns one canonical projected conversation.
// @Summary Get a canonical conversation
// @Description conversationRef accepts a canonical conversation UUID, a current or absorbed provider Chat ID alias, or an absorbed conversation UUID. The response always normalizes identity to conversationId and never uses addressingJid as entity identity.
// @Tags Conversation
// @Produce json
// @Param conversationRef path string true "Canonical conversation UUID or absorbed provider Chat ID"
// @Success 200 {object} apidocs.SuccessResponse{data=projection_service.ProjectedConversation} "success"
// @Failure 404 {object} apidocs.ErrorResponse "Conversation not found"
// @Failure 503 {object} apidocs.ErrorResponse "Canonical conversation projection not ready"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Security ApiKeyAuth
// @ID getConversation
// @Router /conversations/{conversationRef} [get]
func (c *chatHandler) GetConversation(ctx *gin.Context) {
	instance, ok := projectionInstance(ctx)
	if !ok {
		return
	}
	item, meta, err := c.reader.GetConversation(ctx.Request.Context(), instance.Id, ctx.Param("conversationRef"))
	if err != nil {
		writeProjectionReadError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": item, "meta": meta})
}

// ConversationMessages returns canonical conversation-scoped message history.
// @Summary List messages for a canonical conversation
// @Description conversationRef accepts a canonical or absorbed identifier. Results aggregate all authoritative provider Chat aliases and deduplicate by instance-scoped provider message identity. conversationId is required; providerChatId is provenance only. Cursors are opaque and scoped to the resolved canonical conversation.
// @Tags Conversation
// @Produce json
// @Param conversationRef path string true "Canonical conversation UUID or absorbed provider Chat ID"
// @Param limit query int false "Page size (1-200)"
// @Param cursor query string false "Opaque canonical-conversation message cursor"
// @Success 200 {object} apidocs.SuccessResponse{data=[]projection_service.ProjectedConversationMessage} "success"
// @Failure 400 {object} apidocs.ErrorResponse "Invalid pagination or cursor"
// @Failure 404 {object} apidocs.ErrorResponse "Conversation not found"
// @Failure 503 {object} apidocs.ErrorResponse "Canonical conversation projection not ready"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Security ApiKeyAuth
// @ID listConversationMessages
// @Router /conversations/{conversationRef}/messages [get]
func (c *chatHandler) ConversationMessages(ctx *gin.Context) {
	instance, ok := projectionInstance(ctx)
	if !ok {
		return
	}
	limit, ok := projectionLimit(ctx)
	if !ok {
		return
	}
	items, meta, err := c.reader.ListConversationMessages(ctx.Request.Context(), instance.Id, ctx.Param("conversationRef"), limit, ctx.Query("cursor"))
	if err != nil {
		writeProjectionReadError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": items, "meta": meta})
}

func projectionInstance(ctx *gin.Context) (*instance_model.Instance, bool) {
	instance, ok := ctx.MustGet("instance").(*instance_model.Instance)
	if !ok {
		httpapi.WriteInternal(ctx, nil)
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
		httpapi.WriteError(ctx, http.StatusBadRequest, "invalid_pagination", "limit must be between 1 and 200")
		return 0, false
	}
	return limit, true
}

func writeProjectionReadError(ctx *gin.Context, err error) {
	switch {
	case errors.Is(err, projection_service.ErrInvalidProjectionCursor):
		httpapi.WriteError(ctx, http.StatusBadRequest, "invalid_cursor", "invalid projection cursor")
	case errors.Is(err, projection_service.ErrChatsProjectionNotReady), errors.Is(err, projection_service.ErrMessagesProjectionNotReady):
		httpapi.WriteError(ctx, http.StatusServiceUnavailable, "projection_not_ready", "projection is not ready")
	case errors.Is(err, gorm.ErrRecordNotFound):
		httpapi.WriteError(ctx, http.StatusNotFound, "not_found", "projection record not found")
	default:
		httpapi.WriteInternal(ctx, err)
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
		httpapi.WriteInternal(ctx, nil)
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
		httpapi.WriteInternal(ctx, err)
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
		httpapi.WriteInternal(ctx, nil)
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
		httpapi.WriteInternal(ctx, err)
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
		httpapi.WriteInternal(ctx, nil)
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
		httpapi.WriteInternal(ctx, err)
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
		httpapi.WriteInternal(ctx, nil)
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
		httpapi.WriteInternal(ctx, err)
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
		httpapi.WriteInternal(ctx, nil)
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
		httpapi.WriteInternal(ctx, err)
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
		httpapi.WriteInternal(ctx, nil)
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
		httpapi.WriteInternal(ctx, err)
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
		httpapi.WriteInternal(ctx, nil)
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
		httpapi.WriteInternal(ctx, err)
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
