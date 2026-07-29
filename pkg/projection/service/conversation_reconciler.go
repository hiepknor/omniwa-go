package projection_service

import (
	"context"
	"errors"
	"fmt"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	ConversationBackfillVersion = 1
	conversationBackfillLease   = 2 * time.Minute
)

type ConversationBackfillResult struct {
	Batches    int
	Scanned    int64
	Associated int64
	Absorbed   int64
	Messages   int64
	Conflicts  int64
	Complete   bool
	LeaseHeld  bool
}

type ConversationReconciler struct {
	backfill projection_repository.ConversationBackfillRepository
	unread   interface {
		ReconcileUnreadSnapshots(context.Context, string) error
	}
	now func() time.Time
}

type contactBackfillStateReader interface {
	GetState(context.Context, string) (*projection_model.ContactIdentityBackfill, error)
}

type conversationBackfillStateReader interface {
	GetState(context.Context, string) (*projection_model.ConversationBackfill, error)
	Validate(context.Context, string) (projection_repository.ConversationValidation, error)
}

type CanonicalChatReadiness struct {
	contacts      contactBackfillStateReader
	conversations conversationBackfillStateReader
}

func NewCanonicalChatReadiness(contacts contactBackfillStateReader, conversations conversationBackfillStateReader) *CanonicalChatReadiness {
	return &CanonicalChatReadiness{contacts: contacts, conversations: conversations}
}

func (r *CanonicalChatReadiness) Ready(instanceID string) (bool, error) {
	if r == nil || r.contacts == nil || r.conversations == nil || instanceID == "" {
		return false, errors.New("canonical chat readiness dependencies and instance are required")
	}
	contactState, err := r.contacts.GetState(context.Background(), instanceID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil || contactState == nil || contactState.Version != ContactIdentityBackfillVersion ||
		contactState.Status != projection_model.ContactIdentityBackfillComplete {
		return false, err
	}
	conversationState, err := r.conversations.GetState(context.Background(), instanceID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil || conversationState == nil || conversationState.Version != ConversationBackfillVersion ||
		conversationState.Status != projection_model.ConversationBackfillComplete {
		return false, err
	}
	validation, err := r.conversations.Validate(context.Background(), instanceID)
	return err == nil && validation.Ready(), err
}

func NewConversationReconciler(backfill projection_repository.ConversationBackfillRepository) *ConversationReconciler {
	return &ConversationReconciler{backfill: backfill, now: time.Now}
}

// WithUnreadSnapshots adds an idempotent recovery pass for instances whose
// structural backfill completed before all history sync chunks were reconciled.
func (r *ConversationReconciler) WithUnreadSnapshots(repository interface {
	ReconcileUnreadSnapshots(context.Context, string) error
}) *ConversationReconciler {
	r.unread = repository
	return r
}

func (r *ConversationReconciler) reconcileUnread(ctx context.Context, instanceID string) error {
	if r.unread == nil {
		return nil
	}
	return r.unread.ReconcileUnreadSnapshots(ctx, instanceID)
}

func (r *ConversationReconciler) RunBounded(
	ctx context.Context,
	instanceID string,
	batchSize int,
	maxBatches int,
) (ConversationBackfillResult, error) {
	if r == nil || r.backfill == nil || r.now == nil || ctx == nil || instanceID == "" || batchSize < 1 || maxBatches < 1 {
		return ConversationBackfillResult{}, errors.New("canonical conversation reconciliation dependencies and bounds are required")
	}
	owner := uuid.NewString()
	result := ConversationBackfillResult{}
	for result.Batches < maxBatches {
		now := r.now().UTC()
		batch, err := r.backfill.ClaimBatch(ctx, instanceID, ConversationBackfillVersion, owner, batchSize, now, now.Add(conversationBackfillLease))
		if errors.Is(err, projection_repository.ErrConversationBackfillLeaseHeld) {
			result.LeaseHeld = true
			return result, nil
		}
		if err != nil {
			return result, err
		}
		if batch.AlreadyComplete {
			if err := r.reconcileUnread(ctx, instanceID); err != nil {
				return result, fmt.Errorf("reconcile canonical conversation unread snapshots: %w", err)
			}
			result.Complete = true
			return result, nil
		}
		result.Batches++
		counts := projection_repository.ConversationBackfillCounts{}
		var cursor *string
		for _, candidate := range batch.Items {
			counts.Scanned++
			association, associateErr := r.backfill.AssociateChat(ctx, instanceID, candidate.ChatID, r.now().UTC())
			if associateErr != nil {
				_ = r.backfill.FailBatch(ctx, instanceID, ConversationBackfillVersion, owner, "association_write_failed", r.now().UTC())
				return result, fmt.Errorf("associate canonical conversation chat %q: %w", candidate.ChatID, associateErr)
			}
			counts.Associated += association.Associated
			counts.Absorbed += association.Absorbed
			counts.Messages += association.Messages
			value := candidate.ChatID
			cursor = &value
		}
		if batch.Complete {
			if _, err := r.backfill.Validate(ctx, instanceID); err != nil {
				counts.Conflicts++
				_ = r.backfill.FailBatch(ctx, instanceID, ConversationBackfillVersion, owner, "association_validation_failed", r.now().UTC())
				return result, fmt.Errorf("validate canonical conversation associations: %w", err)
			}
		}
		if err := r.backfill.CommitBatch(ctx, instanceID, ConversationBackfillVersion, owner, cursor, counts, batch.Complete, r.now().UTC()); err != nil {
			return result, err
		}
		result.Scanned += counts.Scanned
		result.Associated += counts.Associated
		result.Absorbed += counts.Absorbed
		result.Messages += counts.Messages
		result.Conflicts += counts.Conflicts
		if batch.Complete {
			if err := r.reconcileUnread(ctx, instanceID); err != nil {
				return result, fmt.Errorf("reconcile canonical conversation unread snapshots: %w", err)
			}
			result.Complete = true
			return result, nil
		}
	}
	return result, nil
}
