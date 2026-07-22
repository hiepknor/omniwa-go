package projection_repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type GroupRepository interface {
	ApplySnapshot(ctx context.Context, group *projection_model.Group, participants []projection_model.GroupParticipant) (bool, error)
	Tombstone(ctx context.Context, instanceID, groupID, eventKey string, occurredAt time.Time) (bool, error)
	Get(ctx context.Context, instanceID, groupID string) (*projection_model.Group, []projection_model.GroupParticipant, error)
}

type groupRepository struct {
	db  *gorm.DB
	now func() time.Time
}

func NewGroupRepository(db *gorm.DB) GroupRepository {
	return &groupRepository{db: db, now: time.Now}
}

func (r *groupRepository) ApplySnapshot(ctx context.Context, group *projection_model.Group, participants []projection_model.GroupParticipant) (bool, error) {
	if err := validateGroupSnapshot(group, participants); err != nil {
		return false, err
	}
	now := r.now().UTC()
	group.SourceOccurredAt = group.SourceOccurredAt.UTC()
	group.LastSyncedAt = now
	group.TombstonedAt = nil
	applied := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Clauses(newerGroupConflict(allGroupSnapshotColumns)).Create(group)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		applied = true
		participantIDs := make([]string, 0, len(participants))
		for index := range participants {
			participant := &participants[index]
			participant.InstanceID = group.InstanceID
			participant.GroupID = group.GroupID
			participant.SourceOccurredAt = group.SourceOccurredAt
			participant.SourceEventKey = group.SourceEventKey
			participant.LastSyncedAt = now
			participant.TombstonedAt = nil
			if err := tx.Clauses(newerParticipantConflict()).Create(participant).Error; err != nil {
				return err
			}
			participantIDs = append(participantIDs, participant.ParticipantID)
		}
		query := tx.Model(&projection_model.GroupParticipant{}).
			Where("instance_id = ? AND group_id = ? AND (source_occurred_at, source_event_key) <= (?, ?) AND tombstoned_at IS NULL", group.InstanceID, group.GroupID, group.SourceOccurredAt, group.SourceEventKey)
		if len(participantIDs) > 0 {
			query = query.Where("participant_id NOT IN ?", participantIDs)
		}
		return query.Updates(map[string]any{
			"tombstoned_at": group.SourceOccurredAt, "source_occurred_at": group.SourceOccurredAt, "source_event_key": group.SourceEventKey,
			"last_synced_at": now, "updated_at": now,
		}).Error
	})
	if err != nil {
		return false, fmt.Errorf("apply group projection snapshot: %w", err)
	}
	return applied, nil
}

func (r *groupRepository) Tombstone(ctx context.Context, instanceID, groupID, eventKey string, occurredAt time.Time) (bool, error) {
	if instanceID == "" || groupID == "" || eventKey == "" || len(eventKey) > 255 || occurredAt.IsZero() {
		return false, errors.New("group identity and occurrence time are required")
	}
	occurredAt = occurredAt.UTC()
	now := r.now().UTC()
	group := &projection_model.Group{
		InstanceID: instanceID, GroupID: groupID, SourceOccurredAt: occurredAt, SourceEventKey: eventKey,
		LastSyncedAt: now, TombstonedAt: &occurredAt,
	}
	applied := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Clauses(newerGroupConflict([]string{"source_occurred_at", "source_event_key", "last_synced_at", "tombstoned_at", "updated_at"})).Create(group)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		applied = true
		return tx.Model(&projection_model.GroupParticipant{}).
			Where("instance_id = ? AND group_id = ? AND (source_occurred_at, source_event_key) <= (?, ?)", instanceID, groupID, occurredAt, eventKey).
			Updates(map[string]any{
				"tombstoned_at": occurredAt, "source_occurred_at": occurredAt, "source_event_key": eventKey,
				"last_synced_at": now, "updated_at": now,
			}).Error
	})
	if err != nil {
		return false, fmt.Errorf("tombstone group projection: %w", err)
	}
	return applied, nil
}

func (r *groupRepository) Get(ctx context.Context, instanceID, groupID string) (*projection_model.Group, []projection_model.GroupParticipant, error) {
	if instanceID == "" || groupID == "" {
		return nil, nil, errors.New("group identity is required")
	}
	var group projection_model.Group
	if err := r.db.WithContext(ctx).
		Where("instance_id = ? AND group_id = ? AND tombstoned_at IS NULL", instanceID, groupID).
		First(&group).Error; err != nil {
		return nil, nil, err
	}
	var participants []projection_model.GroupParticipant
	if err := r.db.WithContext(ctx).
		Where("instance_id = ? AND group_id = ? AND tombstoned_at IS NULL", instanceID, groupID).
		Order("participant_id ASC").Find(&participants).Error; err != nil {
		return nil, nil, err
	}
	return &group, participants, nil
}

var allGroupSnapshotColumns = []string{
	"name", "topic", "owner_jid", "owner_phone_jid", "locked", "announce",
	"ephemeral_enabled", "ephemeral_timer", "join_approval_required", "suspended",
	"participant_version", "addressing_mode", "member_add_mode", "parent_group_id",
	"is_parent", "is_default_subgroup", "invite_link", "invite_link_updated_at",
	"provider_created_at", "source_occurred_at", "source_event_key", "last_synced_at", "tombstoned_at", "updated_at",
}

func newerGroupConflict(columns []string) clause.OnConflict {
	return clause.OnConflict{
		Columns:   []clause.Column{{Name: "instance_id"}, {Name: "group_id"}},
		DoUpdates: clause.AssignmentColumns(columns),
		Where: clause.Where{Exprs: []clause.Expression{
			clause.Expr{SQL: "(projected_groups.source_occurred_at, projected_groups.source_event_key) <= (EXCLUDED.source_occurred_at, EXCLUDED.source_event_key)"},
		}},
	}
}

func newerParticipantConflict() clause.OnConflict {
	return clause.OnConflict{
		Columns: []clause.Column{{Name: "instance_id"}, {Name: "group_id"}, {Name: "participant_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"phone_number_jid", "lid", "display_name", "role", "source_occurred_at", "source_event_key",
			"last_synced_at", "tombstoned_at", "updated_at",
		}),
		Where: clause.Where{Exprs: []clause.Expression{
			clause.Expr{SQL: "(projected_group_participants.source_occurred_at, projected_group_participants.source_event_key) <= (EXCLUDED.source_occurred_at, EXCLUDED.source_event_key)"},
		}},
	}
}

func validateGroupSnapshot(group *projection_model.Group, participants []projection_model.GroupParticipant) error {
	if group == nil || group.InstanceID == "" || group.GroupID == "" || group.SourceEventKey == "" || group.SourceOccurredAt.IsZero() {
		return errors.New("group snapshot identity and occurrence time are required")
	}
	if len(group.GroupID) > 255 || len(group.SourceEventKey) > 255 || (group.EphemeralTimer != nil && *group.EphemeralTimer < 0) {
		return errors.New("group snapshot contains invalid values")
	}
	seen := make(map[string]struct{}, len(participants))
	for _, participant := range participants {
		if participant.ParticipantID == "" || len(participant.ParticipantID) > 255 || !validParticipantRole(participant.Role) {
			return errors.New("group snapshot contains an invalid participant")
		}
		if _, exists := seen[participant.ParticipantID]; exists {
			return errors.New("group snapshot contains duplicate participants")
		}
		seen[participant.ParticipantID] = struct{}{}
	}
	return nil
}

func validParticipantRole(role projection_model.ParticipantRole) bool {
	switch role {
	case projection_model.ParticipantRoleMember, projection_model.ParticipantRoleAdmin, projection_model.ParticipantRoleSuperAdmin:
		return true
	default:
		return false
	}
}
