package media_handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/evolution-foundation/evolution-go/pkg/httpapi"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	media_model "github.com/evolution-foundation/evolution-go/pkg/media/model"
	media_repository "github.com/evolution-foundation/evolution-go/pkg/media/repository"
	media_service "github.com/evolution-foundation/evolution-go/pkg/media/service"
	"github.com/gin-gonic/gin"
)

const multipartEnvelopeAllowance int64 = 1 << 20

type assetService interface {
	Upload(context.Context, media_service.AssetUploadInput) (*media_model.Asset, error)
	Get(context.Context, string, string) (*media_model.Asset, error)
	Delete(context.Context, string, string) error
}

type Handler interface {
	Upload(*gin.Context)
	Get(*gin.Context)
	Delete(*gin.Context)
}

type handler struct {
	service  assetService
	maxBytes int64
}

func New(service assetService, maxBytes int64) Handler {
	return &handler{service: service, maxBytes: maxBytes}
}

// Upload stores one normalized private JPEG or PNG asset.
// @Summary Upload shared image asset
// @Tags Media Assets
// @Accept multipart/form-data
// @Produce json
// @Param file formData file true "JPEG or PNG image"
// @Param Idempotency-Key header string false "Instance-scoped upload idempotency key"
// @Success 201 {object} apidocs.MediaAssetResponse
// @Failure 400 {object} apidocs.ErrorResponse
// @Failure 413 {object} apidocs.ErrorResponse
// @Failure 415 {object} apidocs.ErrorResponse
// @Failure 422 {object} apidocs.ErrorResponse
// @Failure 503 {object} apidocs.ErrorResponse
// @Security ApiKeyAuth
// @Router /media-assets [post]
func (h *handler) Upload(ctx *gin.Context) {
	instance, ok := authenticatedInstance(ctx)
	if !ok {
		return
	}
	if h == nil || h.service == nil || h.maxBytes < 1 {
		httpapi.WriteInternal(ctx, errors.New("media asset handler is unavailable"))
		return
	}
	ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, h.maxBytes+multipartEnvelopeAllowance)
	fileHeader, err := ctx.FormFile("file")
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(ctx, media_service.ErrMediaAssetTooLarge)
		return
	}
	if err != nil || fileHeader.Size < 1 {
		writeError(ctx, media_service.ErrInvalidMediaAsset)
		return
	}
	if fileHeader.Size > h.maxBytes {
		writeError(ctx, media_service.ErrMediaAssetTooLarge)
		return
	}
	file, err := fileHeader.Open()
	if err != nil {
		writeError(ctx, media_service.ErrInvalidMediaAsset)
		return
	}
	defer file.Close()
	asset, err := h.service.Upload(ctx.Request.Context(), media_service.AssetUploadInput{
		InstanceID: instance.Id, IdempotencyKey: ctx.GetHeader("Idempotency-Key"), Reader: file,
	})
	if err != nil {
		writeError(ctx, err)
		return
	}
	ctx.JSON(http.StatusCreated, gin.H{"message": "success", "data": asset})
}

// Get returns safe shared image metadata.
// @Summary Get shared image asset metadata
// @Tags Media Assets
// @Produce json
// @Param mediaId path string true "Media asset UUID"
// @Success 200 {object} apidocs.MediaAssetResponse
// @Failure 404 {object} apidocs.ErrorResponse
// @Security ApiKeyAuth
// @Router /media-assets/{mediaId} [get]
func (h *handler) Get(ctx *gin.Context) {
	instance, ok := authenticatedInstance(ctx)
	if !ok {
		return
	}
	asset, err := h.service.Get(ctx.Request.Context(), instance.Id, ctx.Param("mediaId"))
	if err != nil {
		writeError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": asset})
}

// Delete removes an unreferenced shared image asset.
// @Summary Delete shared image asset
// @Tags Media Assets
// @Produce json
// @Param mediaId path string true "Media asset UUID"
// @Success 200 {object} apidocs.SuccessResponse
// @Failure 404 {object} apidocs.ErrorResponse
// @Failure 409 {object} apidocs.ErrorResponse
// @Security ApiKeyAuth
// @Router /media-assets/{mediaId} [delete]
func (h *handler) Delete(ctx *gin.Context) {
	instance, ok := authenticatedInstance(ctx)
	if !ok {
		return
	}
	if err := h.service.Delete(ctx.Request.Context(), instance.Id, ctx.Param("mediaId")); err != nil {
		writeError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "success"})
}

func authenticatedInstance(ctx *gin.Context) (*instance_model.Instance, bool) {
	value, exists := ctx.Get("instance")
	instance, ok := value.(*instance_model.Instance)
	if !exists || !ok || instance == nil {
		httpapi.WriteError(ctx, http.StatusUnauthorized, "unauthorized", "authentication required")
		return nil, false
	}
	return instance, true
}

func writeError(ctx *gin.Context, err error) {
	switch {
	case errors.Is(err, media_service.ErrInvalidMediaAsset):
		httpapi.WriteError(ctx, http.StatusBadRequest, "invalid_media_asset", "invalid media asset")
	case errors.Is(err, media_service.ErrMediaAssetTooLarge):
		httpapi.WriteError(ctx, http.StatusRequestEntityTooLarge, "media_asset_too_large", "media asset is too large")
	case errors.Is(err, media_service.ErrUnsupportedMediaAsset):
		httpapi.WriteError(ctx, http.StatusUnsupportedMediaType, "unsupported_media_asset_type", "unsupported media asset type")
	case errors.Is(err, media_service.ErrInvalidMediaDimensions):
		httpapi.WriteError(ctx, http.StatusUnprocessableEntity, "invalid_media_asset_dimensions", "invalid media asset dimensions")
	case errors.Is(err, media_service.ErrMediaAssetStorage):
		httpapi.WriteError(ctx, http.StatusServiceUnavailable, "media_asset_storage_unavailable", "media asset storage is unavailable")
	case errors.Is(err, media_repository.ErrAssetNotFound):
		httpapi.WriteError(ctx, http.StatusNotFound, "media_asset_not_found", "media asset not found")
	case errors.Is(err, media_repository.ErrAssetConflict):
		httpapi.WriteError(ctx, http.StatusConflict, "media_asset_conflict", "media asset state conflict")
	default:
		httpapi.WriteInternal(ctx, err)
	}
}
