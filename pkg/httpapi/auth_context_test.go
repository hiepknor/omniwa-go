package httpapi

import (
	"testing"

	"github.com/gin-gonic/gin"
)

func TestAuthPrincipalContextRequiresScopeInvariant(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name      string
		principal AuthPrincipal
		want      bool
	}{
		{name: "admin", principal: AuthPrincipal{Scope: CredentialScopeAdmin}, want: true},
		{name: "instance", principal: AuthPrincipal{Scope: CredentialScopeInstance, InstanceID: "instance-a"}, want: true},
		{name: "admin with instance", principal: AuthPrincipal{Scope: CredentialScopeAdmin, InstanceID: "instance-a"}},
		{name: "instance without id", principal: AuthPrincipal{Scope: CredentialScopeInstance}},
		{name: "unknown scope", principal: AuthPrincipal{Scope: "operator"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, _ := gin.CreateTestContext(nil)
			SetAuthPrincipal(ctx, test.principal)
			got, ok := AuthPrincipalFrom(ctx)
			if ok != test.want {
				t.Fatalf("principal=%#v ok=%v", got, ok)
			}
		})
	}
}
