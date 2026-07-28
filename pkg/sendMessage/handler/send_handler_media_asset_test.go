package send_handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	media_service "github.com/evolution-foundation/evolution-go/pkg/media/service"
	send_service "github.com/evolution-foundation/evolution-go/pkg/sendMessage/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.mau.fi/whatsmeow/types"
)

type assetImageSenderFake struct {
	err   error
	calls int
	data  *send_service.MediaStruct
}

func (f *assetImageSenderFake) Send(_ context.Context, data *send_service.MediaStruct, _ *instance_model.Instance) (*send_service.MessageSendStruct, error) {
	f.calls++
	f.data = data
	if f.err != nil {
		return nil, f.err
	}
	acknowledgedAt := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	return &send_service.MessageSendStruct{Info: types.MessageInfo{ID: "message-id"}, AcknowledgementID: "message-id", AcknowledgedAt: &acknowledgedAt}, nil
}

func TestSendMediaRoutesMediaAssetJSONToSingleAttemptSender(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sender := &assetImageSenderFake{}
	handler := &sendHandler{assetImageSender: sender}
	assetID := uuid.NewString()
	body := `{"number":"120363000001@g.us","type":"image","mediaAssetId":"` + assetID + `","caption":"hello"}`
	request := httptest.NewRequest(http.MethodPost, "/send/media", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = request
	ctx.Set("instance", &instance_model.Instance{Id: uuid.NewString()})

	handler.SendMedia(ctx)
	if recorder.Code != http.StatusOK || sender.calls != 1 || sender.data == nil || sender.data.MediaAssetID != assetID ||
		!strings.Contains(recorder.Body.String(), `"messageId":"message-id"`) || !strings.Contains(recorder.Body.String(), `"timestamp":"2026-07-29T12:00:00Z"`) {
		t.Fatalf("status=%d calls=%d data=%+v body=%s", recorder.Code, sender.calls, sender.data, recorder.Body.String())
	}
}

func TestSendMediaRejectsAmbiguousAssetAndURL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sender := &assetImageSenderFake{}
	handler := &sendHandler{assetImageSender: sender}
	body := `{"number":"120363000001@g.us","type":"image","mediaAssetId":"` + uuid.NewString() + `","url":"https://example.com/a.png"}`
	request := httptest.NewRequest(http.MethodPost, "/send/media", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = request
	ctx.Set("instance", &instance_model.Instance{Id: uuid.NewString()})

	handler.SendMedia(ctx)
	if recorder.Code != http.StatusBadRequest || sender.calls != 0 || !strings.Contains(recorder.Body.String(), "invalid_media_asset") {
		t.Fatalf("status=%d calls=%d body=%s", recorder.Code, sender.calls, recorder.Body.String())
	}
}

func TestSendMediaMapsUnknownAssetOutcomeWithoutRetryAdvice(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sender := &assetImageSenderFake{err: &send_service.ProviderSendError{Cause: errors.New("ack lost")}}
	handler := &sendHandler{assetImageSender: sender}
	body := `{"number":"120363000001@g.us","type":"image","mediaAssetId":"` + uuid.NewString() + `"}`
	request := httptest.NewRequest(http.MethodPost, "/send/media", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = request
	ctx.Set("instance", &instance_model.Instance{Id: uuid.NewString()})

	handler.SendMedia(ctx)
	if recorder.Code != http.StatusBadGateway || !strings.Contains(recorder.Body.String(), "unknown_send_outcome") || !strings.Contains(recorder.Body.String(), "do not retry automatically") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestWriteAssetSendErrorMapsIntegrityFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	writeAssetSendError(ctx, media_service.ErrMediaAssetIntegrity)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestWriteAssetSendErrorMapsLifecycleFailures(t *testing.T) {
	for _, test := range []struct {
		err    error
		status int
		code   string
	}{
		{err: media_service.ErrMediaAssetFailed, status: http.StatusConflict, code: "media_asset_failed"},
		{err: media_service.ErrMediaAssetExpired, status: http.StatusGone, code: "media_asset_expired"},
		{err: media_service.ErrMediaAssetDeleted, status: http.StatusGone, code: "media_asset_deleted"},
		{err: media_service.ErrMediaAssetInstance, status: http.StatusForbidden, code: "media_asset_instance_mismatch"},
	} {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		writeAssetSendError(ctx, test.err)
		if recorder.Code != test.status || !strings.Contains(recorder.Body.String(), test.code) {
			t.Fatalf("error=%v status=%d body=%s", test.err, recorder.Code, recorder.Body.String())
		}
	}
}
