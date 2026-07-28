package auth_middleware

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

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
