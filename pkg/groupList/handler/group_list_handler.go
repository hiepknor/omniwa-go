package group_list_handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	group_list_repository "github.com/evolution-foundation/evolution-go/pkg/groupList/repository"
	group_list_service "github.com/evolution-foundation/evolution-go/pkg/groupList/service"
	"github.com/evolution-foundation/evolution-go/pkg/httpapi"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	"github.com/gin-gonic/gin"
)

const (
	defaultPageSize         = 50
	maxGroupListBodyBytes   = 4 << 20
	maxEligibilityBodyBytes = 64 << 10
)

type Handler interface {
	List(*gin.Context)
	Create(*gin.Context)
	Get(*gin.Context)
	Groups(*gin.Context)
	Update(*gin.Context)
	Delete(*gin.Context)
	Audit(*gin.Context)
	Eligibility(*gin.Context)
	AggregateEligibility(*gin.Context)
	EligibilityEnabled() bool
}

type managementService interface {
	Create(context.Context, string, string, group_list_service.CreateInput) (*group_list_repository.Summary, error)
	Update(context.Context, string, string, string, group_list_service.UpdateInput) (*group_list_repository.Summary, error)
	Get(context.Context, string, string) (*group_list_repository.Summary, error)
	List(context.Context, string, string, int, string) (*group_list_service.ListResult, error)
	Entries(context.Context, string, string, string, int, string) (*group_list_service.EntryList, error)
	Delete(context.Context, string, string, string) error
	Audit(context.Context, string, string, int, string) (*group_list_service.AuditList, error)
	Eligibility(context.Context, string, string, []string) (*group_list_service.EligibilityAssessment, error)
	AggregateEligibility(context.Context, string, string, string, *int64) (*group_list_service.AggregateAssessment, error)
}

type AuthorizationRequest struct {
	Source            string    `json:"source"`
	EvidenceReference string    `json:"evidenceReference"`
	AuthorizedAt      time.Time `json:"authorizedAt" format:"date-time"`
}

type CreateRequest struct {
	Name          string               `json:"name"`
	Description   string               `json:"description"`
	GroupJIDs     []string             `json:"groupJids"`
	Authorization AuthorizationRequest `json:"authorization"`
}

type UpdateRequest struct {
	Name            string               `json:"name"`
	Description     string               `json:"description"`
	GroupJIDs       []string             `json:"groupJids"`
	ExpectedVersion *int64               `json:"expectedVersion"`
	Authorization   AuthorizationRequest `json:"authorization"`
}

type EligibilityRequest struct {
	GroupJIDs []string `json:"groupJids"`
}

type handler struct {
	service              managementService
	eligibilityEndpoints bool
}

type Option func(*handler)

func WithEligibilityEndpoints(enabled bool) Option {
	return func(handler *handler) { handler.eligibilityEndpoints = enabled }
}

func New(service managementService, options ...Option) Handler {
	handler := &handler{service: service}
	for _, option := range options {
		option(handler)
	}
	return handler
}

func (h *handler) EligibilityEnabled() bool { return h != nil && h.eligibilityEndpoints }

// Eligibility evaluates an ordered, bounded advisory group batch from the persisted projection.
// @Summary Evaluate Group List eligibility
// @Tags Group Lists
// @Accept json
// @Produce json
// @Param request body EligibilityRequest true "Group JIDs"
// @Success 200 {object} apidocs.GroupListEligibilityResponse
// @Failure 400 {object} apidocs.ErrorResponse
// @Failure 401 {object} apidocs.ErrorResponse
// @Failure 500 {object} apidocs.ErrorResponse
// @Security ApiKeyAuth
// @Router /group-lists/eligibility [post]
func (h *handler) Eligibility(ctx *gin.Context) {
	instance, ok := authenticatedInstance(ctx)
	if !ok {
		return
	}
	var request EligibilityRequest
	if !decodeStrictWithLimit(ctx, &request, maxEligibilityBodyBytes) {
		return
	}
	assessment, err := h.service.Eligibility(ctx.Request.Context(), instance.Id, instance.Jid, request.GroupJIDs)
	if err != nil {
		writeError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": assessment.Results, "meta": assessment.Meta})
}

// AggregateEligibility evaluates the current entries of one Group List.
// @Summary Aggregate Group List eligibility
// @Tags Group Lists
// @Produce json
// @Param groupListId path string true "Group List UUID"
// @Param expectedVersion query int false "Expected current Group List version" minimum(1)
// @Success 200 {object} apidocs.GroupListEligibilityAggregateResponse
// @Failure 400 {object} apidocs.ErrorResponse
// @Failure 401 {object} apidocs.ErrorResponse
// @Failure 404 {object} apidocs.ErrorResponse
// @Failure 409 {object} apidocs.ErrorResponse
// @Failure 500 {object} apidocs.ErrorResponse
// @Security ApiKeyAuth
// @Router /group-lists/{groupListId}/eligibility [get]
func (h *handler) AggregateEligibility(ctx *gin.Context) {
	instance, ok := authenticatedInstance(ctx)
	if !ok {
		return
	}
	var expectedVersion *int64
	if raw, exists := ctx.GetQuery("expectedVersion"); exists {
		value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil || value < 1 {
			writeError(ctx, group_list_service.ErrInvalidInput)
			return
		}
		expectedVersion = &value
	}
	assessment, err := h.service.AggregateEligibility(ctx.Request.Context(), instance.Id, instance.Jid, ctx.Param("groupListId"), expectedVersion)
	if err != nil {
		writeError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": assessment.Data, "meta": assessment.Meta})
}

// List returns an instance-scoped Group List page.
// @Summary List Group Lists
// @Tags Group Lists
// @Produce json
// @Param search query string false "Normalized name prefix" maxlength(128)
// @Param limit query int false "Page size (1-100)" minimum(1) maximum(100) default(50)
// @Param cursor query string false "Opaque instance- and search-scoped cursor"
// @Success 200 {object} apidocs.GroupListListResponse
// @Failure 400 {object} apidocs.ErrorResponse
// @Failure 401 {object} apidocs.ErrorResponse
// @Failure 500 {object} apidocs.ErrorResponse
// @Security ApiKeyAuth
// @Router /group-lists [get]
func (h *handler) List(ctx *gin.Context) {
	instance, ok := authenticatedInstance(ctx)
	if !ok {
		return
	}
	limit, ok := pageSize(ctx)
	if !ok {
		return
	}
	result, err := h.service.List(ctx.Request.Context(), instance.Id, ctx.Query("search"), limit, ctx.Query("cursor"))
	if err != nil {
		writeError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": result.Items, "meta": gin.H{"nextCursor": result.NextCursor}})
}

// Create creates an authorization-backed Group List.
// @Summary Create Group List
// @Tags Group Lists
// @Accept json
// @Produce json
// @Param request body CreateRequest true "Group List"
// @Success 201 {object} apidocs.GroupListDetailResponse
// @Failure 400 {object} apidocs.ErrorResponse
// @Failure 401 {object} apidocs.ErrorResponse
// @Failure 409 {object} apidocs.GroupListEligibilityErrorResponse
// @Failure 503 {object} apidocs.GroupListEligibilityErrorResponse
// @Security ApiKeyAuth
// @Router /group-lists [post]
func (h *handler) Create(ctx *gin.Context) {
	instance, ok := authenticatedInstance(ctx)
	if !ok {
		return
	}
	var request CreateRequest
	if !decodeStrict(ctx, &request) {
		return
	}
	result, err := h.service.Create(ctx.Request.Context(), instance.Id, instance.Jid, group_list_service.CreateInput{
		Name: request.Name, Description: request.Description, GroupJIDs: request.GroupJIDs,
		Authorization: group_list_service.AuthorizationInput{
			Source: request.Authorization.Source, EvidenceReference: request.Authorization.EvidenceReference, AuthorizedAt: request.Authorization.AuthorizedAt,
		},
		ActorReference: ctx.GetHeader("apikey"),
	})
	if err != nil {
		writeError(ctx, err)
		return
	}
	ctx.JSON(http.StatusCreated, gin.H{"message": "success", "data": result})
}

// Get returns one instance-scoped Group List.
// @Summary Get Group List
// @Tags Group Lists
// @Produce json
// @Param groupListId path string true "Group List UUID"
// @Success 200 {object} apidocs.GroupListDetailResponse
// @Failure 400 {object} apidocs.ErrorResponse
// @Failure 404 {object} apidocs.ErrorResponse
// @Security ApiKeyAuth
// @Router /group-lists/{groupListId} [get]
func (h *handler) Get(ctx *gin.Context) {
	instance, ok := authenticatedInstance(ctx)
	if !ok {
		return
	}
	result, err := h.service.Get(ctx.Request.Context(), instance.Id, ctx.Param("groupListId"))
	if err != nil {
		writeError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": result})
}

// Groups returns Group List entries with backend-authoritative eligibility.
// @Summary List Group List groups
// @Tags Group Lists
// @Produce json
// @Param groupListId path string true "Group List UUID"
// @Param limit query int false "Page size (1-100)" minimum(1) maximum(100) default(50)
// @Param cursor query string false "Opaque list-scoped cursor"
// @Success 200 {object} apidocs.GroupListEntryListResponse
// @Failure 400 {object} apidocs.ErrorResponse
// @Failure 404 {object} apidocs.ErrorResponse
// @Failure 500 {object} apidocs.ErrorResponse
// @Security ApiKeyAuth
// @Router /group-lists/{groupListId}/groups [get]
func (h *handler) Groups(ctx *gin.Context) {
	instance, ok := authenticatedInstance(ctx)
	if !ok {
		return
	}
	limit, ok := pageSize(ctx)
	if !ok {
		return
	}
	result, err := h.service.Entries(ctx.Request.Context(), instance.Id, instance.Jid, ctx.Param("groupListId"), limit, ctx.Query("cursor"))
	if err != nil {
		writeError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": result.Items, "meta": gin.H{
		"source": result.Meta.Source, "syncStatus": result.Meta.SyncStatus, "lastSyncedAt": result.Meta.LastSyncedAt, "nextCursor": result.NextCursor,
	}})
}

// Update fully replaces mutable Group List fields and entries.
// @Summary Update Group List
// @Tags Group Lists
// @Accept json
// @Produce json
// @Param groupListId path string true "Group List UUID"
// @Param request body UpdateRequest true "Replacement Group List and expected version"
// @Success 200 {object} apidocs.GroupListDetailResponse
// @Failure 400 {object} apidocs.ErrorResponse
// @Failure 404 {object} apidocs.ErrorResponse
// @Failure 409 {object} apidocs.GroupListEligibilityErrorResponse
// @Failure 503 {object} apidocs.GroupListEligibilityErrorResponse
// @Security ApiKeyAuth
// @Router /group-lists/{groupListId} [put]
func (h *handler) Update(ctx *gin.Context) {
	instance, ok := authenticatedInstance(ctx)
	if !ok {
		return
	}
	var request UpdateRequest
	if !decodeStrict(ctx, &request) {
		return
	}
	if request.ExpectedVersion == nil {
		writeError(ctx, group_list_service.ErrInvalidInput)
		return
	}
	result, err := h.service.Update(ctx.Request.Context(), instance.Id, instance.Jid, ctx.Param("groupListId"), group_list_service.UpdateInput{
		Name: request.Name, Description: request.Description, GroupJIDs: request.GroupJIDs, ExpectedVersion: *request.ExpectedVersion,
		Authorization: group_list_service.AuthorizationInput{
			Source: request.Authorization.Source, EvidenceReference: request.Authorization.EvidenceReference, AuthorizedAt: request.Authorization.AuthorizedAt,
		},
		ActorReference: ctx.GetHeader("apikey"),
	})
	if err != nil {
		writeError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": result})
}

// Delete soft-deletes one Group List without altering campaign snapshots.
// @Summary Delete Group List
// @Tags Group Lists
// @Produce json
// @Param groupListId path string true "Group List UUID"
// @Success 200 {object} apidocs.SuccessResponse
// @Failure 400 {object} apidocs.ErrorResponse
// @Failure 404 {object} apidocs.ErrorResponse
// @Security ApiKeyAuth
// @Router /group-lists/{groupListId} [delete]
func (h *handler) Delete(ctx *gin.Context) {
	instance, ok := authenticatedInstance(ctx)
	if !ok {
		return
	}
	if err := h.service.Delete(ctx.Request.Context(), instance.Id, ctx.Param("groupListId"), ctx.GetHeader("apikey")); err != nil {
		writeError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "success"})
}

// Audit returns immutable Group List lifecycle history.
// @Summary Get Group List audit history
// @Tags Group Lists
// @Produce json
// @Param groupListId path string true "Group List UUID"
// @Param limit query int false "Page size (1-100)" minimum(1) maximum(100) default(50)
// @Param cursor query string false "Opaque list-scoped cursor"
// @Success 200 {object} apidocs.GroupListAuditListResponse
// @Failure 400 {object} apidocs.ErrorResponse
// @Failure 404 {object} apidocs.ErrorResponse
// @Security ApiKeyAuth
// @Router /group-lists/{groupListId}/audit [get]
func (h *handler) Audit(ctx *gin.Context) {
	instance, ok := authenticatedInstance(ctx)
	if !ok {
		return
	}
	limit, ok := pageSize(ctx)
	if !ok {
		return
	}
	result, err := h.service.Audit(ctx.Request.Context(), instance.Id, ctx.Param("groupListId"), limit, ctx.Query("cursor"))
	if err != nil {
		writeError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": result.Items, "meta": gin.H{"nextCursor": result.NextCursor}})
}

func authenticatedInstance(ctx *gin.Context) (*instance_model.Instance, bool) {
	value, exists := ctx.Get("instance")
	instance, ok := value.(*instance_model.Instance)
	if !exists || !ok || instance == nil || instance.Id == "" {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "not authorized"})
		return nil, false
	}
	return instance, true
}

func decodeStrict(ctx *gin.Context, target any) bool {
	return decodeStrictWithLimit(ctx, target, maxGroupListBodyBytes)
}

func decodeStrictWithLimit(ctx *gin.Context, target any, limit int64) bool {
	ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, limit)
	decoder := json.NewDecoder(ctx.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(ctx, group_list_service.ErrInvalidInput)
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(ctx, group_list_service.ErrInvalidInput)
		return false
	}
	return true
}

func pageSize(ctx *gin.Context) (int, bool) {
	value := strings.TrimSpace(ctx.Query("limit"))
	if value == "" {
		return defaultPageSize, true
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit < 1 || limit > 100 {
		httpapi.WriteError(ctx, http.StatusBadRequest, "invalid_pagination", "limit must be between 1 and 100")
		return 0, false
	}
	return limit, true
}

func writeError(ctx *gin.Context, err error) {
	var issues *group_list_service.EligibilityIssuesError
	if errors.As(err, &issues) {
		if errors.Is(err, group_list_service.ErrProjectionNotReady) {
			httpapi.WriteErrorWithDetails(ctx, http.StatusServiceUnavailable, "projection_not_ready", "groups projection is not ready", issues.Details)
		} else {
			httpapi.WriteErrorWithDetails(ctx, http.StatusConflict, "group_list_group_unavailable", "group list contains an unavailable group", issues.Details)
		}
		return
	}
	switch {
	case errors.Is(err, group_list_repository.ErrNotFound):
		httpapi.WriteError(ctx, http.StatusNotFound, "group_list_not_found", "group list not found")
	case errors.Is(err, group_list_repository.ErrNameConflict):
		httpapi.WriteError(ctx, http.StatusConflict, "group_list_name_conflict", "group list name already exists")
	case errors.Is(err, group_list_repository.ErrVersionConflict):
		httpapi.WriteError(ctx, http.StatusConflict, "group_list_version_conflict", "group list version changed; refresh before retrying")
	case errors.Is(err, group_list_service.ErrEmpty):
		httpapi.WriteError(ctx, http.StatusBadRequest, "group_list_empty", "group list requires at least one group")
	case errors.Is(err, group_list_service.ErrInvalidGroup):
		httpapi.WriteError(ctx, http.StatusBadRequest, "group_list_invalid_group", "group list contains an invalid or duplicate group")
	case errors.Is(err, group_list_service.ErrGroupUnavailable):
		httpapi.WriteError(ctx, http.StatusConflict, "group_list_group_unavailable", "group list contains an unavailable group")
	case errors.Is(err, group_list_service.ErrProjectionNotReady):
		httpapi.WriteError(ctx, http.StatusServiceUnavailable, "projection_not_ready", "groups projection is not ready")
	case errors.Is(err, group_list_service.ErrInvalidCursor):
		httpapi.WriteError(ctx, http.StatusBadRequest, "invalid_cursor", "invalid cursor")
	case errors.Is(err, group_list_service.ErrInvalidInput):
		httpapi.WriteError(ctx, http.StatusBadRequest, "invalid_group_list_input", "invalid group list input")
	default:
		httpapi.WriteInternal(ctx, err)
	}
}
