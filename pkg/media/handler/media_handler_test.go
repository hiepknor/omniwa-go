package media_handler

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	media_model "github.com/evolution-foundation/evolution-go/pkg/media/model"
	media_service "github.com/evolution-foundation/evolution-go/pkg/media/service"
	"github.com/gin-gonic/gin"
)

type assetServiceFake struct {
	asset  *media_model.Asset
	upload media_service.AssetUploadInput
	delete string
}

func (f *assetServiceFake) Upload(_ context.Context, input media_service.AssetUploadInput) (*media_model.Asset, error) {
	f.upload = input
	return f.asset, nil
}
func (f *assetServiceFake) Get(context.Context, string, string) (*media_model.Asset, error) {
	return f.asset, nil
}
func (f *assetServiceFake) Delete(_ context.Context, _, assetID string) error {
	f.delete = assetID
	return nil
}

func TestUploadPassesAuthenticatedScopeAndIdempotencyWithoutFilename(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &assetServiceFake{asset: &media_model.Asset{ID: "asset-id", Status: media_model.AssetStatusReady}}
	handler := New(service, 1024)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	file, err := writer.CreateFormFile("file", "private-name.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("image")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/media-assets", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Idempotency-Key", "request-1")
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = request
	ctx.Set("instance", &instance_model.Instance{Id: "instance-id"})

	handler.Upload(ctx)
	if recorder.Code != http.StatusCreated || service.upload.InstanceID != "instance-id" || service.upload.IdempotencyKey != "request-1" || strings.Contains(recorder.Body.String(), "private-name") {
		t.Fatalf("status=%d input=%+v body=%s", recorder.Code, service.upload, recorder.Body.String())
	}
}

func TestGetDoesNotExposeObjectKeyOrInstance(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &assetServiceFake{asset: &media_model.Asset{
		ID: "asset-id", InstanceID: "instance-id", Status: media_model.AssetStatusReady,
		Canonical: &media_model.AssetVariant{Kind: media_model.VariantCanonical, ObjectKey: "media-assets/secret", MIMEType: "image/png"},
	}}
	handler := New(service, 1024)
	request := httptest.NewRequest(http.MethodGet, "/media-assets/asset-id", nil)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = request
	ctx.Params = gin.Params{{Key: "mediaId", Value: "asset-id"}}
	ctx.Set("instance", &instance_model.Instance{Id: "instance-id"})

	handler.Get(ctx)
	if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), "media-assets/secret") || strings.Contains(recorder.Body.String(), "instance-id") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestMediaAssetsRequireAuthenticatedInstance(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := New(&assetServiceFake{}, 1024)
	request := httptest.NewRequest(http.MethodGet, "/media-assets/asset-id", nil)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = request

	handler.Get(ctx)
	if recorder.Code != http.StatusUnauthorized || !strings.Contains(recorder.Body.String(), "unauthorized") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
