package httpapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestWriteInternalReturnsStableSafeContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Set(requestIDKey, "console-request-123")

	WriteInternal(ctx, errors.New("database password=do-not-return"))

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d", response.Code)
	}
	body := response.Body.String()
	for _, expected := range []string{`"error":"internal server error"`, `"code":"internal_error"`, `"requestId":"console-request-123"`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("body=%s missing=%s", body, expected)
		}
	}
	if strings.Contains(body, "database") || strings.Contains(body, "do-not-return") {
		t.Fatalf("internal detail leaked: %s", body)
	}
}

func TestRequestIdentityCorrelatesInternalError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestIdentity())
	router.GET("/", func(ctx *gin.Context) { WriteInternal(ctx, errors.New("storage failed")) })
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set(RequestIDHeader, "console-request-123")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Header().Get(RequestIDHeader) != "console-request-123" ||
		!strings.Contains(response.Body.String(), `"requestId":"console-request-123"`) {
		t.Fatalf("header=%q body=%s", response.Header().Get(RequestIDHeader), response.Body.String())
	}
}

func TestWriteErrorCannotExposeHTTP500Detail(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)

	WriteError(ctx, http.StatusInternalServerError, "database_failed", "connection to secret-host failed")

	body := response.Body.String()
	if !strings.Contains(body, `"code":"internal_error"`) || strings.Contains(body, "database_failed") || strings.Contains(body, "secret-host") {
		t.Fatalf("unsafe HTTP 500 body: %s", body)
	}
}
