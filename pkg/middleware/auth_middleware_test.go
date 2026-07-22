package auth_middleware

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/evolution-foundation/evolution-go/pkg/config"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	"github.com/gin-gonic/gin"
)

type tokenResolver struct{}

func (tokenResolver) GetInstanceByToken(token string) (*instance_model.Instance, error) {
	if token == "instance-token" {
		return &instance_model.Instance{Id: "instance-a"}, nil
	}
	return nil, errors.New("not found")
}

func TestAuthAdminOrInstance(t *testing.T) {
	gin.SetMode(gin.TestMode)
	middleware := NewMiddleware(&config.Config{GlobalApiKey: "admin-token"}, tokenResolver{})
	router := gin.New()
	router.GET("/capabilities", middleware.AuthAdminOrInstance, func(ctx *gin.Context) {
		_, hasInstance := ctx.Get("instance")
		ctx.JSON(http.StatusOK, gin.H{"scope": ctx.GetString("auth_scope"), "hasInstance": hasInstance})
	})

	for _, test := range []struct {
		token  string
		status int
		body   string
	}{
		{"admin-token", 200, `{"hasInstance":false,"scope":"admin"}`},
		{"instance-token", 200, `{"hasInstance":true,"scope":"instance"}`},
		{"invalid", 401, `{"error":"not authorized"}`},
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
