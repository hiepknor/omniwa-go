package chat_handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/evolution-foundation/evolution-go/pkg/httpapi"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	projection_service "github.com/evolution-foundation/evolution-go/pkg/projection/service"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type ChatHandler interface {
	ListConversations(ctx *gin.Context)
	GetConversation(ctx *gin.Context)
	ConversationMessages(ctx *gin.Context)
	ConversationMessage(ctx *gin.Context)
	ArchiveConversation(ctx *gin.Context)
	UnarchiveConversation(ctx *gin.Context)
	PinConversation(ctx *gin.Context)
	UnpinConversation(ctx *gin.Context)
	MuteConversation(ctx *gin.Context)
	UnmuteConversation(ctx *gin.Context)
	ConversationHistorySync(ctx *gin.Context)
	ConversationAppStateCommandsEnabled() bool
	ConversationHistorySyncEnabled() bool
}

type chatHandler struct {
	reader                  *projection_service.ChatMessageReader
	commands                conversationCommandService
	appStateCommandsEnabled bool
	historySyncEnabled      bool
}

const defaultProjectionPageSize = 50

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
	ctx.Header("Cache-Control", "private, no-store")
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
	ctx.Header("Cache-Control", "private, no-store")
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
	ctx.Header("Cache-Control", "private, no-store")
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

// ConversationMessage returns one canonical conversation-scoped message.
// @Summary Get a message in a canonical conversation
// @Description Resolves conversationRef through the canonical instance-scoped resolver and returns the message only when it belongs to that canonical conversation. conversationId is required and providerChatId is provenance only.
// @Tags Conversation
// @Produce json
// @Param conversationRef path string true "Canonical conversation UUID or absorbed provider Chat ID"
// @Param messageId path string true "Provider message ID scoped to the canonical conversation"
// @Success 200 {object} apidocs.SuccessResponse{data=projection_service.ProjectedConversationMessage} "success"
// @Failure 404 {object} apidocs.ErrorResponse "Conversation or message not found"
// @Failure 503 {object} apidocs.ErrorResponse "Canonical conversation projection not ready"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Security ApiKeyAuth
// @ID getConversationMessage
// @Router /conversations/{conversationRef}/messages/{messageId} [get]
func (c *chatHandler) ConversationMessage(ctx *gin.Context) {
	ctx.Header("Cache-Control", "private, no-store")
	instance, ok := projectionInstance(ctx)
	if !ok {
		return
	}
	item, meta, err := c.reader.GetConversationMessage(ctx.Request.Context(), instance.Id, ctx.Param("conversationRef"), ctx.Param("messageId"))
	if err != nil {
		writeProjectionReadError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": item, "meta": meta})
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

type Option func(*chatHandler)

func WithConversationCommands(service conversationCommandService, appStateEnabled, historySyncEnabled bool) Option {
	return func(handler *chatHandler) {
		handler.commands = service
		handler.appStateCommandsEnabled = appStateEnabled && service != nil
		handler.historySyncEnabled = historySyncEnabled && service != nil
	}
}

func NewChatHandler(reader *projection_service.ChatMessageReader, options ...Option) ChatHandler {
	handler := &chatHandler{reader: reader}
	for _, option := range options {
		if option != nil {
			option(handler)
		}
	}
	return handler
}
