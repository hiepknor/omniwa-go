package instance_handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/evolution-foundation/evolution-go/pkg/config"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	instance_service "github.com/evolution-foundation/evolution-go/pkg/instance/service"
	auth_middleware "github.com/evolution-foundation/evolution-go/pkg/middleware"
	"github.com/gin-gonic/gin"
	"go.mau.fi/whatsmeow"
)

type disconnectedStatusRuntime struct{}

func (disconnectedStatusRuntime) Get(string) *whatsmeow.Client { return nil }
func (disconnectedStatusRuntime) RemoveCurrent(string) bool    { return false }

type authenticatedStatusService struct {
	instance_service.InstanceService
	instances map[string]*instance_model.Instance
}

func (s *authenticatedStatusService) GetInstanceByToken(token string) (*instance_model.Instance, error) {
	instance, ok := s.instances[token]
	if !ok {
		return nil, errors.New("instance not found")
	}
	return instance, nil
}

func statusTestRouter(instances map[string]*instance_model.Instance) *gin.Engine {
	statusService := instance_service.NewInstanceService(nil, disconnectedStatusRuntime{}, nil, nil, nil, nil, nil)
	service := &authenticatedStatusService{InstanceService: statusService, instances: instances}
	handler := NewInstanceHandler(service, &config.Config{})
	auth := auth_middleware.NewMiddleware(&config.Config{}, service)
	router := gin.New()
	router.GET("/instance/status", auth.Auth, handler.Status)
	return router
}

func performStatusRequest(router http.Handler, token, target string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, target, nil)
	if token != "" {
		request.Header.Set("apikey", token)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func decodeStatusData(t *testing.T, response *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var payload struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	return payload.Data
}

func TestStatusReturnsAuthenticatedIdentityWhileDisconnectedOrUnpaired(t *testing.T) {
	gin.SetMode(gin.TestMode)
	firstID := "d2b52b8b-64ce-4eae-a2bb-33a7fd6ae4ce"
	secondID := "00000000-0000-0000-0000-000000000456"
	router := statusTestRouter(map[string]*instance_model.Instance{
		"first-token":  {Id: firstID, Name: "Nibi EU", Jid: "", Qrcode: ""},
		"second-token": {Id: secondID, Name: "Nibi US", Jid: "", Qrcode: ""},
	})

	for _, test := range []struct {
		name         string
		token        string
		instanceID   string
		instanceName string
	}{
		{name: "disconnected and unpaired first instance", token: "first-token", instanceID: firstID, instanceName: "Nibi EU"},
		{name: "second token is isolated to second instance", token: "second-token", instanceID: secondID, instanceName: "Nibi US"},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := performStatusRequest(router, test.token, "/instance/status?instanceId="+secondID)
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			data := decodeStatusData(t, response)
			if data["InstanceId"] != test.instanceID || data["InstanceName"] != test.instanceName {
				t.Fatalf("identity=%v", data)
			}
			if data["Connected"] != false || data["LoggedIn"] != false || data["Name"] != "" {
				t.Fatalf("legacy status fields=%v", data)
			}
		})
	}
}

func TestStatusFallsBackToCanonicalIDForLegacyEmptyName(t *testing.T) {
	gin.SetMode(gin.TestMode)
	instanceID := "00000000-0000-0000-0000-000000000123"
	router := statusTestRouter(map[string]*instance_model.Instance{
		"legacy-token": {Id: instanceID, Name: "   "},
	})

	response := performStatusRequest(router, "legacy-token", "/instance/status")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	data := decodeStatusData(t, response)
	if data["InstanceId"] != instanceID || data["InstanceName"] != instanceID {
		t.Fatalf("legacy identity=%v", data)
	}
}

func TestStatusResponseExposesOnlyStatusAndSafeIdentityFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	digest := "lookup-digest-secret"
	keyVersion := 7
	instance := &instance_model.Instance{
		Id: "00000000-0000-0000-0000-000000000123", Name: "primary", Token: "bearer-secret",
		TokenDigest: &digest, TokenKeyVersion: &keyVersion, Proxy: "proxy-credential-secret", Qrcode: "qr-material-secret",
	}
	router := statusTestRouter(map[string]*instance_model.Instance{"bearer-secret": instance})
	response := performStatusRequest(router, "bearer-secret", "/instance/status")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}

	body := response.Body.String()
	for _, secret := range []string{"bearer-secret", digest, "proxy-credential-secret", "qr-material-secret"} {
		if strings.Contains(body, secret) {
			t.Fatalf("response exposed secret %q: %s", secret, body)
		}
	}
	data := decodeStatusData(t, response)
	wantKeys := map[string]bool{"Connected": true, "LoggedIn": true, "Name": true, "InstanceId": true, "InstanceName": true}
	if len(data) != len(wantKeys) {
		t.Fatalf("unexpected status fields: %v", data)
	}
	for key := range data {
		if !wantKeys[key] {
			t.Fatalf("unexpected status field %q", key)
		}
	}

	var legacyConsumer struct {
		Data struct {
			Connected bool   `json:"Connected"`
			LoggedIn  bool   `json:"LoggedIn"`
			Name      string `json:"Name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &legacyConsumer); err != nil {
		t.Fatalf("legacy consumer decode failed: %v", err)
	}
	if legacyConsumer.Data.Connected || legacyConsumer.Data.LoggedIn || legacyConsumer.Data.Name != "" {
		t.Fatalf("legacy consumer changed behavior: %+v", legacyConsumer.Data)
	}
}

func TestStatusKeepsNormalizedAuthenticationErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := statusTestRouter(nil)
	for _, token := range []string{"", "invalid-token"} {
		response := performStatusRequest(router, token, "/instance/status")
		if response.Code != http.StatusUnauthorized || response.Body.String() != `{"error":"not authorized"}` {
			t.Fatalf("token=%q status=%d body=%s", token, response.Code, response.Body.String())
		}
	}
}
