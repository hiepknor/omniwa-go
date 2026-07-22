package instance_service

import (
	"context"
	"errors"
	"testing"

	"github.com/evolution-foundation/evolution-go/pkg/waquery"
	"go.mau.fi/whatsmeow/types"
)

type cleanupQueryGuard struct{ removed string }

func (*cleanupQueryGuard) Do(context.Context, string, string, string, waquery.Query) (any, error) {
	return nil, nil
}
func (*cleanupQueryGuard) ObserveError(string, error) error         { return nil }
func (g *cleanupQueryGuard) RemoveInstance(instanceID string)       { g.removed = instanceID }
func (*cleanupQueryGuard) Snapshot(string) (waquery.Snapshot, bool) { return waquery.Snapshot{}, false }

type cleanupIdentityResolver struct{ removed string }

func (*cleanupIdentityResolver) Resolve(context.Context, string, []string, waquery.IdentityQuery) ([]types.IsOnWhatsAppResponse, error) {
	return nil, nil
}
func (r *cleanupIdentityResolver) RemoveInstance(instanceID string) { r.removed = instanceID }

func TestClearInstanceRateLimitStateRemovesAllStateEvenWhenRuntimeCleanupFails(t *testing.T) {
	query := &cleanupQueryGuard{}
	identity := &cleanupIdentityResolver{}
	wantErr := errors.New("runtime cleanup failed")
	err := clearInstanceRateLimitState("instance-a", query, identity, func() error { return wantErr })
	if !errors.Is(err, wantErr) || query.removed != "instance-a" || identity.removed != "instance-a" {
		t.Fatalf("cleanup error=%v query=%q identity=%q", err, query.removed, identity.removed)
	}
}
