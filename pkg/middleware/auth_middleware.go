package auth_middleware

import (
	"net/http"

	"github.com/evolution-foundation/evolution-go/pkg/config"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	"github.com/gin-gonic/gin"
)

type Middleware interface {
	Auth(ctx *gin.Context)
	AuthAdmin(ctx *gin.Context)
	AuthAdminOrInstance(ctx *gin.Context)
}

type InstanceTokenResolver interface {
	GetInstanceByToken(token string) (*instance_model.Instance, error)
}

type middleware struct {
	config          *config.Config
	instanceService InstanceTokenResolver
}

func (m middleware) AuthAdminOrInstance(ctx *gin.Context) {
	token := ctx.GetHeader("apikey")
	if token == "" {
		ctx.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "not authorized"})
		return
	}
	if token == m.config.GlobalApiKey {
		ctx.Set("auth_scope", "admin")
		ctx.Next()
		return
	}
	instance, err := m.instanceService.GetInstanceByToken(token)
	if err != nil {
		ctx.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "not authorized"})
		return
	}
	ctx.Set("auth_scope", "instance")
	ctx.Set("instance", instance)
	ctx.Next()
}

func (m middleware) Auth(ctx *gin.Context) {
	token := ctx.GetHeader("apikey")
	if token == "" {
		ctx.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "not authorized"})
		return
	}

	instance, err := m.instanceService.GetInstanceByToken(token)
	if err != nil {
		ctx.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "not authorized"})
		return
	}

	ctx.Set("instance", instance)

	ctx.Next()
}

func (m middleware) AuthAdmin(ctx *gin.Context) {
	token := ctx.GetHeader("apikey")
	if token == "" {
		ctx.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "not authorized"})
		return
	}

	if token != m.config.GlobalApiKey {
		ctx.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "not authorized"})
		return
	}

	ctx.Next()
}

func NewMiddleware(config *config.Config, instanceService InstanceTokenResolver) *middleware {
	return &middleware{config: config, instanceService: instanceService}
}
