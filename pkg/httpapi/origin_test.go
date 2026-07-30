package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestOriginPolicy(t *testing.T) {
	policy, err := NewOriginPolicy([]string{"https://console.example.com/"})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, origin, host string
		want               bool
	}{
		{name: "non browser", host: "api.example.com", want: true},
		{name: "allowlisted", origin: "https://console.example.com", host: "api.example.com", want: true},
		{name: "same host", origin: "https://api.example.com", host: "api.example.com", want: true},
		{name: "different origin", origin: "https://evil.example", host: "api.example.com", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "http://"+test.host+"/", nil)
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			if got := policy.Allows(request); got != test.want {
				t.Fatalf("Allows()=%t want %t", got, test.want)
			}
		})
	}
}

func TestOriginMiddlewareRejectsAndDoesNotAdvertiseCredentials(t *testing.T) {
	gin.SetMode(gin.TestMode)
	policy, err := NewOriginPolicy([]string{"https://console.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.Use(RequestIdentity(), policy.Middleware())
	router.POST("/resource", func(ctx *gin.Context) { ctx.Status(http.StatusOK) })

	allowed := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodOptions, "/resource", nil)
	request.Header.Set("Origin", "https://console.example.com")
	router.ServeHTTP(allowed, request)
	if allowed.Code != http.StatusNoContent || allowed.Header().Get("Access-Control-Allow-Origin") != "https://console.example.com" {
		t.Fatalf("allowed preflight status=%d headers=%v", allowed.Code, allowed.Header())
	}
	if value := allowed.Header().Get("Access-Control-Allow-Credentials"); value != "" {
		t.Fatalf("credentials header must be absent, got %q", value)
	}

	rejected := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/resource", nil)
	request.Header.Set("Origin", "https://evil.example")
	router.ServeHTTP(rejected, request)
	if rejected.Code != http.StatusForbidden {
		t.Fatalf("rejected status=%d body=%s", rejected.Code, rejected.Body.String())
	}
	if body := rejected.Body.String(); !strings.Contains(body, `"code":"origin_not_allowed"`) || !strings.Contains(body, `"requestId"`) {
		t.Fatalf("rejected response is not machine-readable: %s", body)
	}
}

func TestOriginPolicyRejectsUnsafeConfiguration(t *testing.T) {
	for _, value := range []string{"*", "file://console", "https://user:pass@example.com", "https://example.com/path", "https://example.com?token=value"} {
		if _, err := NewOriginPolicy([]string{value}); err == nil {
			t.Fatalf("NewOriginPolicy(%q) succeeded", value)
		}
	}
}
