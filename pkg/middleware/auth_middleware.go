package auth_middleware

import (
	"errors"
	"net/http"

	"github.com/evolution-foundation/evolution-go/pkg/config"
	"github.com/evolution-foundation/evolution-go/pkg/httpapi"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	instance_service "github.com/evolution-foundation/evolution-go/pkg/instance/service"
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
		httpapi.SetAuthPrincipal(ctx, httpapi.AuthPrincipal{Scope: httpapi.CredentialScopeAdmin})
		ctx.Next()
		return
	}
	instance, err := m.instanceService.GetInstanceByToken(token)
	if errors.Is(err, instance_service.ErrInvalidInstanceCredential) {
		ctx.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "not authorized"})
		return
	}
	if err != nil {
		httpapi.WriteInternal(ctx, err)
		ctx.Abort()
		return
	}
	if instance == nil || instance.Id == "" {
		httpapi.WriteInternal(ctx, errors.New("instance credential resolver returned an invalid principal"))
		ctx.Abort()
		return
	}
	ctx.Set("auth_scope", "instance")
	ctx.Set("instance", instance)
	httpapi.SetAuthPrincipal(ctx, httpapi.AuthPrincipal{Scope: httpapi.CredentialScopeInstance, InstanceID: instance.Id})
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
	httpapi.SetAuthPrincipal(ctx, httpapi.AuthPrincipal{Scope: httpapi.CredentialScopeInstance, InstanceID: instance.Id})

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

	httpapi.SetAuthPrincipal(ctx, httpapi.AuthPrincipal{Scope: httpapi.CredentialScopeAdmin})
	ctx.Next()
}

func NewMiddleware(config *config.Config, instanceService InstanceTokenResolver) *middleware {
	return &middleware{config: config, instanceService: instanceService}
}
