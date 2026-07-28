package media_handler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

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
	GetMetadata(context.Context, string, string) (*media_model.Asset, error)
	Delete(context.Context, string, string) error
	OpenContent(context.Context, string, string, int64, int64) (*media_service.AssetContent, error)
}

type Handler interface {
	Upload(*gin.Context)
	Get(*gin.Context)
	Delete(*gin.Context)
	Content(*gin.Context)
	DeviceUploadsEnabled() bool
	ContentEnabled() bool
}

type handler struct {
	service       assetService
	maxBytes      int64
	deviceUploads bool
	content       bool
}

type Option func(*handler)

func WithDeviceUploads(enabled bool) Option {
	return func(handler *handler) { handler.deviceUploads = enabled }
}
func WithContent(enabled bool) Option { return func(handler *handler) { handler.content = enabled } }

func New(service assetService, maxBytes int64, options ...Option) Handler {
	handler := &handler{service: service, maxBytes: maxBytes, deviceUploads: true}
	for _, option := range options {
		if option != nil {
			option(handler)
		}
	}
	return handler
}

func (h *handler) DeviceUploadsEnabled() bool { return h != nil && h.deviceUploads }
func (h *handler) ContentEnabled() bool       { return h != nil && h.content }

// Upload stores one normalized private JPEG or PNG asset.
// @Summary Upload shared image asset
// @Tags Media Assets
// @Accept multipart/form-data
// @Produce json
// @Description Accepts one genuine JPEG or PNG up to the server-configured MEDIA_ASSET_MAX_BYTES limit (default 8388608 bytes). The authenticated instance owns the resulting private asset.
// @Param file formData file true "JPEG or PNG image; default maximum 8388608 bytes"
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
	if !h.deviceUploads {
		httpapi.WriteError(ctx, http.StatusNotImplemented, "media_asset_upload_disabled", "device media upload is disabled")
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
// @Failure 403 {object} apidocs.ErrorResponse
// @Failure 404 {object} apidocs.ErrorResponse
// @Security ApiKeyAuth
// @Router /media-assets/{mediaId} [get]
func (h *handler) Get(ctx *gin.Context) {
	instance, ok := authenticatedInstance(ctx)
	if !ok {
		return
	}
	asset, err := h.service.GetMetadata(ctx.Request.Context(), instance.Id, ctx.Param("mediaId"))
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
// @Failure 503 {object} apidocs.ErrorResponse
// @Security ApiKeyAuth
// @Router /media-assets/{mediaId} [delete]
func (h *handler) Delete(ctx *gin.Context) {
	instance, ok := authenticatedInstance(ctx)
	if !ok {
		return
	}
	if !h.DeviceUploadsEnabled() {
		httpapi.WriteError(ctx, http.StatusNotImplemented, "media_asset_delete_disabled", "device media deletion is disabled")
		return
	}
	if err := h.service.Delete(ctx.Request.Context(), instance.Id, ctx.Param("mediaId")); err != nil {
		writeError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "success"})
}

// Content streams authenticated canonical image bytes with optional single-range support.
// @Summary Stream shared image asset content
// @Tags Media Assets
// @Produce image/jpeg,image/png
// @Param mediaId path string true "Media asset UUID"
// @Param Range header string false "Single bytes range"
// @Success 200 {file} binary
// @Success 206 {file} binary
// @Failure 403 {object} apidocs.ErrorResponse
// @Failure 404 {object} apidocs.ErrorResponse
// @Failure 409 {object} apidocs.ErrorResponse
// @Failure 410 {object} apidocs.ErrorResponse
// @Failure 416 {object} apidocs.ErrorResponse
// @Failure 422 {object} apidocs.ErrorResponse
// @Failure 503 {object} apidocs.ErrorResponse
// @Security ApiKeyAuth
// @Router /media-assets/{mediaId}/content [get]
func (h *handler) Content(ctx *gin.Context) {
	instance, ok := authenticatedInstance(ctx)
	if !ok {
		return
	}
	if !h.ContentEnabled() {
		httpapi.WriteError(ctx, http.StatusNotImplemented, "media_asset_content_disabled", "media content streaming is disabled")
		return
	}
	asset, err := h.service.GetMetadata(ctx.Request.Context(), instance.Id, ctx.Param("mediaId"))
	if err != nil {
		writeError(ctx, err)
		return
	}
	if err := media_service.AssetAvailability(asset, time.Now().UTC()); err != nil {
		writeError(ctx, err)
		return
	}
	if asset.Canonical == nil {
		writeError(ctx, media_service.ErrMediaAssetIntegrity)
		return
	}
	offset, length, partial, err := parseRange(ctx.GetHeader("Range"), asset.Canonical.SizeBytes)
	if err != nil {
		ctx.Header("Content-Range", fmt.Sprintf("bytes */%d", asset.Canonical.SizeBytes))
		httpapi.WriteError(ctx, http.StatusRequestedRangeNotSatisfiable, "invalid_media_range", "requested media byte range is not satisfiable")
		return
	}
	content, err := h.service.OpenContent(ctx.Request.Context(), instance.Id, asset.ID, offset, length)
	if err != nil {
		writeError(ctx, err)
		return
	}
	defer content.Reader.Close()
	ctx.Header("Accept-Ranges", "bytes")
	ctx.Header("Cache-Control", "private, max-age=31536000, immutable")
	ctx.Header("ETag", `"`+content.SHA256+`"`)
	ctx.Header("X-Content-Type-Options", "nosniff")
	ctx.Header("Content-Length", strconv.FormatInt(content.Length, 10))
	ctx.Header("Content-Type", content.MIMEType)
	status := http.StatusOK
	if partial {
		status = http.StatusPartialContent
		ctx.Header("Content-Range", fmt.Sprintf("bytes %d-%d/%d", content.Offset, content.Offset+content.Length-1, content.Total))
	}
	ctx.Status(status)
	if _, err := io.CopyN(ctx.Writer, content.Reader, content.Length); err != nil {
		_ = ctx.Error(err)
	}
}

func parseRange(value string, total int64) (offset, length int64, partial bool, err error) {
	if total < 1 {
		return 0, 0, false, errors.New("media content is empty")
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, total, false, nil
	}
	if !strings.HasPrefix(value, "bytes=") || strings.Contains(value, ",") {
		return 0, 0, false, errors.New("only one bytes range is supported")
	}
	parts := strings.Split(strings.TrimPrefix(value, "bytes="), "-")
	if len(parts) != 2 {
		return 0, 0, false, errors.New("invalid bytes range")
	}
	if parts[0] == "" {
		suffix, parseErr := strconv.ParseInt(parts[1], 10, 64)
		if parseErr != nil || suffix < 1 {
			return 0, 0, false, errors.New("invalid suffix range")
		}
		if suffix > total {
			suffix = total
		}
		return total - suffix, suffix, true, nil
	}
	start, parseErr := strconv.ParseInt(parts[0], 10, 64)
	if parseErr != nil || start < 0 || start >= total {
		return 0, 0, false, errors.New("invalid range start")
	}
	end := total - 1
	if parts[1] != "" {
		end, parseErr = strconv.ParseInt(parts[1], 10, 64)
		if parseErr != nil || end < start {
			return 0, 0, false, errors.New("invalid range end")
		}
		if end >= total {
			end = total - 1
		}
	}
	return start, end - start + 1, true, nil
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
	case errors.Is(err, media_service.ErrMediaAssetInstance):
		httpapi.WriteError(ctx, http.StatusForbidden, "media_asset_instance_mismatch", "media asset does not belong to the authenticated instance")
	case errors.Is(err, media_repository.ErrAssetNotFound):
		httpapi.WriteError(ctx, http.StatusNotFound, "media_asset_not_found", "media asset not found")
	case errors.Is(err, media_service.ErrMediaAssetFailed):
		httpapi.WriteError(ctx, http.StatusConflict, "media_asset_failed", "media asset processing failed")
	case errors.Is(err, media_service.ErrMediaAssetExpired):
		httpapi.WriteError(ctx, http.StatusGone, "media_asset_expired", "media asset expired")
	case errors.Is(err, media_service.ErrMediaAssetDeleted):
		httpapi.WriteError(ctx, http.StatusGone, "media_asset_deleted", "media asset was deleted")
	case errors.Is(err, media_repository.ErrAssetConflict):
		httpapi.WriteError(ctx, http.StatusConflict, "media_asset_conflict", "media asset state conflict")
	case errors.Is(err, media_service.ErrMediaAssetNotReady):
		httpapi.WriteError(ctx, http.StatusConflict, "media_asset_not_ready", "media asset is not ready")
	case errors.Is(err, media_service.ErrMediaAssetIntegrity):
		httpapi.WriteError(ctx, http.StatusUnprocessableEntity, "media_asset_integrity_failed", "media asset integrity check failed")
	default:
		httpapi.WriteInternal(ctx, err)
	}
}
