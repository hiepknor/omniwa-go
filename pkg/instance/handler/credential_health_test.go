package instance_handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	instance_credential "github.com/evolution-foundation/evolution-go/pkg/instance/credential"
	instance_repository "github.com/evolution-foundation/evolution-go/pkg/instance/repository"
	"github.com/gin-gonic/gin"
)

type handlerCredentialHealthReader struct {
	snapshot *instance_repository.CredentialHealthSnapshot
	err      error
}

func (r *handlerCredentialHealthReader) CredentialHealth(_ context.Context, _ int) (*instance_repository.CredentialHealthSnapshot, error) {
	return r.snapshot, r.err
}

func TestCredentialHealthReturnsOnlyMigrationFacts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	reader := &handlerCredentialHealthReader{snapshot: &instance_repository.CredentialHealthSnapshot{
		GeneratedAt: time.Unix(300, 0).UTC(), CurrentKeyVersion: 9, TotalInstances: 3,
		CurrentDigestInstances: 2, PlaintextOnlyInstances: 1, FallbackLookups: 4, FallbackAffectedInstances: 1,
	}}
	handler := &instanceHandler{credentialHealth: instance_credential.NewHealthService(reader, 9)}
	router := gin.New()
	router.GET("/instance/credential-health", handler.CredentialHealth)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/instance/credential-health", nil))
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `"currentKeyVersion":9`) ||
		!strings.Contains(body, `"plaintextOnly":1`) || !strings.Contains(body, `"lookups":4`) ||
		strings.Contains(body, "tokenDigest") || strings.Contains(body, "token\"") {
		t.Fatalf("status=%d body=%s", response.Code, body)
	}
}

func TestCredentialHealthMapsUnavailableAndInternalErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name    string
		handler *instanceHandler
		status  int
		code    string
	}{
		{name: "unconfigured", handler: &instanceHandler{}, status: http.StatusServiceUnavailable, code: "capability_unavailable"},
		{name: "repository failure", handler: &instanceHandler{credentialHealth: instance_credential.NewHealthService(&handlerCredentialHealthReader{err: errors.New("database unavailable")}, 1)}, status: http.StatusInternalServerError, code: "internal_error"},
	} {
		t.Run(test.name, func(t *testing.T) {
			router := gin.New()
			router.GET("/instance/credential-health", test.handler.CredentialHealth)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/instance/credential-health", nil))
			if response.Code != test.status || !strings.Contains(response.Body.String(), `"code":"`+test.code+`"`) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}
