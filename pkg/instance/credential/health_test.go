package instance_credential

import (
	"context"
	"errors"
	"testing"
	"time"

	instance_repository "github.com/evolution-foundation/evolution-go/pkg/instance/repository"
)

type healthReaderStub struct {
	version  int
	snapshot *instance_repository.CredentialHealthSnapshot
	err      error
}

func (r *healthReaderStub) CredentialHealth(_ context.Context, version int) (*instance_repository.CredentialHealthSnapshot, error) {
	r.version = version
	return r.snapshot, r.err
}

func TestCredentialHealthMapsSecretFreeFacts(t *testing.T) {
	first, last := time.Unix(100, 0).UTC(), time.Unix(200, 0).UTC()
	reader := &healthReaderStub{snapshot: &instance_repository.CredentialHealthSnapshot{
		GeneratedAt: time.Unix(300, 0).UTC(), CurrentKeyVersion: 7, TotalInstances: 4,
		CurrentDigestInstances: 2, PlaintextOnlyInstances: 1, OtherKeyVersionInstances: 1,
		FallbackLookups: 3, FallbackAffectedInstances: 2, FirstFallbackAt: &first, LastFallbackAt: &last,
	}}
	health, err := NewHealthService(reader, 7).Snapshot(context.Background())
	if err != nil || reader.version != 7 || health.CurrentKeyVersion != 7 || health.Instances.Total != 4 ||
		health.Instances.CurrentDigest != 2 || health.Instances.PlaintextOnly != 1 || health.Instances.OtherKeyVersion != 1 ||
		health.PlaintextFallback.Lookups != 3 || health.PlaintextFallback.AffectedInstances != 2 ||
		health.PlaintextFallback.FirstObservedAt == nil || !health.PlaintextFallback.LastObservedAt.Equal(last) {
		t.Fatalf("health=%#v version=%d err=%v", health, reader.version, err)
	}
}

func TestCredentialHealthRejectsUnavailableAndPropagatesRepositoryErrors(t *testing.T) {
	if _, err := NewHealthService(nil, 1).Snapshot(context.Background()); !errors.Is(err, ErrCredentialHealthUnavailable) {
		t.Fatalf("nil repository error = %v", err)
	}
	expected := errors.New("database unavailable")
	if _, err := NewHealthService(&healthReaderStub{err: expected}, 1).Snapshot(context.Background()); !errors.Is(err, expected) {
		t.Fatalf("repository error = %v", err)
	}
	if _, err := NewHealthService(&healthReaderStub{}, 1).Snapshot(context.Background()); !errors.Is(err, ErrCredentialHealthUnavailable) {
		t.Fatalf("nil snapshot error = %v", err)
	}
}
