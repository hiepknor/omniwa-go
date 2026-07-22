package instance_credential

import (
	"context"
	"errors"
	"time"

	instance_repository "github.com/evolution-foundation/evolution-go/pkg/instance/repository"
)

var ErrCredentialHealthUnavailable = errors.New("instance credential health is unavailable")

type CredentialCoverage struct {
	Total           int64 `json:"total"`
	CurrentDigest   int64 `json:"currentDigest"`
	PlaintextOnly   int64 `json:"plaintextOnly"`
	OtherKeyVersion int64 `json:"otherKeyVersion"`
}

type PlaintextFallbackUsage struct {
	Lookups           int64      `json:"lookups"`
	AffectedInstances int64      `json:"affectedInstances"`
	FirstObservedAt   *time.Time `json:"firstObservedAt,omitempty"`
	LastObservedAt    *time.Time `json:"lastObservedAt,omitempty"`
}

type CredentialHealth struct {
	GeneratedAt       time.Time              `json:"generatedAt"`
	CurrentKeyVersion int                    `json:"currentKeyVersion"`
	Instances         CredentialCoverage     `json:"instances"`
	PlaintextFallback PlaintextFallbackUsage `json:"plaintextFallback"`
}

type HealthService struct {
	repository        instance_repository.CredentialHealthReader
	currentKeyVersion int
}

func NewHealthService(repository instance_repository.CredentialHealthReader, currentKeyVersion int) *HealthService {
	return &HealthService{repository: repository, currentKeyVersion: currentKeyVersion}
}

func (s *HealthService) Snapshot(ctx context.Context) (*CredentialHealth, error) {
	if s == nil || s.repository == nil || s.currentKeyVersion <= 0 {
		return nil, ErrCredentialHealthUnavailable
	}
	snapshot, err := s.repository.CredentialHealth(ctx, s.currentKeyVersion)
	if err != nil {
		return nil, err
	}
	if snapshot == nil {
		return nil, ErrCredentialHealthUnavailable
	}
	return &CredentialHealth{
		GeneratedAt:       snapshot.GeneratedAt,
		CurrentKeyVersion: snapshot.CurrentKeyVersion,
		Instances: CredentialCoverage{
			Total: snapshot.TotalInstances, CurrentDigest: snapshot.CurrentDigestInstances,
			PlaintextOnly: snapshot.PlaintextOnlyInstances, OtherKeyVersion: snapshot.OtherKeyVersionInstances,
		},
		PlaintextFallback: PlaintextFallbackUsage{
			Lookups: snapshot.FallbackLookups, AffectedInstances: snapshot.FallbackAffectedInstances,
			FirstObservedAt: snapshot.FirstFallbackAt, LastObservedAt: snapshot.LastFallbackAt,
		},
	}, nil
}
