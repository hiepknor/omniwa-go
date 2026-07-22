package instance_credential

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	instance_repository "github.com/evolution-foundation/evolution-go/pkg/instance/repository"
)

var ErrInvalidRotationRequest = errors.New("invalid instance token rotation request")

type RotationRequest struct {
	InstanceID         string
	ExpectedGeneration int64
	Reason             string
	ActorCredential    string
	RequestID          string
}

type RotationResult struct {
	InstanceID        string    `json:"instanceId"`
	Token             string    `json:"token"`
	CredentialVersion int64     `json:"credentialVersion"`
	RotatedAt         time.Time `json:"rotatedAt"`
}

type RotationService struct {
	repository     instance_repository.TokenRotator
	runtimeUpdater RuntimeTokenUpdater
	now            func() time.Time
	generateToken  func() (string, error)
}

type RuntimeTokenUpdater interface {
	UpdateInstanceToken(instanceID, token string)
}

type RotationOption func(*RotationService)

func WithRuntimeTokenUpdater(updater RuntimeTokenUpdater) RotationOption {
	return func(service *RotationService) { service.runtimeUpdater = updater }
}

func NewRotationService(repository instance_repository.TokenRotator, options ...RotationOption) *RotationService {
	service := &RotationService{repository: repository, now: time.Now, generateToken: secureInstanceToken}
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	return service
}

func (s *RotationService) Rotate(ctx context.Context, request RotationRequest) (*RotationResult, error) {
	request.Reason = strings.TrimSpace(request.Reason)
	if s == nil || s.repository == nil || s.now == nil || s.generateToken == nil || request.InstanceID == "" ||
		request.ExpectedGeneration <= 0 || request.Reason == "" || request.ActorCredential == "" || request.RequestID == "" {
		return nil, ErrInvalidRotationRequest
	}
	newToken, err := s.generateToken()
	if err != nil {
		return nil, err
	}
	actorHash := sha256.Sum256([]byte("instance_token_rotation_admin\x00" + request.ActorCredential))
	operation := instance_repository.TokenRotation{
		InstanceID: request.InstanceID, ExpectedGeneration: request.ExpectedGeneration, NewToken: newToken,
		Reason: request.Reason, ActorReferenceHash: hex.EncodeToString(actorHash[:]), RequestID: request.RequestID,
		OccurredAt: s.now().UTC(),
	}
	result, err := s.repository.RotateToken(ctx, operation)
	if errors.Is(err, instance_repository.ErrInvalidTokenRotation) {
		return nil, ErrInvalidRotationRequest
	}
	if err != nil {
		return nil, err
	}
	if s.runtimeUpdater != nil {
		s.runtimeUpdater.UpdateInstanceToken(result.InstanceID, newToken)
	}
	return &RotationResult{
		InstanceID: result.InstanceID, Token: newToken, CredentialVersion: result.TokenGeneration, RotatedAt: result.RotatedAt,
	}, nil
}

func secureInstanceToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}
