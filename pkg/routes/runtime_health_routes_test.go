package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	server_handler "github.com/evolution-foundation/evolution-go/pkg/server/handler"
	"github.com/gin-gonic/gin"
)

type routeRuntimeHealth struct{}

func (routeRuntimeHealth) Live() bool  { return true }
func (routeRuntimeHealth) Ready() bool { return true }

func TestRuntimeHealthRoutesAreUnauthenticatedAndAdditive(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	configured := &Routes{serverHandler: server_handler.NewServerHandler(
		"test", "abc123", nil, nil, nil, server_handler.WithRuntimeHealth(routeRuntimeHealth{}),
	)}
	configured.assignRuntimeHealthRoutes(router)

	for _, path := range []string{"/server/ok", "/server/live", "/server/ready"} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		router.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("path=%s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
}

func TestRuntimeHealthRoutesAreAbsentWithoutHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	(&Routes{}).assignRuntimeHealthRoutes(router)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/server/live", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status=%d", response.Code)
	}
}
