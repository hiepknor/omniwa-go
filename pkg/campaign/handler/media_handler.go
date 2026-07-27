package campaign_handler

import (
	"context"
	"errors"
	"net/http"

	campaign_model "github.com/evolution-foundation/evolution-go/pkg/campaign/model"
	campaign_repository "github.com/evolution-foundation/evolution-go/pkg/campaign/repository"
	campaign_service "github.com/evolution-foundation/evolution-go/pkg/campaign/service"
	"github.com/evolution-foundation/evolution-go/pkg/httpapi"
	"github.com/gin-gonic/gin"
)

const multipartEnvelopeAllowance int64 = 1 << 20

type MediaHandler interface {
	Upload(*gin.Context)
	Get(*gin.Context)
	Delete(*gin.Context)
}

type mediaService interface {
	Upload(context.Context, campaign_service.MediaUploadInput) (*campaign_model.MediaAsset, error)
	Get(context.Context, string, string) (*campaign_model.MediaAsset, error)
	Delete(context.Context, string, string) error
}

type mediaHandler struct {
	service  mediaService
	maxBytes int64
}

func NewMediaHandler(service mediaService, maxBytes int64) MediaHandler {
	return &mediaHandler{service: service, maxBytes: maxBytes}
}

// Upload stores an immutable, instance-scoped campaign image.
// @Summary Upload campaign image
// @Description Uploads and normalizes one private JPEG or PNG image. The response never exposes an object-store key or URL.
// @Tags Campaign Media
// @Accept multipart/form-data
// @Produce json
// @Param file formData file true "JPEG or PNG image"
// @Param Idempotency-Key header string false "Instance-scoped upload idempotency key"
// @Success 201 {object} apidocs.CampaignMediaAssetResponse
// @Failure 400 {object} apidocs.CampaignErrorResponse
// @Failure 401 {object} apidocs.ErrorResponse
// @Failure 413 {object} apidocs.CampaignErrorResponse
// @Failure 415 {object} apidocs.CampaignErrorResponse
// @Failure 503 {object} apidocs.CampaignErrorResponse
// @Security ApiKeyAuth
// @Router /campaign-media [post]
func (h *mediaHandler) Upload(ctx *gin.Context) {
	instance, ok := authenticatedInstance(ctx)
	if !ok {
		return
	}
	if h == nil || h.service == nil || h.maxBytes < 1 {
		httpapi.WriteInternal(ctx, errors.New("campaign media handler is unavailable"))
		return
	}
	ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, h.maxBytes+multipartEnvelopeAllowance)
	fileHeader, err := ctx.FormFile("file")
	if err != nil || fileHeader.Size < 1 {
		writeMediaError(ctx, campaign_service.ErrInvalidMediaUpload)
		return
	}
	if fileHeader.Size > h.maxBytes {
		writeMediaError(ctx, campaign_service.ErrMediaTooLarge)
		return
	}
	file, err := fileHeader.Open()
	if err != nil {
		writeMediaError(ctx, campaign_service.ErrInvalidMediaUpload)
		return
	}
	defer file.Close()
	asset, err := h.service.Upload(ctx.Request.Context(), campaign_service.MediaUploadInput{
		InstanceID: instance.Id, IdempotencyKey: ctx.GetHeader("Idempotency-Key"), Reader: file,
	})
	if err != nil {
		writeMediaError(ctx, err)
		return
	}
	ctx.JSON(http.StatusCreated, gin.H{"message": "success", "data": asset})
}

// Get returns campaign image metadata.
// @Summary Get campaign image metadata
// @Tags Campaign Media
// @Produce json
// @Param mediaId path string true "Media asset ID"
// @Success 200 {object} apidocs.CampaignMediaAssetResponse
// @Failure 400 {object} apidocs.CampaignErrorResponse
// @Failure 404 {object} apidocs.CampaignErrorResponse
// @Security ApiKeyAuth
// @Router /campaign-media/{mediaId} [get]
func (h *mediaHandler) Get(ctx *gin.Context) {
	instance, ok := authenticatedInstance(ctx)
	if !ok {
		return
	}
	asset, err := h.service.Get(ctx.Request.Context(), instance.Id, ctx.Param("mediaId"))
	if err != nil {
		writeMediaError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": asset})
}

// Delete removes an unbound campaign image.
// @Summary Delete campaign image
// @Tags Campaign Media
// @Produce json
// @Param mediaId path string true "Media asset ID"
// @Success 200 {object} apidocs.SuccessResponse
// @Failure 400 {object} apidocs.CampaignErrorResponse
// @Failure 404 {object} apidocs.CampaignErrorResponse
// @Failure 409 {object} apidocs.CampaignErrorResponse
// @Failure 503 {object} apidocs.CampaignErrorResponse
// @Security ApiKeyAuth
// @Router /campaign-media/{mediaId} [delete]
func (h *mediaHandler) Delete(ctx *gin.Context) {
	instance, ok := authenticatedInstance(ctx)
	if !ok {
		return
	}
	if err := h.service.Delete(ctx.Request.Context(), instance.Id, ctx.Param("mediaId")); err != nil {
		writeMediaError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "success"})
}

func writeMediaError(ctx *gin.Context, err error) {
	switch {
	case errors.Is(err, campaign_service.ErrInvalidMediaUpload):
		httpapi.WriteError(ctx, http.StatusBadRequest, "invalid_campaign_media", "invalid campaign media")
	case errors.Is(err, campaign_service.ErrMediaTooLarge):
		httpapi.WriteError(ctx, http.StatusRequestEntityTooLarge, "campaign_media_too_large", "campaign media is too large")
	case errors.Is(err, campaign_service.ErrUnsupportedMediaType):
		httpapi.WriteError(ctx, http.StatusUnsupportedMediaType, "unsupported_campaign_media_type", "unsupported campaign media type")
	case errors.Is(err, campaign_service.ErrInvalidMediaDimension):
		httpapi.WriteError(ctx, http.StatusUnprocessableEntity, "invalid_campaign_media_dimensions", "invalid campaign media dimensions")
	case errors.Is(err, campaign_service.ErrMediaStorageUnavailable):
		httpapi.WriteError(ctx, http.StatusServiceUnavailable, "campaign_media_storage_unavailable", "campaign media storage is unavailable")
	case errors.Is(err, campaign_repository.ErrMediaAssetNotFound):
		httpapi.WriteError(ctx, http.StatusNotFound, "campaign_media_not_found", "campaign media not found")
	case errors.Is(err, campaign_repository.ErrMediaAssetConflict):
		httpapi.WriteError(ctx, http.StatusConflict, "campaign_media_conflict", "campaign media state conflict")
	default:
		httpapi.WriteInternal(ctx, err)
	}
}
