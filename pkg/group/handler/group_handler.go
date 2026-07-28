package group_handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	group_repository "github.com/evolution-foundation/evolution-go/pkg/group/repository"
	group_service "github.com/evolution-foundation/evolution-go/pkg/group/service"
	"github.com/evolution-foundation/evolution-go/pkg/httpapi"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	projection_service "github.com/evolution-foundation/evolution-go/pkg/projection/service"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const maxGroupInfoBodyBytes int64 = 16 << 10
const maxGroupManagementBodyBytes int64 = 64 << 10

type GroupHandler interface {
	ListGroups(ctx *gin.Context)
	SearchGroups(ctx *gin.Context)
	GroupSummary(ctx *gin.Context)
	ListGroupMembers(ctx *gin.Context)
	GroupAudit(ctx *gin.Context)
	GetGroupInfo(ctx *gin.Context)
	GetGroupInviteLink(ctx *gin.Context)
	SetGroupPhoto(ctx *gin.Context)
	SetGroupName(ctx *gin.Context)
	SetGroupDescription(ctx *gin.Context)
	CreateGroup(ctx *gin.Context)
	UpdateParticipant(ctx *gin.Context)
	GetMyGroups(ctx *gin.Context)
	JoinGroupLink(ctx *gin.Context)
	LeaveGroup(ctx *gin.Context)
	UpdateGroupSettings(ctx *gin.Context)
	ManagementContractEnabled() bool
	PhotoAssetsEnabled() bool
}

// GroupSummary returns authoritative instance-wide directory aggregates.
// @Summary Get authoritative group summary
// @Description Aggregate the complete normalized Group projection for the authenticated instance. Counts are never derived from a directory page and the endpoint never calls WhatsApp.
// @Tags Group
// @Produce json
// @Success 200 {object} apidocs.SuccessResponse{data=group_service.GroupDirectorySummary} "success"
// @Failure 404 {object} apidocs.ErrorResponse "not_found when the normalized management contract is disabled"
// @Failure 503 {object} apidocs.ErrorResponse "projection_not_ready"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Security ApiKeyAuth
// @Router /group/summary [get]
func (g *groupHandler) GroupSummary(ctx *gin.Context) {
	if !g.managementContract {
		httpapi.WriteError(ctx, http.StatusNotFound, "not_found", "group summary is not enabled")
		return
	}
	instance, ok := ctx.MustGet("instance").(*instance_model.Instance)
	if !ok {
		httpapi.WriteInternal(ctx, nil)
		return
	}
	summary, meta, err := g.groupService.GetManagementGroupSummary(ctx.Request.Context(), instance)
	if err != nil {
		writeGroupProjectionReadError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": summary, "meta": meta})
}

// GroupAudit returns terminal, public-safe management command history.
// @Summary Get group management audit history
// @Description List terminal management command outcomes without provider payloads, aliases, invite links, media, or credentials.
// @Tags Group
// @Produce json
// @Param groupJid path string true "Canonical WhatsApp Group JID"
// @Param limit query int false "Page size (1-200)" minimum(1) maximum(200) default(50)
// @Param cursor query string false "Opaque cursor bound to instance and group"
// @Success 200 {object} apidocs.SuccessResponse{data=[]group_service.ManagementAuditEvent} "success"
// @Failure 400 {object} apidocs.ErrorResponse "invalid_cursor or invalid_pagination"
// @Failure 404 {object} apidocs.ErrorResponse "not_found"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Security ApiKeyAuth
// @Router /group/{groupJid}/audit [get]
func (g *groupHandler) GroupAudit(ctx *gin.Context) {
	if !g.managementContract {
		httpapi.WriteError(ctx, http.StatusNotFound, "not_found", "group management audit is not enabled")
		return
	}
	instance, ok := ctx.MustGet("instance").(*instance_model.Instance)
	if !ok {
		httpapi.WriteInternal(ctx, nil)
		return
	}
	limit := 50
	if value := ctx.Query("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 200 {
			httpapi.WriteError(ctx, http.StatusBadRequest, "invalid_pagination", "limit must be between 1 and 200")
			return
		}
		limit = parsed
	}
	result, err := g.groupService.ListManagementAudit(ctx.Request.Context(), instance.Id, ctx.Param("groupJid"), limit, ctx.Query("cursor"))
	if err != nil {
		switch {
		case errors.Is(err, group_service.ErrInvalidManagementAuditCursor):
			httpapi.WriteError(ctx, http.StatusBadRequest, "invalid_cursor", "invalid group management audit cursor")
		case errors.Is(err, group_repository.ErrInvalidManagementCommand):
			httpapi.WriteError(ctx, http.StatusBadRequest, "invalid_filter", "invalid group management audit request")
		default:
			httpapi.WriteInternal(ctx, err)
		}
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": result.Items, "meta": gin.H{"nextCursor": result.NextCursor}})
}

// ListGroupMembers returns a bounded projected member page.
// @Summary List projected group members
// @Description Search current projected members without provider aliases or live WhatsApp queries. Target actions are advisory tri-state decisions and mutations revalidate them.
// @Tags Group
// @Produce json
// @Param groupJid path string true "Canonical WhatsApp Group JID"
// @Param q query string false "Case-insensitive display-name prefix" maxlength(128)
// @Param role query string false "Public member role" Enums(owner,superadmin,admin,member)
// @Param limit query int false "Page size (1-200)" minimum(1) maximum(200) default(50)
// @Param cursor query string false "Opaque cursor bound to instance, group, and filters"
// @Success 200 {object} apidocs.SuccessResponse{data=[]group_service.GroupMember} "success"
// @Failure 400 {object} apidocs.ErrorResponse "invalid_filter, invalid_pagination, or invalid_cursor"
// @Failure 404 {object} apidocs.ErrorResponse "group_not_found"
// @Failure 409 {object} apidocs.ErrorResponse "group_unavailable"
// @Failure 503 {object} apidocs.ErrorResponse "projection_not_ready"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Security ApiKeyAuth
// @Router /group/{groupJid}/members [get]
func (g *groupHandler) ListGroupMembers(ctx *gin.Context) {
	if !g.managementContract {
		httpapi.WriteError(ctx, http.StatusNotFound, "not_found", "group member projection is not enabled")
		return
	}
	instance, ok := ctx.MustGet("instance").(*instance_model.Instance)
	if !ok {
		httpapi.WriteInternal(ctx, nil)
		return
	}
	limit := 50
	if value := ctx.Query("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 200 {
			httpapi.WriteError(ctx, http.StatusBadRequest, "invalid_pagination", "limit must be between 1 and 200")
			return
		}
		limit = parsed
	}
	items, meta, err := g.groupService.ListManagementGroupMembers(ctx.Request.Context(), instance, ctx.Param("groupJid"), group_service.GroupMemberFilters{Query: ctx.Query("q"), Role: ctx.Query("role")}, limit, ctx.Query("cursor"))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httpapi.WriteError(ctx, http.StatusNotFound, "group_not_found", "group projection record not found")
			return
		}
		if errors.Is(err, group_service.ErrManagementGroupUnavailable) {
			httpapi.WriteError(ctx, http.StatusConflict, "group_unavailable", "group member projection is unavailable")
			return
		}
		writeGroupProjectionReadError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": items, "meta": meta})
}

// Search groups
// @Summary Search projected groups
// @Description Search normalized group summaries from the persisted instance projection without participants or live WhatsApp queries. The normalized contract is active when group_management_permissions is advertised.
// @Tags Group
// @Produce json
// @Param q query string false "Case-insensitive group JID or name prefix" maxlength(128)
// @Param type query string false "Group type" Enums(group,community,subgroup,unknown)
// @Param myRole query string false "Current instance role" Enums(owner,superadmin,admin,member,not_member,unknown)
// @Param sendMode query string false "Who may send" Enums(all_members,admins_only,unknown)
// @Param state query string false "Projected group state" Enums(active,suspended,dissolved,unavailable,unknown)
// @Param membershipState query string false "Current instance membership" Enums(joined,left,removed,unknown)
// @Param limit query int false "Page size (1-200)" minimum(1) maximum(200) default(50)
// @Param cursor query string false "Opaque cursor bound to the instance and all filters"
// @Success 200 {object} apidocs.SuccessResponse{data=[]group_service.GroupSummary} "success"
// @Failure 400 {object} apidocs.ErrorResponse "invalid_filter, invalid_pagination, or invalid_cursor"
// @Failure 503 {object} apidocs.ErrorResponse "Groups projection not ready"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Security ApiKeyAuth
// @Router /group/search [get]
func (g *groupHandler) SearchGroups(ctx *gin.Context) {
	instance, ok := ctx.MustGet("instance").(*instance_model.Instance)
	if !ok {
		httpapi.WriteInternal(ctx, nil)
		return
	}
	limit := 50
	if value := ctx.Query("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 200 {
			httpapi.WriteError(ctx, http.StatusBadRequest, "invalid_pagination", "limit must be between 1 and 200")
			return
		}
		limit = parsed
	}
	if g.managementContract {
		filters := group_service.GroupManagementFilters{
			Query: ctx.Query("q"), Type: ctx.Query("type"), MyRole: ctx.Query("myRole"), SendMode: ctx.Query("sendMode"),
			State: ctx.Query("state"), MembershipState: ctx.Query("membershipState"),
		}
		items, meta, err := g.groupService.SearchManagementGroups(ctx.Request.Context(), instance, filters, limit, ctx.Query("cursor"))
		if err != nil {
			writeGroupProjectionReadError(ctx, err)
			return
		}
		ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": items, "meta": meta})
		return
	}
	items, meta, err := g.groupService.SearchGroupsRead(ctx.Request.Context(), instance, ctx.Query("q"), limit, ctx.Query("cursor"))
	if err != nil {
		writeGroupProjectionReadError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": items, "meta": meta})
}

type groupHandler struct {
	groupService       group_service.GroupService
	managementContract bool
	photoAssets        bool
}

type Option func(*groupHandler)

func WithManagementContract(enabled bool) Option {
	return func(handler *groupHandler) { handler.managementContract = enabled }
}

func WithPhotoAssets(enabled bool) Option {
	return func(handler *groupHandler) { handler.photoAssets = enabled }
}

func (g *groupHandler) ManagementContractEnabled() bool { return g != nil && g.managementContract }
func (g *groupHandler) PhotoAssetsEnabled() bool        { return g != nil && g.photoAssets }

func decodeStrictManagementBody[T any](ctx *gin.Context, target *T) bool {
	ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, maxGroupManagementBodyBytes)
	decoder := json.NewDecoder(ctx.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		httpapi.WriteError(ctx, http.StatusBadRequest, "invalid_request", "invalid group management request")
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		httpapi.WriteError(ctx, http.StatusBadRequest, "invalid_request", "invalid group management request")
		return false
	}
	return true
}

func managementCommandMetadata(ctx *gin.Context) group_service.ManagementCommandMetadata {
	return group_service.ManagementCommandMetadata{
		ActorReference: ctx.GetHeader("apikey"), RequestID: httpapi.RequestID(ctx), IdempotencyKey: ctx.GetHeader("Idempotency-Key"),
	}
}

func writeManagementCommandError(ctx *gin.Context, err error) {
	if httpapi.WriteRateLimit(ctx, err) {
		return
	}
	switch {
	case errors.Is(err, group_service.ErrInvalidManagementFilter), errors.Is(err, group_repository.ErrInvalidManagementCommand):
		httpapi.WriteError(ctx, http.StatusBadRequest, "invalid_request", "invalid group management request")
	case errors.Is(err, group_service.ErrManagementPermissionDenied):
		httpapi.WriteError(ctx, http.StatusForbidden, "group_permission_denied", "group management permission denied")
	case errors.Is(err, group_service.ErrManagementPermissionUnknown):
		httpapi.WriteError(ctx, http.StatusConflict, "group_state_changed", "group management permission could not be established")
	case errors.Is(err, group_service.ErrManagementProviderNotReady):
		httpapi.WriteError(ctx, http.StatusServiceUnavailable, "provider_disconnected", "WhatsApp provider is disconnected")
	case errors.Is(err, group_service.ErrGroupPhotoAssetNotFound):
		httpapi.WriteError(ctx, http.StatusNotFound, "media_asset_not_found", "media asset was not found")
	case errors.Is(err, group_service.ErrGroupPhotoAssetNotReady):
		httpapi.WriteError(ctx, http.StatusConflict, "media_asset_not_ready", "media asset is not ready")
	case errors.Is(err, group_service.ErrGroupPhotoAssetInvalidType):
		httpapi.WriteError(ctx, http.StatusUnprocessableEntity, "media_asset_invalid_type", "media asset type is not supported for a Group photo")
	case errors.Is(err, group_service.ErrGroupPhotoAssetTooLarge):
		httpapi.WriteError(ctx, http.StatusRequestEntityTooLarge, "media_asset_too_large", "media asset exceeds Group photo limits")
	case errors.Is(err, group_service.ErrGroupPhotoAssetIntegrity):
		httpapi.WriteError(ctx, http.StatusConflict, "media_asset_integrity_failed", "media asset integrity could not be verified")
	case errors.Is(err, group_service.ErrGroupPhotoAssetStorage):
		httpapi.WriteError(ctx, http.StatusServiceUnavailable, "media_asset_storage_unavailable", "media asset storage is unavailable")
	case errors.Is(err, group_repository.ErrManagementIdempotencyConflict):
		httpapi.WriteError(ctx, http.StatusConflict, "idempotency_conflict", "idempotency key was reused with different input")
	case errors.Is(err, group_repository.ErrManagementCommandConflict):
		httpapi.WriteError(ctx, http.StatusConflict, "command_conflict", "group management command is already terminal")
	case errors.Is(err, gorm.ErrRecordNotFound):
		httpapi.WriteError(ctx, http.StatusNotFound, "group_not_found", "group projection record not found")
	case errors.Is(err, projection_service.ErrGroupsProjectionNotReady):
		httpapi.WriteError(ctx, http.StatusServiceUnavailable, "projection_not_ready", "groups projection is not ready")
	default:
		httpapi.WriteInternal(ctx, err)
	}
}

// List groups
// @Summary List groups
// @Description List normalized group summaries from the persisted instance projection. This is the unfiltered form of /group/search when group_management_permissions is advertised.
// @Tags Group
// @Produce json
// @Param limit query int false "Page size (1-200)" minimum(1) maximum(200) default(50)
// @Param cursor query string false "Opaque cursor bound to the instance"
// @Success 200 {object} apidocs.SuccessResponse{data=[]group_service.GroupSummary} "success"
// @Failure 400 {object} apidocs.ErrorResponse "invalid_pagination or invalid_cursor"
// @Failure 503 {object} apidocs.ErrorResponse "Groups projection not ready"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Security ApiKeyAuth
// @Router /group/list [get]
func (g *groupHandler) ListGroups(ctx *gin.Context) {
	getInstance := ctx.MustGet("instance")

	instance, ok := getInstance.(*instance_model.Instance)
	if !ok {
		httpapi.WriteInternal(ctx, nil)
		return
	}

	if g.managementContract {
		limit := 50
		if value := ctx.Query("limit"); value != "" {
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed < 1 || parsed > 200 {
				httpapi.WriteError(ctx, http.StatusBadRequest, "invalid_pagination", "limit must be between 1 and 200")
				return
			}
			limit = parsed
		}
		resp, meta, err := g.groupService.SearchManagementGroups(ctx.Request.Context(), instance, group_service.GroupManagementFilters{}, limit, ctx.Query("cursor"))
		if err != nil {
			writeGroupProjectionReadError(ctx, err)
			return
		}
		ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": resp, "meta": meta})
		return
	}
	resp, meta, err := g.groupService.ListGroupsRead(ctx.Request.Context(), instance)
	if err != nil {
		writeGroupProjectionReadError(ctx, err)
		return
	}

	response := gin.H{"message": "success", "data": resp}
	if meta != nil {
		response["meta"] = meta
	}
	ctx.JSON(http.StatusOK, response)
}

// Get group info
// @Summary Get group info
// @Description Get normalized projected group facts and advisory tri-state action decisions without members or live WhatsApp queries. Mutations must revalidate permissions.
// @Tags Group
// @Accept json
// @Produce json
// @Param message body group_service.GetGroupInfoStruct true "Group data"
// @Success 200 {object} apidocs.SuccessResponse{data=group_service.GroupDetail} "success"
// @Failure 400 {object} apidocs.ErrorResponse "invalid_filter"
// @Failure 404 {object} apidocs.ErrorResponse "Projected group not found"
// @Failure 503 {object} apidocs.ErrorResponse "Groups projection not ready"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Security ApiKeyAuth
// @Router /group/info [post]
func (g *groupHandler) GetGroupInfo(ctx *gin.Context) {
	getInstance := ctx.MustGet("instance")

	instance, ok := getInstance.(*instance_model.Instance)
	if !ok {
		httpapi.WriteInternal(ctx, nil)
		return
	}

	var data *group_service.GetGroupInfoStruct
	if g.managementContract {
		request := &group_service.GetGroupInfoStruct{}
		ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, maxGroupInfoBodyBytes)
		decoder := json.NewDecoder(ctx.Request.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(request); err != nil {
			httpapi.WriteError(ctx, http.StatusBadRequest, "invalid_filter", "invalid group info request")
			return
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			httpapi.WriteError(ctx, http.StatusBadRequest, "invalid_filter", "invalid group info request")
			return
		}
		data = request
	} else {
		if err := ctx.ShouldBindBodyWithJSON(&data); err != nil {
			ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}

	if data.GroupJID == "" {
		if g.managementContract {
			httpapi.WriteError(ctx, http.StatusBadRequest, "invalid_filter", "groupJid is required")
			return
		}
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "groupJID is required"})
		return
	}

	if g.managementContract {
		resp, meta, err := g.groupService.GetManagementGroupInfo(ctx.Request.Context(), data, instance)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				httpapi.WriteError(ctx, http.StatusNotFound, "group_not_found", "group projection record not found")
				return
			}
			writeGroupProjectionReadError(ctx, err)
			return
		}
		ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": resp, "meta": meta})
		return
	}
	resp, meta, err := g.groupService.GetGroupInfoRead(ctx.Request.Context(), data, instance)
	if err != nil {
		writeGroupProjectionReadError(ctx, err)
		return
	}

	response := gin.H{"message": "success", "data": resp}
	if meta != nil {
		response["meta"] = meta
	}
	ctx.JSON(http.StatusOK, response)
}

// Get group invite link
// @Summary Get group invite link
// @Description Read the cached group invite link from the instance-scoped Groups projection. This endpoint never calls WhatsApp when reset=false. Permission and cache availability are separate facts.
// @Tags Group
// @Accept json
// @Produce json
// @Param message body group_service.GetGroupInviteLinkStruct true "Group data"
// @Success 200 {object} apidocs.SuccessResponse "data is a cached-link string for reset=false or a CommandAcknowledgement for reset=true"
// @Failure 400 {object} apidocs.ErrorResponse "Error on validation"
// @Failure 403 {object} apidocs.GroupProjectionErrorResponse "group_permission_denied"
// @Failure 404 {object} apidocs.GroupInviteLinkNotFoundErrorResponse "group_not_found or group_invite_link_not_found"
// @Failure 409 {object} apidocs.GroupProjectionErrorResponse "group_state_changed"
// @Failure 503 {object} apidocs.ErrorResponse "Groups projection not ready"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Failure 429 {object} apidocs.RateLimitResponse "Information query rate limited; see Retry-After header"
// @Security ApiKeyAuth
// @Router /group/invitelink [post]
func (g *groupHandler) GetGroupInviteLink(ctx *gin.Context) {
	getInstance := ctx.MustGet("instance")

	instance, ok := getInstance.(*instance_model.Instance)
	if !ok {
		httpapi.WriteInternal(ctx, nil)
		return
	}

	var data *group_service.GetGroupInviteLinkStruct
	if g.managementContract {
		data = &group_service.GetGroupInviteLinkStruct{}
		if !decodeStrictManagementBody(ctx, data) {
			return
		}
	} else if err := ctx.ShouldBindBodyWithJSON(&data); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if data.GroupJID == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "groupJID is required"})
		return
	}
	if g.managementContract && data.Reset {
		acknowledgement, err := g.groupService.ExecuteResetInviteLink(ctx.Request.Context(), data, instance, managementCommandMetadata(ctx))
		if err != nil {
			writeManagementCommandError(ctx, err)
			return
		}
		ctx.JSON(http.StatusOK, gin.H{"message": "accepted", "data": acknowledgement})
		return
	}

	resp, meta, err := g.groupService.GetGroupInviteLink(ctx.Request.Context(), data, instance)
	if err != nil {
		if httpapi.WriteRateLimit(ctx, err) {
			return
		}
		if errors.Is(err, group_service.ErrGroupInviteLinkNotFound) {
			httpapi.WriteErrorWithDetails(ctx, http.StatusNotFound, "group_invite_link_not_found", "cached group invite link is not available", gin.H{
				"available": false,
				"meta":      meta,
			})
			return
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httpapi.WriteErrorWithDetails(ctx, http.StatusNotFound, "group_not_found", "group projection record not found", gin.H{"meta": meta})
			return
		}
		if errors.Is(err, group_service.ErrManagementPermissionDenied) {
			httpapi.WriteErrorWithDetails(ctx, http.StatusForbidden, "group_permission_denied", "group management permission denied", gin.H{"meta": meta})
			return
		}
		if errors.Is(err, group_service.ErrManagementPermissionUnknown) {
			httpapi.WriteErrorWithDetails(ctx, http.StatusConflict, "group_state_changed", "group management permission could not be established", gin.H{"meta": meta})
			return
		}
		writeGroupProjectionReadError(ctx, err)
		return
	}

	response := gin.H{"message": "success", "data": resp}
	if meta != nil {
		response["meta"] = meta
	}
	ctx.JSON(http.StatusOK, response)
}

func writeGroupProjectionReadError(ctx *gin.Context, err error) {
	switch {
	case errors.Is(err, group_service.ErrInvalidManagementCursor):
		httpapi.WriteError(ctx, http.StatusBadRequest, "invalid_cursor", "invalid group directory cursor")
	case errors.Is(err, group_service.ErrInvalidManagementFilter):
		httpapi.WriteError(ctx, http.StatusBadRequest, "invalid_filter", "invalid group directory filter")
	case errors.Is(err, projection_service.ErrInvalidGroupCursor):
		httpapi.WriteError(ctx, http.StatusBadRequest, "invalid_cursor", "invalid group search cursor")
	case errors.Is(err, projection_service.ErrInvalidGroupSearch):
		httpapi.WriteError(ctx, http.StatusBadRequest, "invalid_search", "invalid group search query")
	case errors.Is(err, projection_service.ErrGroupsProjectionNotReady):
		httpapi.WriteError(ctx, http.StatusServiceUnavailable, "projection_not_ready", "groups projection is not ready")
	case errors.Is(err, gorm.ErrRecordNotFound):
		httpapi.WriteError(ctx, http.StatusNotFound, "not_found", "group projection record not found")
	default:
		httpapi.WriteInternal(ctx, err)
	}
}

// Set group photo
// @Summary Set group photo
// @Description Set a Group photo from an instance-owned ready shared media asset. The normalized contract performs command-time permission revalidation and never accepts a URL or base64 image.
// @Tags Group
// @Accept json
// @Produce json
// @Param message body group_service.SetGroupPhotoAssetRequest true "Group photo asset command"
// @Success 200 {object} apidocs.SuccessResponse{data=group_service.CommandAcknowledgement} "accepted"
// @Failure 400 {object} apidocs.ErrorResponse "invalid_request"
// @Failure 403 {object} apidocs.ErrorResponse "group_permission_denied"
// @Failure 404 {object} apidocs.ErrorResponse "media_asset_not_found"
// @Failure 409 {object} apidocs.ErrorResponse "group_state_changed, media_asset_not_ready, media_asset_integrity_failed, or idempotency_conflict"
// @Failure 413 {object} apidocs.ErrorResponse "media_asset_too_large"
// @Failure 422 {object} apidocs.ErrorResponse "media_asset_invalid_type"
// @Failure 429 {object} apidocs.RateLimitResponse "Mutation rate limited; see Retry-After header"
// @Failure 503 {object} apidocs.ErrorResponse "projection_not_ready, provider_disconnected, or media_asset_storage_unavailable"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Security ApiKeyAuth
// @Router /group/photo [post]
func (g *groupHandler) SetGroupPhoto(ctx *gin.Context) {
	getInstance := ctx.MustGet("instance")

	instance, ok := getInstance.(*instance_model.Instance)
	if !ok {
		httpapi.WriteInternal(ctx, nil)
		return
	}
	if g.photoAssets {
		data := &group_service.SetGroupPhotoAssetRequest{}
		if !decodeStrictManagementBody(ctx, data) {
			return
		}
		acknowledgement, err := g.groupService.ExecuteSetGroupPhoto(ctx.Request.Context(), data, instance, managementCommandMetadata(ctx))
		if err != nil {
			writeManagementCommandError(ctx, err)
			return
		}
		ctx.JSON(http.StatusOK, gin.H{"message": "accepted", "data": acknowledgement})
		return
	}

	var data *group_service.SetGroupPhotoStruct
	err := ctx.ShouldBindBodyWithJSON(&data)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if data.GroupJID == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "groupJID is required"})
		return
	}

	if data.Image == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "image is required"})
		return
	}

	resp, err := g.groupService.SetGroupPhoto(data, instance)
	if err != nil {
		httpapi.WriteInternal(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": resp})
}

// Set group name
// @Summary Set group name
// @Description Set group name with command-time permission revalidation and a journaled acknowledgement when group management commands are enabled.
// @Tags Group
// @Accept json
// @Produce json
// @Param message body group_service.SetGroupNameStruct true "Group data"
// @Success 200 {object} apidocs.SuccessResponse{data=group_service.CommandAcknowledgement} "accepted"
// @Failure 400 {object} apidocs.ErrorResponse "Error on validation"
// @Failure 403 {object} apidocs.ErrorResponse "group_permission_denied"
// @Failure 409 {object} apidocs.ErrorResponse "group_state_changed or idempotency_conflict"
// @Failure 429 {object} apidocs.RateLimitResponse "Mutation rate limited; see Retry-After header"
// @Failure 503 {object} apidocs.ErrorResponse "projection_not_ready or provider_disconnected"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Security ApiKeyAuth
// @Router /group/name [post]
func (g *groupHandler) SetGroupName(ctx *gin.Context) {
	getInstance := ctx.MustGet("instance")

	instance, ok := getInstance.(*instance_model.Instance)
	if !ok {
		httpapi.WriteInternal(ctx, nil)
		return
	}

	var data *group_service.SetGroupNameStruct
	if g.managementContract {
		data = &group_service.SetGroupNameStruct{}
		if !decodeStrictManagementBody(ctx, data) {
			return
		}
	} else if err := ctx.ShouldBindBodyWithJSON(&data); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if data.GroupJID == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "groupJID is required"})
		return
	}

	if data.Name == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}

	if g.managementContract {
		acknowledgement, err := g.groupService.ExecuteSetGroupName(ctx.Request.Context(), data, instance, managementCommandMetadata(ctx))
		if err != nil {
			writeManagementCommandError(ctx, err)
			return
		}
		ctx.JSON(http.StatusOK, gin.H{"message": "accepted", "data": acknowledgement})
		return
	}

	err := g.groupService.SetGroupName(data, instance)
	if err != nil {
		httpapi.WriteInternal(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "success"})
}

// Set group description
// @Summary Set group description
// @Description Set or clear group description with command-time permission revalidation and a journaled acknowledgement.
// @Tags Group
// @Accept json
// @Produce json
// @Param message body group_service.SetGroupDescriptionStruct true "Group data"
// @Success 200 {object} apidocs.SuccessResponse{data=group_service.CommandAcknowledgement} "accepted"
// @Failure 400 {object} apidocs.ErrorResponse "Error on validation"
// @Failure 403 {object} apidocs.ErrorResponse "group_permission_denied"
// @Failure 409 {object} apidocs.ErrorResponse "group_state_changed or idempotency_conflict"
// @Failure 429 {object} apidocs.RateLimitResponse "Mutation rate limited; see Retry-After header"
// @Failure 503 {object} apidocs.ErrorResponse "projection_not_ready or provider_disconnected"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Security ApiKeyAuth
// @Router /group/description [post]
func (g *groupHandler) SetGroupDescription(ctx *gin.Context) {
	getInstance := ctx.MustGet("instance")

	instance, ok := getInstance.(*instance_model.Instance)
	if !ok {
		httpapi.WriteInternal(ctx, nil)
		return
	}

	var data *group_service.SetGroupDescriptionStruct
	if g.managementContract {
		data = &group_service.SetGroupDescriptionStruct{}
		if !decodeStrictManagementBody(ctx, data) {
			return
		}
	} else if err := ctx.ShouldBindBodyWithJSON(&data); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if data.GroupJID == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "groupJID is required"})
		return
	}

	// Description can be empty to clear the group description
	// No validation needed for Description field

	if g.managementContract {
		acknowledgement, err := g.groupService.ExecuteSetGroupDescription(ctx.Request.Context(), data, instance, managementCommandMetadata(ctx))
		if err != nil {
			writeManagementCommandError(ctx, err)
			return
		}
		ctx.JSON(http.StatusOK, gin.H{"message": "accepted", "data": acknowledgement})
		return
	}

	err := g.groupService.SetGroupDescription(data, instance)
	if err != nil {
		httpapi.WriteInternal(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "success"})
}

// Create group
// @Summary Create group
// @Description Create a group with bounded typed participant outcomes. The command is journaled before provider admission when group management commands are enabled.
// @Tags Group
// @Accept json
// @Produce json
// @Param message body group_service.CreateGroupStruct true "Group data"
// @Success 200 {object} apidocs.SuccessResponse{data=group_service.CreateGroupCommandResult} "accepted"
// @Failure 400 {object} apidocs.ErrorResponse "Error on validation"
// @Failure 409 {object} apidocs.ErrorResponse "idempotency_conflict"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Failure 429 {object} apidocs.RateLimitResponse "Mutation or information query rate limited; see Retry-After header"
// @Security ApiKeyAuth
// @Router /group/create [post]
func (g *groupHandler) CreateGroup(ctx *gin.Context) {
	getInstance := ctx.MustGet("instance")

	instance, ok := getInstance.(*instance_model.Instance)
	if !ok {
		httpapi.WriteInternal(ctx, nil)
		return
	}

	var data *group_service.CreateGroupStruct
	if g.managementContract {
		data = &group_service.CreateGroupStruct{}
		if !decodeStrictManagementBody(ctx, data) {
			return
		}
	} else if err := ctx.ShouldBindBodyWithJSON(&data); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if data.GroupName == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "groupName is required"})
		return
	}

	if len(data.Participants) < 1 {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "participants are required"})
		return
	}

	if g.managementContract {
		result, err := g.groupService.ExecuteCreateGroup(ctx.Request.Context(), data, instance, managementCommandMetadata(ctx))
		if err != nil {
			writeManagementCommandError(ctx, err)
			return
		}
		ctx.JSON(http.StatusOK, gin.H{"message": "accepted", "data": result})
		return
	}

	group, err := g.groupService.CreateGroup(ctx.Request.Context(), data, instance)
	if err != nil {
		if httpapi.WriteRateLimit(ctx, err) {
			return
		}
		httpapi.WriteInternal(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": group})
}

// Update participant
// @Summary Update participant
// @Description Execute a bounded participant command. Add accepts canonical user JIDs; remove/promote/demote accept opaque memberId values when group management commands are enabled.
// @Tags Group
// @Accept json
// @Produce json
// @Param message body group_service.ManagementParticipantRequest true "Group participant command"
// @Success 200 {object} apidocs.SuccessResponse{data=group_service.ParticipantCommandResult} "accepted"
// @Failure 400 {object} apidocs.ErrorResponse "Error on validation"
// @Failure 403 {object} apidocs.ErrorResponse "group_permission_denied"
// @Failure 409 {object} apidocs.ErrorResponse "group_state_changed or idempotency_conflict"
// @Failure 429 {object} apidocs.RateLimitResponse "Mutation rate limited; see Retry-After header"
// @Failure 503 {object} apidocs.ErrorResponse "projection_not_ready or provider_disconnected"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Security ApiKeyAuth
// @Router /group/participant [post]
func (g *groupHandler) UpdateParticipant(ctx *gin.Context) {
	getInstance := ctx.MustGet("instance")

	instance, ok := getInstance.(*instance_model.Instance)
	if !ok {
		httpapi.WriteInternal(ctx, nil)
		return
	}

	if g.managementContract {
		data := &group_service.ManagementParticipantRequest{}
		if !decodeStrictManagementBody(ctx, data) {
			return
		}
		result, err := g.groupService.ExecuteUpdateParticipant(ctx.Request.Context(), data, instance, managementCommandMetadata(ctx))
		if err != nil {
			writeManagementCommandError(ctx, err)
			return
		}
		ctx.JSON(http.StatusOK, gin.H{"message": "accepted", "data": result})
		return
	}

	var data *group_service.AddParticipantStruct
	err := ctx.ShouldBindBodyWithJSON(&data)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if data.GroupJID.String() == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "groupJid is required"})
		return
	}

	if data.Action == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "action is required"})
		return
	}

	if len(data.Participants) < 1 {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "participants are required"})
		return
	}

	err = g.groupService.UpdateParticipant(data, instance)
	if err != nil {
		httpapi.WriteInternal(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "success"})
}

// Get my groups
// @Summary Get my groups
// @Description Get my groups
// @Tags Group
// @Accept json
// @Produce json
// @Success 200 {object} apidocs.SuccessResponse{data=[]types.GroupInfo} "success"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Failure 429 {object} apidocs.RateLimitResponse "Information query rate limited; see Retry-After header"
// @Security ApiKeyAuth
// @Router /group/myall [get]
func (g *groupHandler) GetMyGroups(ctx *gin.Context) {
	getInstance := ctx.MustGet("instance")

	instance, ok := getInstance.(*instance_model.Instance)
	if !ok {
		httpapi.WriteInternal(ctx, nil)
		return
	}

	groups, err := g.groupService.GetMyGroups(ctx.Request.Context(), instance)
	if err != nil {
		if httpapi.WriteRateLimit(ctx, err) {
			return
		}
		httpapi.WriteInternal(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": groups})
}

// Join group link
// @Summary Join group link
// @Description Join with an invite code and return a typed public-safe outcome without claiming membership when post-command confirmation is unavailable.
// @Tags Group
// @Accept json
// @Produce json
// @Param message body group_service.JoinGroupStruct true "Group data"
// @Success 200 {object} apidocs.SuccessResponse{data=group_service.JoinGroupCommandResult} "accepted"
// @Failure 400 {object} apidocs.ErrorResponse "Error on validation"
// @Failure 409 {object} apidocs.ErrorResponse "idempotency_conflict"
// @Failure 429 {object} apidocs.RateLimitResponse "Mutation rate limited; see Retry-After header"
// @Failure 503 {object} apidocs.ErrorResponse "provider_disconnected"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Security ApiKeyAuth
// @Router /group/join [post]
func (g *groupHandler) JoinGroupLink(ctx *gin.Context) {
	getInstance := ctx.MustGet("instance")

	instance, ok := getInstance.(*instance_model.Instance)
	if !ok {
		httpapi.WriteInternal(ctx, nil)
		return
	}

	var data *group_service.JoinGroupStruct
	if g.managementContract {
		data = &group_service.JoinGroupStruct{}
		if !decodeStrictManagementBody(ctx, data) {
			return
		}
	} else if err := ctx.ShouldBindBodyWithJSON(&data); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if data.Code == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "code is required"})
		return
	}

	if g.managementContract {
		result, err := g.groupService.ExecuteJoinGroup(ctx.Request.Context(), data, instance, managementCommandMetadata(ctx))
		if err != nil {
			writeManagementCommandError(ctx, err)
			return
		}
		ctx.JSON(http.StatusOK, gin.H{"message": "accepted", "data": result})
		return
	}

	err := g.groupService.JoinGroupLink(data, instance)
	if err != nil {
		httpapi.WriteInternal(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "success"})
}

// Leave group
// @Summary Leave group
// @Description Leave a group after command-time membership revalidation; unknown provider outcomes are not retried.
// @Tags Group
// @Accept json
// @Produce json
// @Param message body group_service.ManagementLeaveGroupRequest true "Group data"
// @Success 200 {object} apidocs.SuccessResponse{data=group_service.CommandAcknowledgement} "accepted"
// @Failure 400 {object} apidocs.ErrorResponse "Error on validation"
// @Failure 403 {object} apidocs.ErrorResponse "group_permission_denied"
// @Failure 409 {object} apidocs.ErrorResponse "group_state_changed or idempotency_conflict"
// @Failure 429 {object} apidocs.RateLimitResponse "Mutation rate limited; see Retry-After header"
// @Failure 503 {object} apidocs.ErrorResponse "projection_not_ready or provider_disconnected"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Security ApiKeyAuth
// @Router /group/leave [post]
func (g *groupHandler) LeaveGroup(ctx *gin.Context) {
	getInstance := ctx.MustGet("instance")

	instance, ok := getInstance.(*instance_model.Instance)
	if !ok {
		httpapi.WriteInternal(ctx, nil)
		return
	}

	if g.managementContract {
		data := &group_service.ManagementLeaveGroupRequest{}
		if !decodeStrictManagementBody(ctx, data) {
			return
		}
		if data.GroupJID == "" {
			writeManagementCommandError(ctx, group_service.ErrInvalidManagementFilter)
			return
		}
		acknowledgement, err := g.groupService.ExecuteLeaveGroup(ctx.Request.Context(), data, instance, managementCommandMetadata(ctx))
		if err != nil {
			writeManagementCommandError(ctx, err)
			return
		}
		ctx.JSON(http.StatusOK, gin.H{"message": "accepted", "data": acknowledgement})
		return
	}

	var data *group_service.LeaveGroupStruct
	if err := ctx.ShouldBindBodyWithJSON(&data); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if data.GroupJID.String() == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "groupJid is required"})
		return
	}

	err := g.groupService.LeaveGroup(data, instance)
	if err != nil {
		httpapi.WriteInternal(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "success"})
}

// Update group settings
// @Summary Update group settings
// @Description Update group settings (announcement, not_announcement, locked, unlocked, approval_on, approval_off, admin_add, all_member_add)
// @Tags Group
// @Accept json
// @Produce json
// @Param message body group_service.UpdateGroupSettingsStruct true "Group data"
// @Success 200 {object} apidocs.SuccessResponse{data=group_service.CommandAcknowledgement} "accepted"
// @Failure 400 {object} apidocs.ErrorResponse "Error on validation"
// @Failure 403 {object} apidocs.ErrorResponse "group_permission_denied"
// @Failure 409 {object} apidocs.ErrorResponse "group_state_changed or idempotency_conflict"
// @Failure 429 {object} apidocs.RateLimitResponse "Mutation rate limited; see Retry-After header"
// @Failure 503 {object} apidocs.ErrorResponse "projection_not_ready or provider_disconnected"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Security ApiKeyAuth
// @Router /group/settings [post]
func (g *groupHandler) UpdateGroupSettings(ctx *gin.Context) {
	getInstance := ctx.MustGet("instance")

	instance, ok := getInstance.(*instance_model.Instance)
	if !ok {
		httpapi.WriteInternal(ctx, nil)
		return
	}

	var data *group_service.UpdateGroupSettingsStruct
	if g.managementContract {
		data = &group_service.UpdateGroupSettingsStruct{}
		if !decodeStrictManagementBody(ctx, data) {
			return
		}
	} else if err := ctx.ShouldBindBodyWithJSON(&data); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if data.GroupJID == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "groupJid is required"})
		return
	}

	if data.Action == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "action is required"})
		return
	}

	if g.managementContract {
		acknowledgement, err := g.groupService.ExecuteUpdateGroupSettings(ctx.Request.Context(), data, instance, managementCommandMetadata(ctx))
		if err != nil {
			writeManagementCommandError(ctx, err)
			return
		}
		ctx.JSON(http.StatusOK, gin.H{"message": "accepted", "data": acknowledgement})
		return
	}

	err := g.groupService.UpdateGroupSettings(data, instance)
	if err != nil {
		httpapi.WriteInternal(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "success"})
}

func NewGroupHandler(
	groupService group_service.GroupService,
	options ...Option,
) GroupHandler {
	handler := &groupHandler{
		groupService: groupService,
	}
	for _, option := range options {
		option(handler)
	}
	return handler
}
