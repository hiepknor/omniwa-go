package projection_repository

import (
	"context"
	"errors"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrContactIdentityBackfillLeaseHeld = errors.New("contact identity backfill lease is held")
	ErrContactIdentityBackfillLeaseLost = errors.New("contact identity backfill lease was lost")
	ErrContactIdentityValidation        = errors.New("canonical contact identity validation failed")
)

type ContactIdentityCandidate struct {
	ContactID    string
	PreferredJID string
	PhoneJID     *string
	LID          *string
}

type ContactIdentityBackfillBatch struct {
	Items           []ContactIdentityCandidate
	Complete        bool
	AlreadyComplete bool
}

type ContactIdentityBackfillCounts struct {
	Scanned   int64
	Mapped    int64
	Merged    int64
	Unchanged int64
}

type ContactIdentityValidation struct {
	RedirectChains           int64
	ChatsReferencingAbsorbed int64
}

func (v ContactIdentityValidation) Valid() bool {
	return v.RedirectChains == 0 && v.ChatsReferencingAbsorbed == 0
}

type ContactIdentityBackfillRepository interface {
	ClaimBatch(context.Context, string, int, string, int, time.Time, time.Time) (*ContactIdentityBackfillBatch, error)
	CommitBatch(context.Context, string, int, string, *string, ContactIdentityBackfillCounts, bool, time.Time) error
	FailBatch(context.Context, string, int, string, string, time.Time) error
	GetState(context.Context, string) (*projection_model.ContactIdentityBackfill, error)
	Validate(context.Context, string) (ContactIdentityValidation, error)
}

type contactIdentityBackfillRepository struct {
	db *gorm.DB
}

func NewContactIdentityBackfillRepository(db *gorm.DB) ContactIdentityBackfillRepository {
	return &contactIdentityBackfillRepository{db: db}
}

func (r *contactIdentityBackfillRepository) ClaimBatch(
	ctx context.Context,
	instanceID string,
	version int,
	owner string,
	limit int,
	now time.Time,
	leaseUntil time.Time,
) (*ContactIdentityBackfillBatch, error) {
	if r == nil || r.db == nil || ctx == nil || instanceID == "" || version < 1 || owner == "" || limit < 1 ||
		now.IsZero() || !leaseUntil.After(now) {
		return nil, errors.New("valid contact identity backfill claim is required")
	}
	var batch ContactIdentityBackfillBatch
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		initial := projection_model.ContactIdentityBackfill{
			InstanceID: instanceID, Version: version, Status: projection_model.ContactIdentityBackfillPending,
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&initial).Error; err != nil {
			return err
		}
		var state projection_model.ContactIdentityBackfill
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("instance_id = ?", instanceID).First(&state).Error; err != nil {
			return err
		}
		if state.Version != version {
			return errors.New("contact identity backfill version mismatch")
		}
		if state.Status == projection_model.ContactIdentityBackfillComplete {
			batch.Items = make([]ContactIdentityCandidate, 0)
			batch.Complete = true
			batch.AlreadyComplete = true
			return nil
		}
		if state.LeaseOwner != nil && *state.LeaseOwner != owner && state.LeaseExpiresAt != nil && state.LeaseExpiresAt.After(now) {
			return ErrContactIdentityBackfillLeaseHeld
		}
		query := tx.Model(&projection_model.Contact{}).
			Select("contact_id, preferred_jid, phone_jid, lid").
			Where("instance_id = ? AND tombstoned_at IS NULL", instanceID)
		if state.CursorContactID != nil {
			query = query.Where("contact_id > ?", *state.CursorContactID)
		}
		if err := query.Order("contact_id ASC").Limit(limit).Scan(&batch.Items).Error; err != nil {
			return err
		}
		if batch.Items == nil {
			batch.Items = make([]ContactIdentityCandidate, 0)
		}
		batch.Complete = len(batch.Items) < limit
		return tx.Model(&projection_model.ContactIdentityBackfill{}).
			Where("instance_id = ?", instanceID).
			Updates(map[string]any{
				"status": projection_model.ContactIdentityBackfillRunning, "lease_owner": owner,
				"lease_expires_at": leaseUntil.UTC(), "last_error_code": nil, "updated_at": now.UTC(),
			}).Error
	})
	if err != nil {
		return nil, err
	}
	return &batch, nil
}

func (r *contactIdentityBackfillRepository) CommitBatch(
	ctx context.Context,
	instanceID string,
	version int,
	owner string,
	cursor *string,
	counts ContactIdentityBackfillCounts,
	complete bool,
	now time.Time,
) error {
	if r == nil || r.db == nil || ctx == nil || instanceID == "" || version < 1 || owner == "" || now.IsZero() ||
		counts.Scanned < 0 || counts.Mapped < 0 || counts.Merged < 0 || counts.Unchanged < 0 {
		return errors.New("valid contact identity backfill commit is required")
	}
	updates := map[string]any{
		"lease_owner": nil, "lease_expires_at": nil, "last_error_code": nil,
		"scanned_count":   gorm.Expr("scanned_count + ?", counts.Scanned),
		"mapped_count":    gorm.Expr("mapped_count + ?", counts.Mapped),
		"merged_count":    gorm.Expr("merged_count + ?", counts.Merged),
		"unchanged_count": gorm.Expr("unchanged_count + ?", counts.Unchanged), "updated_at": now.UTC(),
	}
	if cursor != nil {
		updates["cursor_contact_id"] = *cursor
	}
	if complete {
		updates["status"] = projection_model.ContactIdentityBackfillComplete
		updates["completed_at"] = now.UTC()
	} else {
		updates["status"] = projection_model.ContactIdentityBackfillPending
	}
	result := r.db.WithContext(ctx).Model(&projection_model.ContactIdentityBackfill{}).
		Where("instance_id = ? AND version = ? AND lease_owner = ?", instanceID, version, owner).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrContactIdentityBackfillLeaseLost
	}
	return nil
}

func (r *contactIdentityBackfillRepository) FailBatch(ctx context.Context, instanceID string, version int, owner, code string, now time.Time) error {
	if r == nil || r.db == nil || ctx == nil || instanceID == "" || version < 1 || owner == "" || code == "" || len(code) > 64 || now.IsZero() {
		return errors.New("valid contact identity backfill failure is required")
	}
	result := r.db.WithContext(ctx).Model(&projection_model.ContactIdentityBackfill{}).
		Where("instance_id = ? AND version = ? AND lease_owner = ?", instanceID, version, owner).
		Updates(map[string]any{
			"status": projection_model.ContactIdentityBackfillFailed, "lease_owner": nil, "lease_expires_at": nil,
			"failure_count": gorm.Expr("failure_count + 1"), "last_error_code": code, "updated_at": now.UTC(),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrContactIdentityBackfillLeaseLost
	}
	return nil
}

func (r *contactIdentityBackfillRepository) GetState(ctx context.Context, instanceID string) (*projection_model.ContactIdentityBackfill, error) {
	if r == nil || r.db == nil || ctx == nil || instanceID == "" {
		return nil, errors.New("contact identity backfill instance is required")
	}
	var state projection_model.ContactIdentityBackfill
	err := r.db.WithContext(ctx).Where("instance_id = ?", instanceID).First(&state).Error
	return &state, err
}

func (r *contactIdentityBackfillRepository) Validate(ctx context.Context, instanceID string) (ContactIdentityValidation, error) {
	if r == nil || r.db == nil || ctx == nil || instanceID == "" {
		return ContactIdentityValidation{}, errors.New("contact identity validation instance is required")
	}
	validation := ContactIdentityValidation{}
	if err := r.db.WithContext(ctx).Table("projected_contact_redirects AS redirects").
		Joins("JOIN projected_contact_redirects AS next_redirect ON next_redirect.instance_id = redirects.instance_id AND next_redirect.absorbed_contact_id = redirects.canonical_contact_id").
		Where("redirects.instance_id = ?", instanceID).Count(&validation.RedirectChains).Error; err != nil {
		return ContactIdentityValidation{}, err
	}
	if err := r.db.WithContext(ctx).Table("projected_chats AS chats").
		Joins("JOIN projected_contact_redirects AS redirects ON redirects.instance_id = chats.instance_id AND redirects.absorbed_contact_id = chats.contact_id").
		Where("chats.instance_id = ?", instanceID).Count(&validation.ChatsReferencingAbsorbed).Error; err != nil {
		return ContactIdentityValidation{}, err
	}
	if !validation.Valid() {
		return validation, ErrContactIdentityValidation
	}
	return validation, nil
}
