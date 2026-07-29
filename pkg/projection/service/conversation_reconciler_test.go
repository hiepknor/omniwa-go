package projection_service

import (
	"context"
	"errors"
	"testing"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
	"gorm.io/gorm"
)

type conversationBackfillStub struct {
	batches      []*projection_repository.ConversationBackfillBatch
	associations map[string]projection_repository.ConversationAssociationResult
	claimError   error
	validate     projection_repository.ConversationValidation
	validateErr  error
	state        *projection_model.ConversationBackfill
	stateErr     error
	commits      []projection_repository.ConversationBackfillCounts
	failedCode   string
}

func (s *conversationBackfillStub) ClaimBatch(context.Context, string, int, string, int, time.Time, time.Time) (*projection_repository.ConversationBackfillBatch, error) {
	if s.claimError != nil {
		return nil, s.claimError
	}
	if len(s.batches) == 0 {
		return &projection_repository.ConversationBackfillBatch{AlreadyComplete: true, Complete: true}, nil
	}
	batch := s.batches[0]
	s.batches = s.batches[1:]
	return batch, nil
}

func (s *conversationBackfillStub) AssociateChat(_ context.Context, _, chatID string, _ time.Time) (projection_repository.ConversationAssociationResult, error) {
	return s.associations[chatID], nil
}

func (s *conversationBackfillStub) CommitBatch(_ context.Context, _ string, _ int, _ string, _ *string, counts projection_repository.ConversationBackfillCounts, _ bool, _ time.Time) error {
	s.commits = append(s.commits, counts)
	return nil
}

func (s *conversationBackfillStub) FailBatch(_ context.Context, _ string, _ int, _, code string, _ time.Time) error {
	s.failedCode = code
	return nil
}

func (s *conversationBackfillStub) GetState(context.Context, string) (*projection_model.ConversationBackfill, error) {
	return s.state, s.stateErr
}

func (s *conversationBackfillStub) Validate(context.Context, string) (projection_repository.ConversationValidation, error) {
	return s.validate, s.validateErr
}

func TestConversationReconcilerCompletesBoundedStructuralBackfill(t *testing.T) {
	repository := &conversationBackfillStub{
		batches: []*projection_repository.ConversationBackfillBatch{{
			Items: []projection_repository.ConversationBackfillCandidate{{ChatID: "phone"}, {ChatID: "lid"}}, Complete: true,
		}},
		associations: map[string]projection_repository.ConversationAssociationResult{
			"phone": {Associated: 2, Messages: 3},
			"lid":   {},
		},
		validate: projection_repository.ConversationValidation{UnreadNonAuthoritative: 1},
	}
	reconciler := NewConversationReconciler(repository)
	reconciler.now = func() time.Time { return time.Unix(100, 0).UTC() }
	result, err := reconciler.RunBounded(context.Background(), "instance-a", 100, 10)
	if err != nil || !result.Complete || result.Batches != 1 || result.Scanned != 2 || result.Associated != 2 || result.Messages != 3 {
		t.Fatalf("RunBounded() = %#v, %v", result, err)
	}
	if len(repository.commits) != 1 || repository.commits[0].Scanned != 2 || repository.commits[0].Associated != 2 {
		t.Fatalf("commits = %#v", repository.commits)
	}
	// Structural completion is intentionally independent from authoritative unread.
	if repository.validate.Ready() {
		t.Fatal("non-authoritative unread unexpectedly passed readiness")
	}
}

func TestConversationReconcilerTreatsCompetingLeaseAsNoop(t *testing.T) {
	repository := &conversationBackfillStub{claimError: projection_repository.ErrConversationBackfillLeaseHeld}
	result, err := NewConversationReconciler(repository).RunBounded(context.Background(), "instance-a", 10, 1)
	if err != nil || !result.LeaseHeld || result.Complete || len(repository.commits) != 0 {
		t.Fatalf("RunBounded() = %#v, %v", result, err)
	}
}

func TestConversationReconcilerFailsClosedOnAssociationValidation(t *testing.T) {
	repository := &conversationBackfillStub{
		batches:     []*projection_repository.ConversationBackfillBatch{{Complete: true}},
		validate:    projection_repository.ConversationValidation{MissingChats: 1},
		validateErr: projection_repository.ErrConversationBackfillValidation,
	}
	result, err := NewConversationReconciler(repository).RunBounded(context.Background(), "instance-a", 10, 1)
	if !errors.Is(err, projection_repository.ErrConversationBackfillValidation) || result.Complete || repository.failedCode != "association_validation_failed" || len(repository.commits) != 0 {
		t.Fatalf("RunBounded() = %#v, %v, failed=%q", result, err, repository.failedCode)
	}
}

type contactBackfillStateStub struct {
	state *projection_model.ContactIdentityBackfill
	err   error
}

func (s *contactBackfillStateStub) GetState(context.Context, string) (*projection_model.ContactIdentityBackfill, error) {
	return s.state, s.err
}

func TestCanonicalChatReadinessRequiresBothCheckpointsAndAuthoritativeUnread(t *testing.T) {
	contacts := &contactBackfillStateStub{state: &projection_model.ContactIdentityBackfill{
		Version: ContactIdentityBackfillVersion, Status: projection_model.ContactIdentityBackfillComplete,
	}}
	conversations := &conversationBackfillStub{state: &projection_model.ConversationBackfill{
		Version: ConversationBackfillVersion, Status: projection_model.ConversationBackfillComplete,
	}, validate: projection_repository.ConversationValidation{UnreadNonAuthoritative: 1}}
	readiness := NewCanonicalChatReadiness(contacts, conversations)
	ready, err := readiness.Ready("instance-a")
	if err != nil || ready {
		t.Fatalf("non-authoritative readiness = %t, %v", ready, err)
	}

	conversations.validate = projection_repository.ConversationValidation{}
	ready, err = readiness.Ready("instance-a")
	if err != nil || !ready {
		t.Fatalf("authoritative readiness = %t, %v", ready, err)
	}

	conversations.state.Status = projection_model.ConversationBackfillRunning
	ready, err = readiness.Ready("instance-a")
	if err != nil || ready {
		t.Fatalf("running checkpoint readiness = %t, %v", ready, err)
	}
}

func TestCanonicalChatReadinessTreatsMissingCheckpointAsNotReady(t *testing.T) {
	readiness := NewCanonicalChatReadiness(
		&contactBackfillStateStub{err: gorm.ErrRecordNotFound},
		&conversationBackfillStub{},
	)
	ready, err := readiness.Ready("instance-a")
	if err != nil || ready {
		t.Fatalf("missing checkpoint readiness = %t, %v", ready, err)
	}
}
