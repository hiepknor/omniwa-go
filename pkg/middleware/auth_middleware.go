package auth_middleware

import (
	"crypto/subtle"
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
	authFailures    *authFailureLimiter
}

func (m middleware) AuthAdminOrInstance(ctx *gin.Context) {
	source, ok := m.admitAuthentication(ctx)
	if !ok {
		return
	}
	token := ctx.GetHeader("apikey")
	if token == "" {
		m.rejectAuthentication(ctx, source)
		return
	}
	if credentialsEqual(token, m.config.GlobalApiKey) {
		ctx.Set("auth_scope", "admin")
		httpapi.SetAuthPrincipal(ctx, httpapi.AuthPrincipal{Scope: httpapi.CredentialScopeAdmin})
		ctx.Next()
		return
	}
	instance, err := m.instanceService.GetInstanceByToken(token)
	if errors.Is(err, instance_service.ErrInvalidInstanceCredential) {
		m.rejectAuthentication(ctx, source)
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
	source, ok := m.admitAuthentication(ctx)
	if !ok {
		return
	}
	token := ctx.GetHeader("apikey")
	if token == "" {
		m.rejectAuthentication(ctx, source)
		return
	}

	instance, err := m.instanceService.GetInstanceByToken(token)
	if errors.Is(err, instance_service.ErrInvalidInstanceCredential) {
		m.rejectAuthentication(ctx, source)
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

	ctx.Set("instance", instance)
	httpapi.SetAuthPrincipal(ctx, httpapi.AuthPrincipal{Scope: httpapi.CredentialScopeInstance, InstanceID: instance.Id})

	ctx.Next()
}

func (m middleware) AuthAdmin(ctx *gin.Context) {
	source, ok := m.admitAuthentication(ctx)
	if !ok {
		return
	}
	token := ctx.GetHeader("apikey")
	if token == "" {
		m.rejectAuthentication(ctx, source)
		return
	}

	if !credentialsEqual(token, m.config.GlobalApiKey) {
		m.rejectAuthentication(ctx, source)
		return
	}

	httpapi.SetAuthPrincipal(ctx, httpapi.AuthPrincipal{Scope: httpapi.CredentialScopeAdmin})
	ctx.Next()
}

func NewMiddleware(config *config.Config, instanceService InstanceTokenResolver) *middleware {
	return &middleware{config: config, instanceService: instanceService, authFailures: defaultAuthFailureLimiter()}
}

func (m middleware) admitAuthentication(ctx *gin.Context) (string, bool) {
	source := authSourceKey(ctx)
	if retryAfter, limited := m.authFailures.retryAfter(source); limited {
		writeAuthRateLimit(ctx, retryAfter)
		return "", false
	}
	return source, true
}

func (m middleware) rejectAuthentication(ctx *gin.Context, source string) {
	if retryAfter, limited := m.authFailures.recordFailure(source); limited {
		writeAuthRateLimit(ctx, retryAfter)
		return
	}
	ctx.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "not authorized"})
}

func credentialsEqual(provided, expected string) bool {
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}
