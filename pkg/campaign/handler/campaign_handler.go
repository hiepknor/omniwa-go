package campaign_handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	campaign_model "github.com/evolution-foundation/evolution-go/pkg/campaign/model"
	campaign_repository "github.com/evolution-foundation/evolution-go/pkg/campaign/repository"
	campaign_service "github.com/evolution-foundation/evolution-go/pkg/campaign/service"
	"github.com/evolution-foundation/evolution-go/pkg/httpapi"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	defaultPageSize        = 50
	maxCampaignRequestSize = 8 << 20
)

type CampaignHandler interface {
	Create(*gin.Context)
	List(*gin.Context)
	Get(*gin.Context)
	Recipients(*gin.Context)
	Audit(*gin.Context)
	Schedule(*gin.Context)
	Start(*gin.Context)
	Pause(*gin.Context)
	Resume(*gin.Context)
	Abort(*gin.Context)
}

type managementService interface {
	Create(context.Context, string, campaign_service.CreateCampaignInput) (*campaign_service.CampaignDetail, error)
	Get(context.Context, string, string) (*campaign_service.CampaignDetail, error)
	List(context.Context, string, campaign_model.CampaignStatus, int, string) (*campaign_service.CampaignList, error)
	Recipients(context.Context, string, string, int, string) (*campaign_service.RecipientList, error)
	Audit(context.Context, string, string, int, string) (*campaign_service.AuditList, error)
	Transition(context.Context, string, string, campaign_model.CampaignStatus, *time.Time, campaign_repository.Actor) (*campaign_service.CampaignDetail, error)
}

type campaignHandler struct{ service managementService }

type CreateCampaignRequest struct {
	Name       string                     `json:"name" binding:"required"`
	Text       string                     `json:"text" binding:"required"`
	Recipients []CampaignRecipientConsent `json:"recipients" binding:"required"`
}

type CampaignRecipientConsent struct {
	JID                    string    `json:"jid" binding:"required"`
	OptInSource            string    `json:"optInSource" binding:"required"`
	OptInEvidenceReference string    `json:"optInEvidenceReference" binding:"required"`
	OptedInAt              time.Time `json:"optedInAt" binding:"required"`
}

type ScheduleCampaignRequest struct {
	StartsAt time.Time `json:"startsAt" binding:"required"`
}

func NewCampaignHandler(service managementService) CampaignHandler {
	return &campaignHandler{service: service}
}

// Create creates a consent-backed campaign draft.
// @Summary Create campaign draft
// @Description Creates a text campaign draft. Every recipient requires instance-scoped opt-in evidence; raw evidence references are hashed before persistence.
// @Tags Campaigns
// @Accept json
// @Produce json
// @Param request body CreateCampaignRequest true "Campaign draft"
// @Success 201 {object} apidocs.CampaignDetailResponse
// @Failure 400 {object} apidocs.CampaignErrorResponse
// @Failure 401 {object} apidocs.ErrorResponse
// @Failure 500 {object} apidocs.ErrorResponse
// @Security ApiKeyAuth
// @Router /campaigns [post]
func (h *campaignHandler) Create(ctx *gin.Context) {
	instance, ok := authenticatedInstance(ctx)
	if !ok {
		return
	}
	ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, maxCampaignRequestSize)
	var request CreateCampaignRequest
	if err := ctx.ShouldBindJSON(&request); err != nil {
		writeCampaignError(ctx, campaign_repository.ErrInvalidCampaignInput)
		return
	}
	recipients := make([]campaign_repository.RecipientConsent, len(request.Recipients))
	for index, recipient := range request.Recipients {
		recipients[index] = campaign_repository.RecipientConsent{
			JID: recipient.JID, OptInSource: recipient.OptInSource, EvidenceReference: recipient.OptInEvidenceReference, OptedInAt: recipient.OptedInAt,
		}
	}
	detail, err := h.service.Create(ctx.Request.Context(), instance.Id, campaign_service.CreateCampaignInput{
		Name: request.Name, TextBody: request.Text, Recipients: recipients, Actor: instanceActor(ctx),
	})
	if err != nil {
		writeCampaignError(ctx, err)
		return
	}
	ctx.JSON(http.StatusCreated, gin.H{"message": "success", "data": detail})
}

// List returns an instance-scoped campaign page.
// @Summary List campaigns
// @Tags Campaigns
// @Produce json
// @Param status query string false "Campaign status"
// @Param limit query int false "Page size (1-100)" default(50)
// @Param cursor query string false "Opaque cursor"
// @Success 200 {object} apidocs.CampaignListResponse
// @Failure 400 {object} apidocs.CampaignErrorResponse
// @Failure 401 {object} apidocs.ErrorResponse
// @Failure 500 {object} apidocs.ErrorResponse
// @Security ApiKeyAuth
// @Router /campaigns [get]
func (h *campaignHandler) List(ctx *gin.Context) {
	instance, ok := authenticatedInstance(ctx)
	if !ok {
		return
	}
	limit, ok := pageSize(ctx)
	if !ok {
		return
	}
	result, err := h.service.List(ctx.Request.Context(), instance.Id, campaign_model.CampaignStatus(ctx.Query("status")), limit, ctx.Query("cursor"))
	if err != nil {
		writeCampaignError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": result.Items, "meta": gin.H{"nextCursor": result.NextCursor}})
}

// Get returns campaign state and recipient status counts.
// @Summary Get campaign
// @Tags Campaigns
// @Produce json
// @Param campaignId path string true "Campaign ID"
// @Success 200 {object} apidocs.CampaignDetailResponse
// @Failure 404 {object} apidocs.CampaignErrorResponse
// @Security ApiKeyAuth
// @Router /campaigns/{campaignId} [get]
func (h *campaignHandler) Get(ctx *gin.Context) {
	h.readDetail(ctx, func(instanceID, campaignID string) (any, error) {
		return h.service.Get(ctx.Request.Context(), instanceID, campaignID)
	})
}

// Recipients returns stable per-recipient campaign state.
// @Summary List campaign recipients
// @Tags Campaigns
// @Produce json
// @Param campaignId path string true "Campaign ID"
// @Param limit query int false "Page size (1-100)" default(50)
// @Param cursor query string false "Opaque cursor"
// @Success 200 {object} apidocs.CampaignRecipientListResponse
// @Failure 400 {object} apidocs.CampaignErrorResponse
// @Failure 404 {object} apidocs.CampaignErrorResponse
// @Security ApiKeyAuth
// @Router /campaigns/{campaignId}/recipients [get]
func (h *campaignHandler) Recipients(ctx *gin.Context) {
	instance, ok := authenticatedInstance(ctx)
	if !ok {
		return
	}
	limit, ok := pageSize(ctx)
	if !ok {
		return
	}
	if !validCampaignID(ctx) {
		return
	}
	result, err := h.service.Recipients(ctx.Request.Context(), instance.Id, ctx.Param("campaignId"), limit, ctx.Query("cursor"))
	if err != nil {
		writeCampaignError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": result.Items, "meta": gin.H{"nextCursor": result.NextCursor}})
}

// Audit returns durable campaign and recipient lifecycle events.
// @Summary List campaign audit history
// @Tags Campaigns
// @Produce json
// @Param campaignId path string true "Campaign ID"
// @Param limit query int false "Page size (1-100)" default(50)
// @Param cursor query string false "Opaque cursor"
// @Success 200 {object} apidocs.CampaignAuditListResponse
// @Failure 400 {object} apidocs.CampaignErrorResponse
// @Failure 404 {object} apidocs.CampaignErrorResponse
// @Security ApiKeyAuth
// @Router /campaigns/{campaignId}/audit [get]
func (h *campaignHandler) Audit(ctx *gin.Context) {
	instance, ok := authenticatedInstance(ctx)
	if !ok {
		return
	}
	limit, ok := pageSize(ctx)
	if !ok {
		return
	}
	if !validCampaignID(ctx) {
		return
	}
	result, err := h.service.Audit(ctx.Request.Context(), instance.Id, ctx.Param("campaignId"), limit, ctx.Query("cursor"))
	if err != nil {
		writeCampaignError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": result.Items, "meta": gin.H{"nextCursor": result.NextCursor}})
}

// Schedule schedules a draft for future or immediate worker eligibility.
// @Summary Schedule campaign
// @Tags Campaigns
// @Accept json
// @Produce json
// @Param campaignId path string true "Campaign ID"
// @Param request body ScheduleCampaignRequest true "Schedule"
// @Success 200 {object} apidocs.CampaignDetailResponse
// @Failure 400 {object} apidocs.CampaignErrorResponse
// @Failure 409 {object} apidocs.CampaignErrorResponse
// @Security ApiKeyAuth
// @Router /campaigns/{campaignId}/schedule [post]
func (h *campaignHandler) Schedule(ctx *gin.Context) {
	if _, ok := authenticatedInstance(ctx); !ok {
		return
	}
	if !validCampaignID(ctx) {
		return
	}
	ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, maxCampaignRequestSize)
	var request ScheduleCampaignRequest
	if err := ctx.ShouldBindJSON(&request); err != nil {
		writeCampaignError(ctx, campaign_repository.ErrInvalidCampaignInput)
		return
	}
	h.transition(ctx, campaign_model.CampaignStatusScheduled, &request.StartsAt)
}

// Start starts a scheduled campaign.
// @Summary Start campaign
// @Tags Campaigns
// @Produce json
// @Param campaignId path string true "Campaign ID"
// @Success 200 {object} apidocs.CampaignDetailResponse
// @Failure 409 {object} apidocs.CampaignErrorResponse
// @Security ApiKeyAuth
// @Router /campaigns/{campaignId}/start [post]
func (h *campaignHandler) Start(ctx *gin.Context) {
	h.transition(ctx, campaign_model.CampaignStatusRunning, nil)
}

// Pause prevents new recipient claims; already leased work may finish.
// @Summary Pause campaign
// @Tags Campaigns
// @Produce json
// @Param campaignId path string true "Campaign ID"
// @Success 200 {object} apidocs.CampaignDetailResponse
// @Failure 409 {object} apidocs.CampaignErrorResponse
// @Security ApiKeyAuth
// @Router /campaigns/{campaignId}/pause [post]
func (h *campaignHandler) Pause(ctx *gin.Context) {
	h.transition(ctx, campaign_model.CampaignStatusPaused, nil)
}

// Resume resumes a paused campaign.
// @Summary Resume campaign
// @Tags Campaigns
// @Produce json
// @Param campaignId path string true "Campaign ID"
// @Success 200 {object} apidocs.CampaignDetailResponse
// @Failure 409 {object} apidocs.CampaignErrorResponse
// @Security ApiKeyAuth
// @Router /campaigns/{campaignId}/resume [post]
func (h *campaignHandler) Resume(ctx *gin.Context) {
	h.transition(ctx, campaign_model.CampaignStatusRunning, nil)
}

// Abort prevents new claims and terminally aborts pending recipients.
// @Summary Abort campaign
// @Tags Campaigns
// @Produce json
// @Param campaignId path string true "Campaign ID"
// @Success 200 {object} apidocs.CampaignDetailResponse
// @Failure 409 {object} apidocs.CampaignErrorResponse
// @Security ApiKeyAuth
// @Router /campaigns/{campaignId}/abort [post]
func (h *campaignHandler) Abort(ctx *gin.Context) {
	h.transition(ctx, campaign_model.CampaignStatusAborted, nil)
}

func (h *campaignHandler) transition(ctx *gin.Context, target campaign_model.CampaignStatus, startsAt *time.Time) {
	instance, ok := authenticatedInstance(ctx)
	if !ok {
		return
	}
	if !validCampaignID(ctx) {
		return
	}
	detail, err := h.service.Transition(ctx.Request.Context(), instance.Id, ctx.Param("campaignId"), target, startsAt, instanceActor(ctx))
	if err != nil {
		writeCampaignError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": detail})
}

func (h *campaignHandler) readDetail(ctx *gin.Context, read func(string, string) (any, error)) {
	instance, ok := authenticatedInstance(ctx)
	if !ok {
		return
	}
	if !validCampaignID(ctx) {
		return
	}
	data, err := read(instance.Id, ctx.Param("campaignId"))
	if err != nil {
		writeCampaignError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": data})
}

func validCampaignID(ctx *gin.Context) bool {
	if uuid.Validate(ctx.Param("campaignId")) != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid campaign input", "code": "invalid_campaign_input"})
		return false
	}
	return true
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

func instanceActor(ctx *gin.Context) campaign_repository.Actor {
	return campaign_repository.Actor{Type: "instance", Reference: ctx.GetHeader("apikey")}
}

func pageSize(ctx *gin.Context) (int, bool) {
	value := strings.TrimSpace(ctx.Query("limit"))
	if value == "" {
		return defaultPageSize, true
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit < 1 || limit > 100 {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid pagination", "code": "invalid_pagination"})
		return 0, false
	}
	return limit, true
}

func writeCampaignError(ctx *gin.Context, err error) {
	switch {
	case errors.Is(err, campaign_service.ErrInvalidCampaignCursor):
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid cursor", "code": "invalid_cursor"})
	case errors.Is(err, campaign_repository.ErrInvalidCampaignInput):
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid campaign input", "code": "invalid_campaign_input"})
	case errors.Is(err, gorm.ErrRecordNotFound):
		ctx.JSON(http.StatusNotFound, gin.H{"error": "campaign not found", "code": "campaign_not_found"})
	case errors.Is(err, campaign_repository.ErrInvalidCampaignTransition), errors.Is(err, campaign_repository.ErrCampaignConflict), errors.Is(err, campaign_repository.ErrCampaignHasPendingWork):
		ctx.JSON(http.StatusConflict, gin.H{"error": "campaign state conflict", "code": "campaign_state_conflict"})
	default:
		httpapi.WriteInternal(ctx, err)
	}
}
