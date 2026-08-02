package auth_middleware

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/evolution-foundation/evolution-go/pkg/config"
	"github.com/evolution-foundation/evolution-go/pkg/httpapi"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	instance_service "github.com/evolution-foundation/evolution-go/pkg/instance/service"
	"github.com/gin-gonic/gin"
)

type tokenResolver struct{}

func (tokenResolver) GetInstanceByToken(token string) (*instance_model.Instance, error) {
	switch token {
	case "instance-token":
		return &instance_model.Instance{Id: "instance-a"}, nil
	case "invalid":
		return nil, instance_service.ErrInvalidInstanceCredential
	case "broken":
		return nil, errors.New("credential database unavailable")
	default:
		return nil, instance_service.ErrInvalidInstanceCredential
	}
}

type countingTokenResolver struct{ calls int }

func (r *countingTokenResolver) GetInstanceByToken(token string) (*instance_model.Instance, error) {
	r.calls++
	if token == "instance-token" {
		return &instance_model.Instance{Id: "instance-a"}, nil
	}
	return nil, instance_service.ErrInvalidInstanceCredential
}

func TestAuthAdminOrInstance(t *testing.T) {
	gin.SetMode(gin.TestMode)
	middleware := NewMiddleware(&config.Config{GlobalApiKey: "admin-token"}, tokenResolver{})
	router := gin.New()
	router.GET("/capabilities", middleware.AuthAdminOrInstance, func(ctx *gin.Context) {
		_, hasInstance := ctx.Get("instance")
		principal, authenticated := httpapi.AuthPrincipalFrom(ctx)
		ctx.JSON(http.StatusOK, gin.H{"scope": principal.Scope, "instanceId": principal.InstanceID, "hasInstance": hasInstance, "authenticated": authenticated})
	})

	for _, test := range []struct {
		token  string
		status int
		body   string
	}{
		{"admin-token", 200, `{"authenticated":true,"hasInstance":false,"instanceId":"","scope":"admin"}`},
		{"instance-token", 200, `{"authenticated":true,"hasInstance":true,"instanceId":"instance-a","scope":"instance"}`},
		{"invalid", 401, `{"error":"not authorized"}`},
		{"broken", 500, `{"code":"internal_error","error":"internal server error"}`},
		{"", 401, `{"error":"not authorized"}`},
	} {
		request := httptest.NewRequest(http.MethodGet, "/capabilities", nil)
		request.Header.Set("apikey", test.token)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != test.status || response.Body.String() != test.body {
			t.Fatalf("token %q: status=%d body=%s", test.token, response.Code, response.Body.String())
		}
	}
}

func TestAuthDistinguishesInvalidCredentialFromInfrastructureFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	middleware := NewMiddleware(&config.Config{GlobalApiKey: "admin-token"}, tokenResolver{})
	router := gin.New()
	router.GET("/protected", middleware.Auth, func(ctx *gin.Context) { ctx.Status(http.StatusNoContent) })

	for _, test := range []struct {
		token  string
		status int
		body   string
	}{
		{token: "instance-token", status: http.StatusNoContent},
		{token: "invalid", status: http.StatusUnauthorized, body: `{"error":"not authorized"}`},
		{token: "broken", status: http.StatusInternalServerError, body: `{"code":"internal_error","error":"internal server error"}`},
	} {
		request := httptest.NewRequest(http.MethodGet, "/protected", nil)
		request.Header.Set("apikey", test.token)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != test.status || (test.body != "" && response.Body.String() != test.body) {
			t.Fatalf("token %q: status=%d body=%s", test.token, response.Code, response.Body.String())
		}
	}
}

func TestAuthRateLimitsRepeatedFailuresBeforeCredentialLookup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resolver := &countingTokenResolver{}
	middleware := NewMiddleware(&config.Config{GlobalApiKey: "admin-token"}, resolver)
	middleware.authFailures = newAuthFailureLimiter(authFailureLimiterSettings{
		MaxFailures: 2,
		Window:      time.Minute,
		BlockFor:    time.Minute,
		EntryTTL:    time.Hour,
		MaxEntries:  10,
	})
	router := gin.New()
	router.GET("/protected", middleware.Auth, func(ctx *gin.Context) { ctx.Status(http.StatusNoContent) })

	for attempt, expected := range []int{http.StatusUnauthorized, http.StatusTooManyRequests, http.StatusTooManyRequests} {
		request := httptest.NewRequest(http.MethodGet, "/protected", nil)
		request.Header.Set("apikey", "invalid-token")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != expected {
			t.Fatalf("attempt %d: status=%d body=%s", attempt+1, response.Code, response.Body.String())
		}
		if expected == http.StatusTooManyRequests && response.Header().Get("Retry-After") == "" {
			t.Fatalf("attempt %d: missing Retry-After header", attempt+1)
		}
	}
	if resolver.calls != 2 {
		t.Fatalf("blocked request reached credential storage: calls=%d", resolver.calls)
	}
}

func TestSuccessfulAuthenticationDoesNotConsumeOrEraseFailureBudget(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resolver := &countingTokenResolver{}
	middleware := NewMiddleware(&config.Config{GlobalApiKey: "admin-token"}, resolver)
	middleware.authFailures = newAuthFailureLimiter(authFailureLimiterSettings{
		MaxFailures: 2,
		Window:      time.Minute,
		BlockFor:    time.Minute,
		EntryTTL:    time.Hour,
		MaxEntries:  10,
	})
	router := gin.New()
	router.GET("/protected", middleware.Auth, func(ctx *gin.Context) { ctx.Status(http.StatusNoContent) })

	for _, test := range []struct {
		token  string
		status int
	}{
		{token: "invalid-token", status: http.StatusUnauthorized},
		{token: "instance-token", status: http.StatusNoContent},
		{token: "invalid-token", status: http.StatusTooManyRequests},
	} {
		request := httptest.NewRequest(http.MethodGet, "/protected", nil)
		request.Header.Set("apikey", test.token)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != test.status {
			t.Fatalf("token %q: status=%d body=%s", test.token, response.Code, response.Body.String())
		}
	}
}
