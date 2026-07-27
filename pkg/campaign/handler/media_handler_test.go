package campaign_handler

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	campaign_model "github.com/evolution-foundation/evolution-go/pkg/campaign/model"
	campaign_service "github.com/evolution-foundation/evolution-go/pkg/campaign/service"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type mediaServiceFake struct {
	input      campaign_service.MediaUploadInput
	instanceID string
	assetID    string
	asset      *campaign_model.MediaAsset
	err        error
	content    []byte
}

func (f *mediaServiceFake) Upload(_ context.Context, input campaign_service.MediaUploadInput) (*campaign_model.MediaAsset, error) {
	f.input = input
	f.content, _ = io.ReadAll(input.Reader)
	return f.asset, f.err
}
func (f *mediaServiceFake) Get(_ context.Context, instanceID, assetID string) (*campaign_model.MediaAsset, error) {
	f.instanceID, f.assetID = instanceID, assetID
	return f.asset, f.err
}
func (f *mediaServiceFake) Delete(_ context.Context, instanceID, assetID string) error {
	f.instanceID, f.assetID = instanceID, assetID
	return f.err
}

func TestMediaUploadUsesAuthenticatedInstanceAndDoesNotExposeStorageIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	instanceID, assetID := uuid.NewString(), uuid.NewString()
	service := &mediaServiceFake{asset: &campaign_model.MediaAsset{
		ID: assetID, InstanceID: instanceID, ObjectKey: "campaign-media/private/object", Status: campaign_model.MediaAssetStatusReady,
	}}
	handler := NewMediaHandler(service, 1024)
	request := multipartRequest(t, []byte("image-content"), "device-photo.jpg")
	request.Header.Set("Idempotency-Key", "upload-once")
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Request = request
	ctx.Set("instance", &instance_model.Instance{Id: instanceID})
	handler.Upload(ctx)
	if response.Code != http.StatusCreated || service.input.InstanceID != instanceID || service.input.IdempotencyKey != "upload-once" || string(service.content) != "image-content" {
		t.Fatalf("status=%d input=%+v content=%q body=%s", response.Code, service.input, service.content, response.Body.String())
	}
	if strings.Contains(response.Body.String(), instanceID) || strings.Contains(response.Body.String(), "campaign-media/private/object") {
		t.Fatalf("private storage identity leaked: %s", response.Body.String())
	}
}

func TestMediaHandlerMapsBoundedPublicErrors(t *testing.T) {
	instanceID := uuid.NewString()
	for _, test := range []struct {
		err    error
		status int
		code   string
	}{
		{campaign_service.ErrMediaTooLarge, http.StatusRequestEntityTooLarge, "campaign_media_too_large"},
		{campaign_service.ErrUnsupportedMediaType, http.StatusUnsupportedMediaType, "unsupported_campaign_media_type"},
		{campaign_service.ErrMediaStorageUnavailable, http.StatusServiceUnavailable, "campaign_media_storage_unavailable"},
	} {
		service := &mediaServiceFake{err: test.err}
		handler := NewMediaHandler(service, 1024)
		request := multipartRequest(t, []byte("x"), "image.jpg")
		response := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(response)
		ctx.Request = request
		ctx.Set("instance", &instance_model.Instance{Id: instanceID})
		handler.Upload(ctx)
		if response.Code != test.status || !strings.Contains(response.Body.String(), `"code":"`+test.code+`"`) {
			t.Fatalf("err=%v status=%d body=%s", test.err, response.Code, response.Body.String())
		}
	}
}

func TestMediaHandlerRejectsOversizedMultipartBeforeService(t *testing.T) {
	service := &mediaServiceFake{asset: &campaign_model.MediaAsset{ID: uuid.NewString()}}
	handler := NewMediaHandler(service, 4)
	request := multipartRequest(t, []byte("oversized"), "image.jpg")
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Request = request
	ctx.Set("instance", &instance_model.Instance{Id: uuid.NewString()})
	handler.Upload(ctx)
	if response.Code != http.StatusRequestEntityTooLarge || service.input.InstanceID != "" {
		t.Fatalf("status=%d called=%t body=%s", response.Code, service.input.InstanceID != "", response.Body.String())
	}
}

func TestMediaGetScopesLookupAndRequiresInstance(t *testing.T) {
	instanceID, assetID := uuid.NewString(), uuid.NewString()
	service := &mediaServiceFake{asset: &campaign_model.MediaAsset{ID: assetID}}
	handler := NewMediaHandler(service, 1024)
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/campaign-media/"+assetID, nil)
	ctx.Params = gin.Params{{Key: "mediaId", Value: assetID}}
	ctx.Set("instance", &instance_model.Instance{Id: instanceID})
	handler.Get(ctx)
	if response.Code != http.StatusOK || service.instanceID != instanceID || service.assetID != assetID {
		t.Fatalf("status=%d instance=%q asset=%q", response.Code, service.instanceID, service.assetID)
	}

	service.err = errors.New("must not be called")
	response = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(response)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/campaign-media/"+assetID, nil)
	handler.Get(ctx)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func multipartRequest(t *testing.T, content []byte, filename string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/campaign-media", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}
