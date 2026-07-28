package httpapi

import "github.com/gin-gonic/gin"

const authPrincipalContextKey = "omniwa.auth.principal"

// CredentialScope is the authenticated authority represented by an apikey.
type CredentialScope string

const (
	CredentialScopeAdmin    CredentialScope = "admin"
	CredentialScopeInstance CredentialScope = "instance"
)

// AuthPrincipal contains only the public-safe identity needed by handlers.
// Credential material and provider identities must never be added here.
type AuthPrincipal struct {
	Scope      CredentialScope
	InstanceID string
}

// Valid reports whether the principal satisfies the scope-specific identity
// invariant. Admin credentials are global; instance credentials always carry
// the stable public instance UUID resolved during authentication.
func (p AuthPrincipal) Valid() bool {
	switch p.Scope {
	case CredentialScopeAdmin:
		return p.InstanceID == ""
	case CredentialScopeInstance:
		return p.InstanceID != ""
	default:
		return false
	}
}

// SetAuthPrincipal stores the typed authentication result for downstream
// handlers. Authentication middleware is the only production caller.
func SetAuthPrincipal(ctx *gin.Context, principal AuthPrincipal) {
	if ctx != nil {
		ctx.Set(authPrincipalContextKey, principal)
	}
}

// AuthPrincipalFrom returns a validated typed authentication result.
func AuthPrincipalFrom(ctx *gin.Context) (AuthPrincipal, bool) {
	if ctx == nil {
		return AuthPrincipal{}, false
	}
	value, exists := ctx.Get(authPrincipalContextKey)
	principal, ok := value.(AuthPrincipal)
	return principal, exists && ok && principal.Valid()
}
