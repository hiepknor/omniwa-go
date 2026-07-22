package instance_handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type metadataReaderStub struct {
	instances []*instance_model.Instance
	instance  *instance_model.Instance
	err       error
	infoID    string
}

func (r *metadataReaderStub) GetAll() ([]*instance_model.Instance, error) {
	return r.instances, r.err
}

func (r *metadataReaderStub) Info(instanceID string) (*instance_model.Instance, error) {
	r.infoID = instanceID
	return r.instance, r.err
}

func TestInstanceMetadataHandlersNeverReturnBearerCredentials(t *testing.T) {
	gin.SetMode(gin.TestMode)
	instanceID := "00000000-0000-0000-0000-000000000123"
	record := &instance_model.Instance{Id: instanceID, Name: "primary", Token: "bearer-secret", TokenGeneration: 3, Proxy: "proxy-secret", Qrcode: "pairing-secret"}
	reader := &metadataReaderStub{instances: []*instance_model.Instance{record}, instance: record}
	handler := &instanceHandler{metadataReader: reader}
	router := gin.New()
	router.GET("/instance/metadata", handler.AllMetadata)
	router.GET("/instance/metadata/:instanceId", handler.Metadata)

	for _, path := range []string{"/instance/metadata", "/instance/metadata/" + instanceID} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		body := response.Body.String()
		if response.Code != http.StatusOK || !strings.Contains(body, `"credentialVersion":3`) ||
			strings.Contains(body, "bearer-secret") || strings.Contains(body, "proxy-secret") || strings.Contains(body, "pairing-secret") ||
			strings.Contains(body, `"token"`) || strings.Contains(body, `"proxy"`) || strings.Contains(body, `"qrcode"`) {
			t.Fatalf("path=%s status=%d body=%s", path, response.Code, body)
		}
	}
	if reader.infoID != instanceID {
		t.Fatalf("detail instance ID = %q", reader.infoID)
	}
}

func TestInstanceMetadataDetailMapsPublicErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name   string
		path   string
		err    error
		status int
		code   string
	}{
		{name: "invalid identity", path: "/instance/metadata/not-a-uuid", status: http.StatusBadRequest, code: "invalid_instance_id"},
		{name: "not found", path: "/instance/metadata/00000000-0000-0000-0000-000000000123", err: gorm.ErrRecordNotFound, status: http.StatusNotFound, code: "instance_not_found"},
		{name: "internal", path: "/instance/metadata/00000000-0000-0000-0000-000000000123", err: errors.New("database unavailable"), status: http.StatusInternalServerError, code: "internal_error"},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := &instanceHandler{metadataReader: &metadataReaderStub{err: test.err}}
			router := gin.New()
			router.GET("/instance/metadata/:instanceId", handler.Metadata)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
			if response.Code != test.status || !strings.Contains(response.Body.String(), `"code":"`+test.code+`"`) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}
