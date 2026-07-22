package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRequestIdentityGeneratesAndReturnsRequestID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestIdentity())
	router.GET("/", func(ctx *gin.Context) {
		if requestID := RequestID(ctx); !validRequestID.MatchString(requestID) {
			t.Fatalf("invalid request ID in context: %q", requestID)
		}
		ctx.Status(http.StatusNoContent)
	})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if requestID := response.Header().Get(RequestIDHeader); !validRequestID.MatchString(requestID) {
		t.Fatalf("invalid response request ID: %q", requestID)
	}
}

func TestRequestIdentityAcceptsOnlyBoundedSafeCallerIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name     string
		provided string
		preserve bool
	}{
		{name: "valid", provided: "console-request-123", preserve: true},
		{name: "too short", provided: "short"},
		{name: "log injection", provided: "console-request-123\nforged=true"},
	} {
		t.Run(test.name, func(t *testing.T) {
			router := gin.New()
			router.Use(RequestIdentity())
			router.GET("/", func(ctx *gin.Context) { ctx.Status(http.StatusNoContent) })
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.Header.Set(RequestIDHeader, test.provided)
			response := httptest.NewRecorder()

			router.ServeHTTP(response, request)

			got := response.Header().Get(RequestIDHeader)
			if (got == test.provided) != test.preserve || !validRequestID.MatchString(got) {
				t.Fatalf("provided=%q preserve=%t got=%q", test.provided, test.preserve, got)
			}
		})
	}
}
