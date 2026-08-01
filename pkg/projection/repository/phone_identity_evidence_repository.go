package projection_repository

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	"github.com/google/uuid"
	"go.mau.fi/whatsmeow/types"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrPhoneIdentityEvidenceConflict = errors.New("phone identity evidence conflicts with an existing instance-scoped relation")

const maxPhoneIdentityResolutionBatch = 1000

type PhoneIdentityEvidenceRepository interface {
	Observe(context.Context, projection_model.PhoneIdentityEvidence) (bool, error)
	Resolve(context.Context, string, []string) (map[string]string, error)
	List(context.Context, string) ([]projection_model.PhoneIdentityEvidence, error)
}

type phoneIdentityEvidenceRepository struct{ db *gorm.DB }

func NewPhoneIdentityEvidenceRepository(db *gorm.DB) PhoneIdentityEvidenceRepository {
	return &phoneIdentityEvidenceRepository{db: db}
}

func (r *phoneIdentityEvidenceRepository) Observe(ctx context.Context, candidate projection_model.PhoneIdentityEvidence) (bool, error) {
	if r == nil || r.db == nil || ctx == nil || uuid.Validate(candidate.InstanceID) != nil ||
		!validPhoneEvidenceJID(candidate.PhoneJID, false) || candidate.FirstObservedAt.IsZero() || candidate.LastObservedAt.IsZero() ||
		candidate.LastObservedAt.Before(candidate.FirstObservedAt) ||
		(candidate.EvidenceKind != projection_model.PhoneIdentityEvidenceDirectPhone && candidate.EvidenceKind != projection_model.PhoneIdentityEvidencePairedAlt) {
		return false, errors.New("complete phone identity evidence is required")
	}
	if candidate.LIDJID != nil {
		value := strings.TrimSpace(*candidate.LIDJID)
		if !validPhoneEvidenceJID(value, true) {
			return false, errors.New("phone identity evidence contains an invalid LID")
		}
		candidate.LIDJID = &value
		candidate.EvidenceKind = projection_model.PhoneIdentityEvidencePairedAlt
	}
	candidate.PhoneJID = strings.TrimSpace(candidate.PhoneJID)
	candidate.FirstObservedAt = candidate.FirstObservedAt.UTC()
	candidate.LastObservedAt = candidate.LastObservedAt.UTC()

	created := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		insert := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&candidate)
		if insert.Error != nil {
			return insert.Error
		}
		created = insert.RowsAffected == 1
		query := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("instance_id = ? AND phone_jid = ?", candidate.InstanceID, candidate.PhoneJID)
		if candidate.LIDJID != nil {
			query = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("instance_id = ? AND (phone_jid = ? OR lid_jid = ?)", candidate.InstanceID, candidate.PhoneJID, *candidate.LIDJID)
		}
		var matches []projection_model.PhoneIdentityEvidence
		if err := query.Find(&matches).Error; err != nil {
			return err
		}
		if len(matches) != 1 || matches[0].PhoneJID != candidate.PhoneJID ||
			(matches[0].LIDJID != nil && candidate.LIDJID != nil && *matches[0].LIDJID != *candidate.LIDJID) {
			return ErrPhoneIdentityEvidenceConflict
		}
		current := matches[0]
		updates := map[string]any{"last_observed_at": laterTime(current.LastObservedAt, candidate.LastObservedAt)}
		if candidate.FirstObservedAt.Before(current.FirstObservedAt) {
			updates["first_observed_at"] = candidate.FirstObservedAt
		}
		if current.LIDJID == nil && candidate.LIDJID != nil {
			updates["lid_jid"] = *candidate.LIDJID
			updates["evidence_kind"] = projection_model.PhoneIdentityEvidencePairedAlt
		}
		return tx.Model(&projection_model.PhoneIdentityEvidence{}).
			Where("instance_id = ? AND phone_jid = ?", candidate.InstanceID, candidate.PhoneJID).
			Updates(updates).Error
	})
	return created, err
}

func (r *phoneIdentityEvidenceRepository) Resolve(ctx context.Context, instanceID string, identities []string) (map[string]string, error) {
	if r == nil || r.db == nil || ctx == nil || uuid.Validate(instanceID) != nil || len(identities) > maxPhoneIdentityResolutionBatch {
		return nil, errors.New("bounded instance-scoped phone identity query is required")
	}
	values := normalizedIdentityValues(identities)
	if len(values) == 0 {
		return map[string]string{}, nil
	}
	var rows []projection_model.PhoneIdentityEvidence
	if err := r.db.WithContext(ctx).Where("instance_id = ? AND (phone_jid IN ? OR lid_jid IN ?)", instanceID, values, values).Find(&rows).Error; err != nil {
		return nil, err
	}
	resolved := make(map[string]string, len(rows)*2)
	for _, row := range rows {
		resolved[row.PhoneJID] = row.PhoneJID
		if row.LIDJID != nil {
			resolved[*row.LIDJID] = row.PhoneJID
		}
	}
	return resolved, nil
}

func (r *phoneIdentityEvidenceRepository) List(ctx context.Context, instanceID string) ([]projection_model.PhoneIdentityEvidence, error) {
	if r == nil || r.db == nil || ctx == nil || uuid.Validate(instanceID) != nil {
		return nil, errors.New("instance-scoped phone identity list is required")
	}
	var rows []projection_model.PhoneIdentityEvidence
	err := r.db.WithContext(ctx).Where("instance_id = ?", instanceID).Order("phone_jid ASC").Find(&rows).Error
	return rows, err
}

func normalizedIdentityValues(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func validPhoneEvidenceJID(value string, lid bool) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 255 {
		return false
	}
	jid, err := types.ParseJID(value)
	if err != nil || jid.IsEmpty() || jid.ToNonAD().String() != value {
		return false
	}
	if lid {
		return jid.Server == types.HiddenUserServer || jid.Server == types.HostedLIDServer
	}
	return jid.Server == types.DefaultUserServer || jid.Server == types.LegacyUserServer
}

func laterTime(left, right time.Time) time.Time {
	if right.After(left) {
		return right
	}
	return left
}
