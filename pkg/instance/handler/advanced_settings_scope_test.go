package instance_handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/evolution-foundation/evolution-go/pkg/httpapi"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	"github.com/gin-gonic/gin"
)

const (
	advancedSettingsOwnerID = "11111111-1111-1111-1111-111111111111"
	advancedSettingsOtherID = "22222222-2222-2222-2222-222222222222"
)

type advancedSettingsServiceSpy struct {
	getInstanceIDs    []string
	updateInstanceIDs []string
	updatedSettings   *instance_model.AdvancedSettings
	settings          *instance_model.AdvancedSettings
}

func (s *advancedSettingsServiceSpy) GetAdvancedSettings(instanceID string) (*instance_model.AdvancedSettings, error) {
	s.getInstanceIDs = append(s.getInstanceIDs, instanceID)
	return s.settings, nil
}

func (s *advancedSettingsServiceSpy) UpdateAdvancedSettings(instanceID string, settings *instance_model.AdvancedSettings) error {
	s.updateInstanceIDs = append(s.updateInstanceIDs, instanceID)
	s.updatedSettings = settings
	return nil
}

func TestAdvancedSettingsRejectsCrossInstanceAccess(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, method := range []string{http.MethodGet, http.MethodPut} {
		t.Run(method, func(t *testing.T) {
			service := &advancedSettingsServiceSpy{}
			response := performAdvancedSettingsRequest(t, service, method, advancedSettingsOtherID, &httpapi.AuthPrincipal{
				Scope:      httpapi.CredentialScopeInstance,
				InstanceID: advancedSettingsOwnerID,
			})

			if response.Code != http.StatusForbidden {
				t.Fatalf("expected status %d, got %d: %s", http.StatusForbidden, response.Code, response.Body.String())
			}
			assertAdvancedSettingsErrorCode(t, response, "instance_scope_mismatch")
			if len(service.getInstanceIDs) != 0 || len(service.updateInstanceIDs) != 0 {
				t.Fatalf("service must not be called for a cross-instance request: get=%v update=%v", service.getInstanceIDs, service.updateInstanceIDs)
			}
		})
	}
}

func TestAdvancedSettingsUsesAuthenticatedInstance(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("get", func(t *testing.T) {
		service := &advancedSettingsServiceSpy{settings: &instance_model.AdvancedSettings{AlwaysOnline: true}}
		response := performAdvancedSettingsRequest(t, service, http.MethodGet, advancedSettingsOwnerID, &httpapi.AuthPrincipal{
			Scope:      httpapi.CredentialScopeInstance,
			InstanceID: advancedSettingsOwnerID,
		})

		if response.Code != http.StatusOK {
			t.Fatalf("expected status %d, got %d: %s", http.StatusOK, response.Code, response.Body.String())
		}
		if len(service.getInstanceIDs) != 1 || service.getInstanceIDs[0] != advancedSettingsOwnerID {
			t.Fatalf("expected one scoped get call, got %v", service.getInstanceIDs)
		}
	})

	t.Run("update", func(t *testing.T) {
		service := &advancedSettingsServiceSpy{}
		response := performAdvancedSettingsRequest(t, service, http.MethodPut, advancedSettingsOwnerID, &httpapi.AuthPrincipal{
			Scope:      httpapi.CredentialScopeInstance,
			InstanceID: advancedSettingsOwnerID,
		})

		if response.Code != http.StatusOK {
			t.Fatalf("expected status %d, got %d: %s", http.StatusOK, response.Code, response.Body.String())
		}
		if len(service.updateInstanceIDs) != 1 || service.updateInstanceIDs[0] != advancedSettingsOwnerID {
			t.Fatalf("expected one scoped update call, got %v", service.updateInstanceIDs)
		}
		if service.updatedSettings == nil || !service.updatedSettings.AlwaysOnline {
			t.Fatalf("expected decoded settings, got %#v", service.updatedSettings)
		}
	})
}

func TestAdvancedSettingsFailsClosedWithoutInstancePrincipal(t *testing.T) {
	gin.SetMode(gin.TestMode)

	service := &advancedSettingsServiceSpy{}
	response := performAdvancedSettingsRequest(t, service, http.MethodGet, advancedSettingsOwnerID, nil)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d: %s", http.StatusUnauthorized, response.Code, response.Body.String())
	}
	assertAdvancedSettingsErrorCode(t, response, "not_authorized")
	if len(service.getInstanceIDs) != 0 {
		t.Fatalf("service must not be called without an instance principal: %v", service.getInstanceIDs)
	}
}

func performAdvancedSettingsRequest(t *testing.T, service advancedSettingsService, method, instanceID string, principal *httpapi.AuthPrincipal) *httptest.ResponseRecorder {
	t.Helper()

	handler := &instanceHandler{advancedSettings: service}
	router := gin.New()
	if principal != nil {
		router.Use(func(ctx *gin.Context) {
			httpapi.SetAuthPrincipal(ctx, *principal)
			ctx.Next()
		})
	}
	router.GET("/instance/:instanceId/advanced-settings", handler.GetAdvancedSettings)
	router.PUT("/instance/:instanceId/advanced-settings", handler.UpdateAdvancedSettings)

	body := bytes.NewBuffer(nil)
	if method == http.MethodPut {
		body = bytes.NewBufferString(`{"alwaysOnline":true}`)
	}
	request := httptest.NewRequest(method, "/instance/"+instanceID+"/advanced-settings", body)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func assertAdvancedSettingsErrorCode(t *testing.T, response *httptest.ResponseRecorder, expected string) {
	t.Helper()

	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Code != expected {
		t.Fatalf("expected error code %q, got %q: %s", expected, body.Code, response.Body.String())
	}
}
