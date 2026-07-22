package instance_handler

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/evolution-foundation/evolution-go/pkg/httpapi"
	instance_credential "github.com/evolution-foundation/evolution-go/pkg/instance/credential"
	instance_repository "github.com/evolution-foundation/evolution-go/pkg/instance/repository"
	"github.com/gin-gonic/gin"
)

type handlerTokenRotator struct {
	operation instance_repository.TokenRotation
	err       error
}

func (r *handlerTokenRotator) RotateToken(_ context.Context, operation instance_repository.TokenRotation) (*instance_repository.TokenRotationResult, error) {
	r.operation = operation
	if r.err != nil {
		return nil, r.err
	}
	return &instance_repository.TokenRotationResult{InstanceID: operation.InstanceID, TokenGeneration: operation.ExpectedGeneration + 1, RotatedAt: operation.OccurredAt}, nil
}

func TestRotateTokenReturnsSecretOnceWithoutLeakingAdminCredential(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := &handlerTokenRotator{}
	service := instance_credential.NewRotationService(repository)
	handler := &instanceHandler{tokenRotation: service}
	router := gin.New()
	router.Use(httpapi.RequestIdentity())
	router.POST("/instance/rotate-token/:instanceId", handler.RotateToken)

	request := httptest.NewRequest(http.MethodPost, "/instance/rotate-token/00000000-0000-0000-0000-000000000123", bytes.NewBufferString(`{"expectedVersion":2,"reason":"scheduled rotation"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("apikey", "admin-secret")
	request.Header.Set(httpapi.RequestIDHeader, "request-identity-0001")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"credentialVersion":3`) ||
		strings.Contains(response.Body.String(), "admin-secret") || repository.operation.ActorReferenceHash == "admin-secret" ||
		repository.operation.RequestID != "request-identity-0001" || repository.operation.Reason != "scheduled rotation" {
		t.Fatalf("rotation response=%d body=%s operation=%#v", response.Code, response.Body.String(), repository.operation)
	}
}

func TestRotateTokenMapsKnownErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "missing", err: instance_repository.ErrTokenRotationNotFound, status: http.StatusNotFound, code: "instance_not_found"},
		{name: "conflict", err: instance_repository.ErrTokenRotationConflict, status: http.StatusConflict, code: "credential_version_conflict"},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := &handlerTokenRotator{err: test.err}
			service := instance_credential.NewRotationService(repository)
			handler := &instanceHandler{tokenRotation: service}
			router := gin.New()
			router.Use(httpapi.RequestIdentity())
			router.POST("/instance/rotate-token/:instanceId", handler.RotateToken)
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/instance/rotate-token/00000000-0000-0000-0000-000000000123", bytes.NewBufferString(`{"expectedVersion":1,"reason":"rotate now"}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("apikey", "admin-secret")
			request.Header.Set(httpapi.RequestIDHeader, "request-identity-0002")
			router.ServeHTTP(response, request)
			if response.Code != test.status || !strings.Contains(response.Body.String(), `"code":"`+test.code+`"`) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}
