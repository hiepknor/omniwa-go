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
	restarts     int
}

type unreadSnapshotStub struct {
	instances []string
	err       error
}

func (s *unreadSnapshotStub) ReconcileUnreadSnapshots(_ context.Context, instanceID string) error {
	s.instances = append(s.instances, instanceID)
	return s.err
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

func (s *conversationBackfillStub) RestartCompleted(context.Context, string, int, time.Time) (bool, error) {
	s.restarts++
	return true, nil
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

func TestConversationReconcilerRefreshesAndEnrichesBeforeAssociation(t *testing.T) {
	repository := &conversationBackfillStub{
		batches: []*projection_repository.ConversationBackfillBatch{{
			Items: []projection_repository.ConversationBackfillCandidate{{ChatID: "15550001@s.whatsapp.net"}}, Complete: true,
		}},
		associations: map[string]projection_repository.ConversationAssociationResult{"15550001@s.whatsapp.net": {Associated: 2, Absorbed: 1}},
	}
	enriched := make([]string, 0, 1)
	result, err := NewConversationReconciler(repository).RefreshBounded(context.Background(), "instance-a", 10, 1,
		func(_ context.Context, instanceID, chatID string) error {
			enriched = append(enriched, instanceID+":"+chatID)
			return nil
		})
	if err != nil || !result.Complete || result.Absorbed != 1 || repository.restarts != 1 || len(enriched) != 1 {
		t.Fatalf("RefreshBounded() = %#v, %v, restarts=%d enriched=%#v", result, err, repository.restarts, enriched)
	}
}

func TestConversationReconcilerRecoversUnreadAfterCompletedBackfill(t *testing.T) {
	repository := &conversationBackfillStub{}
	unread := &unreadSnapshotStub{}
	result, err := NewConversationReconciler(repository).WithUnreadSnapshots(unread).
		RunBounded(context.Background(), "instance-a", 10, 1)
	if err != nil || !result.Complete || len(unread.instances) != 1 || unread.instances[0] != "instance-a" {
		t.Fatalf("RunBounded() = %#v, %v, unread=%#v", result, err, unread.instances)
	}

	unread.err = errors.New("snapshot unavailable")
	result, err = NewConversationReconciler(repository).WithUnreadSnapshots(unread).
		RunBounded(context.Background(), "instance-a", 10, 1)
	if err == nil || result.Complete {
		t.Fatalf("snapshot failure = %#v, %v", result, err)
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

func TestCanonicalConversationReadinessSeparatesIdentityFromAuthoritativeUnread(t *testing.T) {
	contacts := &contactBackfillStateStub{state: &projection_model.ContactIdentityBackfill{
		Version: ContactIdentityBackfillVersion, Status: projection_model.ContactIdentityBackfillComplete,
	}}
	conversations := &conversationBackfillStub{state: &projection_model.ConversationBackfill{
		Version: ConversationBackfillVersion, Status: projection_model.ConversationBackfillComplete,
	}, validate: projection_repository.ConversationValidation{UnreadNonAuthoritative: 1}}
	readiness := NewCanonicalConversationReadiness(contacts, conversations)
	ready, err := readiness.Ready("instance-a")
	if err != nil || !ready {
		t.Fatalf("identity readiness = %t, %v", ready, err)
	}
	unreadReady, err := readiness.UnreadReady("instance-a")
	if err != nil || unreadReady {
		t.Fatalf("non-authoritative unread readiness = %t, %v", unreadReady, err)
	}

	conversations.validate = projection_repository.ConversationValidation{}
	ready, err = readiness.Ready("instance-a")
	if err != nil || !ready {
		t.Fatalf("authoritative readiness = %t, %v", ready, err)
	}
	unreadReady, err = readiness.UnreadReady("instance-a")
	if err != nil || !unreadReady {
		t.Fatalf("authoritative unread readiness = %t, %v", unreadReady, err)
	}

	conversations.state.Status = projection_model.ConversationBackfillRunning
	ready, err = readiness.Ready("instance-a")
	if err != nil || ready {
		t.Fatalf("running checkpoint readiness = %t, %v", ready, err)
	}
}

func TestCanonicalConversationReadinessTreatsMissingCheckpointAsNotReady(t *testing.T) {
	readiness := NewCanonicalConversationReadiness(
		&contactBackfillStateStub{err: gorm.ErrRecordNotFound},
		&conversationBackfillStub{},
	)
	ready, err := readiness.Ready("instance-a")
	if err != nil || ready {
		t.Fatalf("missing checkpoint readiness = %t, %v", ready, err)
	}
}
