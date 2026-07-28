package routes

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/evolution-foundation/evolution-go/pkg/config"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	auth_middleware "github.com/evolution-foundation/evolution-go/pkg/middleware"
	"github.com/gin-gonic/gin"
)

type metricsInstanceResolver struct{}

func (metricsInstanceResolver) GetInstanceByToken(token string) (*instance_model.Instance, error) {
	if token == "instance-token" {
		return &instance_model.Instance{Id: "instance-id"}, nil
	}
	return nil, errors.New("not found")
}

func TestMetricsRouteRequiresGlobalAdminKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	middleware := auth_middleware.NewMiddleware(&config.Config{GlobalApiKey: "global-admin-key"}, metricsInstanceResolver{})
	router := gin.New()
	configured := &Routes{
		authMiddleware: middleware,
		metricsHandler: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "text/plain")
			_, _ = writer.Write([]byte("metric 1\n"))
		}),
	}
	configured.assignMetricsRoute(router)

	for _, test := range []struct {
		name   string
		token  string
		status int
	}{
		{name: "missing", status: http.StatusUnauthorized},
		{name: "instance token", token: "instance-token", status: http.StatusUnauthorized},
		{name: "global admin", token: "global-admin-key", status: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
			request.Header.Set("apikey", test.token)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if test.status == http.StatusOK && response.Body.String() != "metric 1\n" {
				t.Fatalf("body=%q", response.Body.String())
			}
		})
	}
}

func TestMetricsRouteIsAbsentWithoutHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	configured := &Routes{}
	configured.assignMetricsRoute(router)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status=%d", response.Code)
	}
}
