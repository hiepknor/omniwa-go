package instance_credential

import (
	"context"
	"errors"
	"testing"
	"time"

	instance_repository "github.com/evolution-foundation/evolution-go/pkg/instance/repository"
)

type recordingRotator struct {
	operation instance_repository.TokenRotation
	err       error
}

type recordingRuntimeUpdater struct{ instanceID, token string }

func (u *recordingRuntimeUpdater) UpdateInstanceToken(instanceID, token string) {
	u.instanceID, u.token = instanceID, token
}

func (r *recordingRotator) RotateToken(_ context.Context, operation instance_repository.TokenRotation) (*instance_repository.TokenRotationResult, error) {
	r.operation = operation
	if r.err != nil {
		return nil, r.err
	}
	return &instance_repository.TokenRotationResult{InstanceID: operation.InstanceID, TokenGeneration: operation.ExpectedGeneration + 1, RotatedAt: operation.OccurredAt}, nil
}

func TestRotationServiceGeneratesOneTimeTokenAndHashesActor(t *testing.T) {
	repository := &recordingRotator{}
	updater := &recordingRuntimeUpdater{}
	service := NewRotationService(repository, WithRuntimeTokenUpdater(updater))
	service.now = func() time.Time { return time.Unix(100, 0) }
	service.generateToken = func() (string, error) { return "new-random-token", nil }
	result, err := service.Rotate(context.Background(), RotationRequest{
		InstanceID: "00000000-0000-0000-0000-000000000123", ExpectedGeneration: 4, Reason: " operator-requested rotation ",
		ActorCredential: "admin-secret", RequestID: "request-identity-0001",
	})
	if err != nil || result.Token != "new-random-token" || result.CredentialVersion != 5 {
		t.Fatalf("Rotate() = %#v, %v", result, err)
	}
	if repository.operation.NewToken != result.Token || repository.operation.Reason != "operator-requested rotation" ||
		repository.operation.ActorReferenceHash == "admin-secret" || len(repository.operation.ActorReferenceHash) != 64 {
		t.Fatalf("unsafe rotation operation = %#v", repository.operation)
	}
	if updater.instanceID != result.InstanceID || updater.token != result.Token {
		t.Fatalf("runtime update = %#v", updater)
	}
}

func TestRotationServiceRejectsInvalidRequestsAndMapsRepositoryValidation(t *testing.T) {
	service := NewRotationService(&recordingRotator{})
	if _, err := service.Rotate(context.Background(), RotationRequest{}); !errors.Is(err, ErrInvalidRotationRequest) {
		t.Fatalf("invalid request error = %v", err)
	}
	repository := &recordingRotator{err: instance_repository.ErrInvalidTokenRotation}
	service = NewRotationService(repository)
	service.generateToken = func() (string, error) { return "token", nil }
	_, err := service.Rotate(context.Background(), RotationRequest{
		InstanceID: "bad-id", ExpectedGeneration: 1, Reason: "rotate", ActorCredential: "admin", RequestID: "request-identity-0001",
	})
	if !errors.Is(err, ErrInvalidRotationRequest) {
		t.Fatalf("repository validation error = %v", err)
	}
}

func TestSecureInstanceTokenHasHighEntropyEncoding(t *testing.T) {
	first, err := secureInstanceToken()
	if err != nil {
		t.Fatal(err)
	}
	second, err := secureInstanceToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 43 || first == second {
		t.Fatalf("generated tokens are invalid: lengths=%d/%d equal=%t", len(first), len(second), first == second)
	}
}
