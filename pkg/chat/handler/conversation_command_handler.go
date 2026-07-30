package chat_handler

import (
	"context"
	"errors"
	"net/http"
	"time"

	chat_service "github.com/evolution-foundation/evolution-go/pkg/chat/service"
	"github.com/evolution-foundation/evolution-go/pkg/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type conversationCommandService interface {
	SetArchived(context.Context, string, string, bool) (*chat_service.ConversationCommandResult, error)
	SetPinned(context.Context, string, string, bool) (*chat_service.ConversationCommandResult, error)
	SetMuted(context.Context, string, string, time.Duration) (*chat_service.ConversationCommandResult, error)
	RequestHistorySync(context.Context, string, string, chat_service.ConversationHistorySyncInput) (*chat_service.ConversationCommandResult, error)
}

func (c *chatHandler) ConversationAppStateCommandsEnabled() bool {
	return c != nil && c.commands != nil && c.appStateCommandsEnabled
}

func (c *chatHandler) ConversationHistorySyncEnabled() bool {
	return c != nil && c.commands != nil && c.historySyncEnabled
}

// ArchiveConversation requests a provider-confirmed archive state change.
// @Summary Archive a canonical conversation
// @Description Resolves a canonical or absorbed conversation reference to its authoritative provider addressing JID. Direct and group conversations are supported; projection state is refreshed from provider app-state events.
// @Tags Conversations
// @Produce json
// @Param conversationRef path string true "Canonical conversation UUID or absorbed provider identifier"
// @Success 202 {object} apidocs.ConversationCommandResponse "Accepted"
// @Failure 404 {object} apidocs.ErrorResponse "Conversation not found"
// @Failure 422 {object} apidocs.ErrorResponse "Unsupported conversation type"
// @Failure 429 {object} apidocs.OutboundRateLimitResponse "Outbound rate limited"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Failure 502 {object} apidocs.ErrorResponse "Provider command failed"
// @Failure 503 {object} apidocs.ErrorResponse "Projection not ready"
// @Security ApiKeyAuth
// @ID archiveConversation
// @Router /conversations/{conversationRef}/archive [post]
func (c *chatHandler) ArchiveConversation(ctx *gin.Context) {
	c.setConversationArchived(ctx, true)
}

// UnarchiveConversation requests a provider-confirmed unarchive state change.
// @Summary Unarchive a canonical conversation
// @Description Resolves a canonical or absorbed conversation reference to its authoritative provider addressing JID.
// @Tags Conversations
// @Produce json
// @Param conversationRef path string true "Canonical conversation UUID or absorbed provider identifier"
// @Success 202 {object} apidocs.ConversationCommandResponse "Accepted"
// @Failure 404 {object} apidocs.ErrorResponse "Conversation not found"
// @Failure 422 {object} apidocs.ErrorResponse "Unsupported conversation type"
// @Failure 429 {object} apidocs.OutboundRateLimitResponse "Outbound rate limited"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Failure 502 {object} apidocs.ErrorResponse "Provider command failed"
// @Failure 503 {object} apidocs.ErrorResponse "Projection not ready"
// @Security ApiKeyAuth
// @ID unarchiveConversation
// @Router /conversations/{conversationRef}/archive [delete]
func (c *chatHandler) UnarchiveConversation(ctx *gin.Context) {
	c.setConversationArchived(ctx, false)
}

func (c *chatHandler) setConversationArchived(ctx *gin.Context, archived bool) {
	instance, ok := projectionInstance(ctx)
	if !ok {
		return
	}
	result, err := c.commands.SetArchived(ctx.Request.Context(), instance.Id, ctx.Param("conversationRef"), archived)
	writeConversationCommandResult(ctx, result, err)
}

// PinConversation requests a provider-confirmed pin state change.
// @Summary Pin a canonical conversation
// @Tags Conversations
// @Produce json
// @Param conversationRef path string true "Canonical conversation UUID or absorbed provider identifier"
// @Success 202 {object} apidocs.ConversationCommandResponse "Accepted"
// @Failure 404 {object} apidocs.ErrorResponse
// @Failure 422 {object} apidocs.ErrorResponse
// @Failure 429 {object} apidocs.OutboundRateLimitResponse
// @Failure 500 {object} apidocs.ErrorResponse
// @Failure 502 {object} apidocs.ErrorResponse
// @Failure 503 {object} apidocs.ErrorResponse
// @Security ApiKeyAuth
// @ID pinConversation
// @Router /conversations/{conversationRef}/pin [post]
func (c *chatHandler) PinConversation(ctx *gin.Context) { c.setConversationPinned(ctx, true) }

// UnpinConversation requests a provider-confirmed unpin state change.
// @Summary Unpin a canonical conversation
// @Tags Conversations
// @Produce json
// @Param conversationRef path string true "Canonical conversation UUID or absorbed provider identifier"
// @Success 202 {object} apidocs.ConversationCommandResponse "Accepted"
// @Failure 404 {object} apidocs.ErrorResponse
// @Failure 422 {object} apidocs.ErrorResponse
// @Failure 429 {object} apidocs.OutboundRateLimitResponse
// @Failure 500 {object} apidocs.ErrorResponse
// @Failure 502 {object} apidocs.ErrorResponse
// @Failure 503 {object} apidocs.ErrorResponse
// @Security ApiKeyAuth
// @ID unpinConversation
// @Router /conversations/{conversationRef}/pin [delete]
func (c *chatHandler) UnpinConversation(ctx *gin.Context) { c.setConversationPinned(ctx, false) }

func (c *chatHandler) setConversationPinned(ctx *gin.Context, pinned bool) {
	instance, ok := projectionInstance(ctx)
	if !ok {
		return
	}
	result, err := c.commands.SetPinned(ctx.Request.Context(), instance.Id, ctx.Param("conversationRef"), pinned)
	writeConversationCommandResult(ctx, result, err)
}

// MuteConversation requests a finite provider mute.
// @Summary Mute a canonical conversation
// @Description durationSeconds must be between 60 and 31536000. Infinite mute is deliberately not exposed because the canonical projection does not represent it authoritatively.
// @Tags Conversations
// @Accept json
// @Produce json
// @Param conversationRef path string true "Canonical conversation UUID or absorbed provider identifier"
// @Param request body chat_service.ConversationMuteInput true "Finite mute duration"
// @Success 202 {object} apidocs.ConversationCommandResponse "Accepted"
// @Failure 400 {object} apidocs.ErrorResponse
// @Failure 404 {object} apidocs.ErrorResponse
// @Failure 422 {object} apidocs.ErrorResponse
// @Failure 429 {object} apidocs.OutboundRateLimitResponse
// @Failure 500 {object} apidocs.ErrorResponse
// @Failure 502 {object} apidocs.ErrorResponse
// @Failure 503 {object} apidocs.ErrorResponse
// @Security ApiKeyAuth
// @ID muteConversation
// @Router /conversations/{conversationRef}/mute [put]
func (c *chatHandler) MuteConversation(ctx *gin.Context) {
	instance, ok := projectionInstance(ctx)
	if !ok {
		return
	}
	var input chat_service.ConversationMuteInput
	if err := ctx.ShouldBindJSON(&input); err != nil || input.DurationSeconds < 60 || input.DurationSeconds > 31536000 {
		httpapi.WriteError(ctx, http.StatusBadRequest, "invalid_conversation_command", "invalid conversation command")
		return
	}
	result, err := c.commands.SetMuted(ctx.Request.Context(), instance.Id, ctx.Param("conversationRef"), time.Duration(input.DurationSeconds)*time.Second)
	writeConversationCommandResult(ctx, result, err)
}

// UnmuteConversation requests a provider-confirmed unmute.
// @Summary Unmute a canonical conversation
// @Tags Conversations
// @Produce json
// @Param conversationRef path string true "Canonical conversation UUID or absorbed provider identifier"
// @Success 202 {object} apidocs.ConversationCommandResponse "Accepted"
// @Failure 404 {object} apidocs.ErrorResponse
// @Failure 422 {object} apidocs.ErrorResponse
// @Failure 429 {object} apidocs.OutboundRateLimitResponse
// @Failure 500 {object} apidocs.ErrorResponse
// @Failure 502 {object} apidocs.ErrorResponse
// @Failure 503 {object} apidocs.ErrorResponse
// @Security ApiKeyAuth
// @ID unmuteConversation
// @Router /conversations/{conversationRef}/mute [delete]
func (c *chatHandler) UnmuteConversation(ctx *gin.Context) {
	instance, ok := projectionInstance(ctx)
	if !ok {
		return
	}
	result, err := c.commands.SetMuted(ctx.Request.Context(), instance.Id, ctx.Param("conversationRef"), 0)
	writeConversationCommandResult(ctx, result, err)
}

// ConversationHistorySync requests older messages using an authoritative projected anchor.
// @Summary Request history sync for a canonical conversation
// @Description The anchor message must belong to the resolved canonical conversation. The backend derives provider Chat JID, direction, group status, and timestamp; callers never supply provider addressing metadata.
// @Tags Conversations
// @Accept json
// @Produce json
// @Param conversationRef path string true "Canonical conversation UUID or absorbed provider identifier"
// @Param request body chat_service.ConversationHistorySyncInput true "Projected anchor message and bounded count"
// @Success 202 {object} apidocs.ConversationCommandResponse "Accepted"
// @Failure 400 {object} apidocs.ErrorResponse
// @Failure 404 {object} apidocs.ErrorResponse
// @Failure 422 {object} apidocs.ErrorResponse
// @Failure 429 {object} apidocs.OutboundRateLimitResponse
// @Failure 500 {object} apidocs.ErrorResponse
// @Failure 502 {object} apidocs.ErrorResponse
// @Failure 503 {object} apidocs.ErrorResponse
// @Security ApiKeyAuth
// @ID requestConversationHistorySync
// @Router /conversations/{conversationRef}/history-sync [post]
func (c *chatHandler) ConversationHistorySync(ctx *gin.Context) {
	instance, ok := projectionInstance(ctx)
	if !ok {
		return
	}
	var input chat_service.ConversationHistorySyncInput
	if err := ctx.ShouldBindJSON(&input); err != nil {
		httpapi.WriteError(ctx, http.StatusBadRequest, "invalid_conversation_command", "invalid conversation command")
		return
	}
	result, err := c.commands.RequestHistorySync(ctx.Request.Context(), instance.Id, ctx.Param("conversationRef"), input)
	writeConversationCommandResult(ctx, result, err)
}

func writeConversationCommandResult(ctx *gin.Context, result *chat_service.ConversationCommandResult, err error) {
	if err == nil {
		ctx.JSON(http.StatusAccepted, gin.H{"message": "accepted", "data": result})
		return
	}
	var providerErr *chat_service.ConversationProviderError
	switch {
	case errors.Is(err, chat_service.ErrInvalidConversationCommand):
		httpapi.WriteError(ctx, http.StatusBadRequest, "invalid_conversation_command", "invalid conversation command")
	case errors.Is(err, chat_service.ErrConversationProjectionNotReady):
		httpapi.WriteError(ctx, http.StatusServiceUnavailable, "projection_not_ready", "conversation projection is not ready")
	case errors.Is(err, chat_service.ErrUnsupportedConversationCommand):
		httpapi.WriteError(ctx, http.StatusUnprocessableEntity, "unsupported_conversation_operation", "operation is not supported for this conversation type")
	case errors.Is(err, gorm.ErrRecordNotFound):
		httpapi.WriteError(ctx, http.StatusNotFound, "conversation_not_found", "conversation or anchor message not found")
	case httpapi.WriteRateLimit(ctx, err):
		return
	case errors.As(err, &providerErr):
		httpapi.WriteError(ctx, http.StatusBadGateway, "provider_command_failed", "provider command failed; outcome may be unknown")
	default:
		httpapi.WriteInternal(ctx, err)
	}
}
