package projection_repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type GroupRepository interface {
	ApplySnapshot(ctx context.Context, group *projection_model.Group, participants []projection_model.GroupParticipant) (bool, error)
	ApplyPatch(ctx context.Context, patch GroupPatch) (bool, error)
	Tombstone(ctx context.Context, instanceID, groupID, eventKey string, occurredAt time.Time) (bool, error)
	TombstoneMissing(ctx context.Context, instanceID string, activeGroupIDs []string, eventKey string, occurredAt time.Time) (int, error)
	Get(ctx context.Context, instanceID, groupID string) (*projection_model.Group, []projection_model.GroupParticipant, error)
	List(ctx context.Context, instanceID string) ([]GroupRecord, error)
}

type GroupRecord struct {
	Group        projection_model.Group
	Participants []projection_model.GroupParticipant
}

func (r *groupRepository) TombstoneMissing(ctx context.Context, instanceID string, activeGroupIDs []string, eventKey string, occurredAt time.Time) (int, error) {
	if instanceID == "" || eventKey == "" || len(eventKey) > 255 || occurredAt.IsZero() {
		return 0, errors.New("reconciliation identity and occurrence time are required")
	}
	occurredAt = occurredAt.UTC()
	now := r.now().UTC()
	fieldVersions, err := encodeBaseGroupVersion(occurredAt, eventKey)
	if err != nil {
		return 0, err
	}
	tombstoned := 0
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := tx.Model(&projection_model.Group{}).Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("instance_id = ? AND tombstoned_at IS NULL AND (source_occurred_at, source_event_key) <= (?, ?)", instanceID, occurredAt, eventKey)
		if len(activeGroupIDs) > 0 {
			query = query.Where("group_id NOT IN ?", activeGroupIDs)
		}
		var groupIDs []string
		if err := query.Pluck("group_id", &groupIDs).Error; err != nil {
			return err
		}
		if len(groupIDs) == 0 {
			return nil
		}
		result := tx.Model(&projection_model.Group{}).
			Where("instance_id = ? AND group_id IN ?", instanceID, groupIDs).
			Updates(map[string]any{
				"source_occurred_at": occurredAt, "source_event_key": eventKey, "field_versions": fieldVersions,
				"last_synced_at": now, "tombstoned_at": occurredAt, "updated_at": now,
			})
		if result.Error != nil {
			return result.Error
		}
		tombstoned = int(result.RowsAffected)
		return tx.Model(&projection_model.GroupParticipant{}).
			Where("instance_id = ? AND group_id IN ? AND (source_occurred_at, source_event_key) <= (?, ?)", instanceID, groupIDs, occurredAt, eventKey).
			Updates(map[string]any{
				"source_occurred_at": occurredAt, "source_event_key": eventKey,
				"last_synced_at": now, "tombstoned_at": occurredAt, "updated_at": now,
			}).Error
	})
	if err != nil {
		return 0, fmt.Errorf("tombstone missing group projections: %w", err)
	}
	return tombstoned, nil
}

type GroupPatch struct {
	InstanceID           string
	GroupID              string
	EventKey             string
	OccurredAt           time.Time
	Name                 *string
	NameSetAt            *time.Time
	NameSetBy            *string
	NameSetByPhone       *string
	Topic                *string
	TopicID              *string
	TopicSetAt           *time.Time
	TopicSetBy           *string
	TopicSetByPhone      *string
	TopicDeleted         *bool
	Locked               *bool
	Announce             *bool
	AnnounceVersion      *string
	EphemeralEnabled     *bool
	EphemeralTimer       *int64
	JoinApprovalRequired *bool
	Suspended            *bool
	ParticipantVersion   *string
	ParentGroupID        *string
	IsDefaultSubgroup    *bool
	InviteLink           *string
	ParticipantChanges   []GroupParticipantPatch
}

type GroupParticipantPatch struct {
	ParticipantID string
	Role          projection_model.ParticipantRole
	Tombstone     bool
}

type groupFieldVersion struct {
	OccurredAt time.Time `json:"occurredAt"`
	EventKey   string    `json:"eventKey"`
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
	incoming := groupFieldVersion{OccurredAt: group.SourceOccurredAt, EventKey: group.SourceEventKey}
	applied := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		placeholder := &projection_model.Group{
			InstanceID: group.InstanceID, GroupID: group.GroupID, SourceOccurredAt: group.SourceOccurredAt,
			SourceEventKey: group.SourceEventKey, FieldVersions: json.RawMessage(`{}`), LastSyncedAt: now,
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(placeholder).Error; err != nil {
			return err
		}
		var stored projection_model.Group
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("instance_id = ? AND group_id = ?", group.InstanceID, group.GroupID).First(&stored).Error; err != nil {
			return err
		}
		versions, err := decodeGroupVersions(stored.FieldVersions)
		if err != nil {
			return err
		}
		if tombstone, exists := versions["_snapshot"]; stored.TombstonedAt != nil && exists && !newerGroupVersion(incoming, tombstone) {
			return nil
		}
		base, hasBase := versions["_snapshot"]
		if hasBase && !newerOrEqualGroupVersion(incoming, base) {
			return nil
		}
		updates := make(map[string]any)
		for _, field := range snapshotGroupFields(group) {
			current, exists := versions[field.name]
			if !exists {
				current, exists = versions["_snapshot"]
			}
			if !exists || newerOrEqualGroupVersion(incoming, current) {
				for column, value := range field.columns {
					updates[column] = value
				}
				delete(versions, field.name)
			}
		}
		if !hasBase || newerOrEqualGroupVersion(incoming, base) {
			versions["_snapshot"] = incoming
		}
		if len(updates) > 0 {
			encoded, err := json.Marshal(versions)
			if err != nil {
				return err
			}
			updates["field_versions"] = encoded
			updates["last_synced_at"] = now
			updates["tombstoned_at"] = nil
			updates["updated_at"] = now
			if newerGroupVersion(incoming, groupFieldVersion{OccurredAt: stored.SourceOccurredAt, EventKey: stored.SourceEventKey}) {
				updates["source_occurred_at"] = group.SourceOccurredAt
				updates["source_event_key"] = group.SourceEventKey
			}
			if err := tx.Model(&projection_model.Group{}).
				Where("instance_id = ? AND group_id = ?", group.InstanceID, group.GroupID).Updates(updates).Error; err != nil {
				return err
			}
			applied = true
		}
		participantIDs := make([]string, 0, len(participants))
		for index := range participants {
			participant := &participants[index]
			participant.InstanceID = group.InstanceID
			participant.GroupID = group.GroupID
			participant.SourceOccurredAt = group.SourceOccurredAt
			participant.SourceEventKey = group.SourceEventKey
			participant.LastSyncedAt = now
			participant.TombstonedAt = nil
			result := tx.Clauses(newerParticipantConflict()).Create(participant)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected > 0 {
				applied = true
			}
			participantIDs = append(participantIDs, participant.ParticipantID)
		}
		query := tx.Model(&projection_model.GroupParticipant{}).
			Where("instance_id = ? AND group_id = ? AND (source_occurred_at, source_event_key) <= (?, ?) AND tombstoned_at IS NULL", group.InstanceID, group.GroupID, group.SourceOccurredAt, group.SourceEventKey)
		if len(participantIDs) > 0 {
			query = query.Where("participant_id NOT IN ?", participantIDs)
		}
		result := query.Updates(map[string]any{
			"tombstoned_at": group.SourceOccurredAt, "source_occurred_at": group.SourceOccurredAt, "source_event_key": group.SourceEventKey,
			"last_synced_at": now, "updated_at": now,
		})
		if result.RowsAffected > 0 {
			applied = true
		}
		if result.Error != nil {
			return result.Error
		}
		var participantCount int64
		if err := tx.Model(&projection_model.GroupParticipant{}).
			Where("instance_id = ? AND group_id = ? AND tombstoned_at IS NULL", group.InstanceID, group.GroupID).
			Count(&participantCount).Error; err != nil {
			return err
		}
		current, exists := versions["participant_count"]
		if !exists {
			current, exists = versions["_snapshot"]
		}
		if !exists || newerOrEqualGroupVersion(incoming, current) {
			versions["participant_count"] = incoming
		}
		encoded, err := json.Marshal(versions)
		if err != nil {
			return err
		}
		return tx.Model(&projection_model.Group{}).
			Where("instance_id = ? AND group_id = ?", group.InstanceID, group.GroupID).
			Updates(map[string]any{"participant_count": int(participantCount), "field_versions": encoded, "last_synced_at": now, "updated_at": now}).Error
	})
	if err != nil {
		return false, fmt.Errorf("apply group projection snapshot: %w", err)
	}
	return applied, nil
}

func (r *groupRepository) ApplyPatch(ctx context.Context, patch GroupPatch) (bool, error) {
	if err := validateGroupPatch(patch); err != nil {
		return false, err
	}
	patch.OccurredAt = patch.OccurredAt.UTC()
	now := r.now().UTC()
	incoming := groupFieldVersion{OccurredAt: patch.OccurredAt, EventKey: patch.EventKey}
	applied := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		placeholder := &projection_model.Group{
			InstanceID: patch.InstanceID, GroupID: patch.GroupID, SourceOccurredAt: patch.OccurredAt,
			SourceEventKey: patch.EventKey, FieldVersions: json.RawMessage(`{}`), LastSyncedAt: now,
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(placeholder).Error; err != nil {
			return err
		}
		var stored projection_model.Group
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("instance_id = ? AND group_id = ?", patch.InstanceID, patch.GroupID).First(&stored).Error; err != nil {
			return err
		}
		versions, err := decodeGroupVersions(stored.FieldVersions)
		if err != nil {
			return err
		}
		if tombstone, exists := versions["_snapshot"]; stored.TombstonedAt != nil && exists && !newerGroupVersion(incoming, tombstone) {
			return nil
		}
		updates := make(map[string]any)
		applyFields := func(field string, columns map[string]any) {
			current, exists := versions[field]
			if !exists {
				current, exists = versions["_snapshot"]
			}
			if !exists || newerGroupVersion(incoming, current) {
				for column, value := range columns {
					updates[column] = value
				}
				versions[field] = incoming
			}
		}
		applyField := func(field, column string, value any) {
			applyFields(field, map[string]any{column: value})
		}
		if patch.Name != nil {
			applyFields("name", map[string]any{
				"name": *patch.Name, "name_set_at": patch.NameSetAt, "name_set_by": patch.NameSetBy,
				"name_set_by_phone": patch.NameSetByPhone,
			})
		}
		if patch.Topic != nil {
			applyFields("topic", map[string]any{
				"topic": *patch.Topic, "topic_id": patch.TopicID, "topic_set_at": patch.TopicSetAt,
				"topic_set_by": patch.TopicSetBy, "topic_set_by_phone": patch.TopicSetByPhone, "topic_deleted": patch.TopicDeleted,
			})
		}
		if patch.Locked != nil {
			applyField("locked", "locked", *patch.Locked)
		}
		if patch.Announce != nil {
			applyFields("announce", map[string]any{"announce": *patch.Announce, "announce_version": patch.AnnounceVersion})
		}
		if patch.EphemeralEnabled != nil {
			applyField("ephemeral_enabled", "ephemeral_enabled", *patch.EphemeralEnabled)
		}
		if patch.EphemeralTimer != nil {
			applyField("ephemeral_timer", "ephemeral_timer", *patch.EphemeralTimer)
		}
		if patch.JoinApprovalRequired != nil {
			applyField("join_approval", "join_approval_required", *patch.JoinApprovalRequired)
		}
		if patch.Suspended != nil {
			applyField("suspended", "suspended", *patch.Suspended)
		}
		if patch.ParticipantVersion != nil {
			applyField("participant_version", "participant_version", *patch.ParticipantVersion)
		}
		if patch.ParentGroupID != nil {
			applyField("parent_group_id", "parent_group_id", *patch.ParentGroupID)
		}
		if patch.IsDefaultSubgroup != nil {
			applyField("is_default_subgroup", "is_default_subgroup", *patch.IsDefaultSubgroup)
		}
		if patch.InviteLink != nil {
			applyField("invite_link", "invite_link", *patch.InviteLink)
			if _, ok := updates["invite_link"]; ok {
				updates["invite_link_updated_at"] = now
			}
		}
		if len(updates) > 0 {
			encoded, err := json.Marshal(versions)
			if err != nil {
				return err
			}
			updates["field_versions"] = encoded
			updates["last_synced_at"] = now
			updates["updated_at"] = now
			updates["tombstoned_at"] = nil
			if newerGroupVersion(incoming, groupFieldVersion{OccurredAt: stored.SourceOccurredAt, EventKey: stored.SourceEventKey}) {
				updates["source_occurred_at"] = patch.OccurredAt
				updates["source_event_key"] = patch.EventKey
			}
			if err := tx.Model(&projection_model.Group{}).
				Where("instance_id = ? AND group_id = ?", patch.InstanceID, patch.GroupID).Updates(updates).Error; err != nil {
				return err
			}
			applied = true
		}
		participantApplied := false
		for _, participant := range patch.ParticipantChanges {
			row := &projection_model.GroupParticipant{
				InstanceID: patch.InstanceID, GroupID: patch.GroupID, ParticipantID: participant.ParticipantID,
				Role: participant.Role, SourceOccurredAt: patch.OccurredAt, SourceEventKey: patch.EventKey, LastSyncedAt: now,
			}
			if participant.Tombstone {
				row.TombstonedAt = &patch.OccurredAt
			}
			result := tx.Clauses(newerParticipantConflict()).Create(row)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected > 0 {
				applied = true
				participantApplied = true
			}
		}
		if participantApplied {
			participantGroupUpdates := map[string]any{
				"last_synced_at": now, "updated_at": now,
			}
			if newerGroupVersion(incoming, groupFieldVersion{OccurredAt: stored.SourceOccurredAt, EventKey: stored.SourceEventKey}) {
				participantGroupUpdates["source_occurred_at"] = patch.OccurredAt
				participantGroupUpdates["source_event_key"] = patch.EventKey
			}
			var participantCount int64
			if err := tx.Model(&projection_model.GroupParticipant{}).
				Where("instance_id = ? AND group_id = ? AND tombstoned_at IS NULL", patch.InstanceID, patch.GroupID).
				Count(&participantCount).Error; err != nil {
				return err
			}
			participantGroupUpdates["participant_count"] = int(participantCount)
			current, exists := versions["participant_count"]
			if !exists {
				current, exists = versions["_snapshot"]
			}
			if !exists || newerGroupVersion(incoming, current) {
				versions["participant_count"] = incoming
			}
			encoded, err := json.Marshal(versions)
			if err != nil {
				return err
			}
			participantGroupUpdates["field_versions"] = encoded
			if stored.TombstonedAt != nil {
				participantGroupUpdates["tombstoned_at"] = nil
			}
			if err := tx.Model(&projection_model.Group{}).
				Where("instance_id = ? AND group_id = ?", patch.InstanceID, patch.GroupID).
				Updates(participantGroupUpdates).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("apply group projection patch: %w", err)
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
	fieldVersions, err := encodeBaseGroupVersion(occurredAt, eventKey)
	if err != nil {
		return false, err
	}
	group.FieldVersions = fieldVersions
	applied := false
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Clauses(newerGroupConflict([]string{"source_occurred_at", "source_event_key", "field_versions", "last_synced_at", "tombstoned_at", "updated_at"})).Create(group)
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

func (r *groupRepository) List(ctx context.Context, instanceID string) ([]GroupRecord, error) {
	if instanceID == "" {
		return nil, errors.New("group instance identity is required")
	}
	var groups []projection_model.Group
	if err := r.db.WithContext(ctx).Where("instance_id = ? AND tombstoned_at IS NULL", instanceID).
		Order("name ASC NULLS LAST, group_id ASC").Find(&groups).Error; err != nil {
		return nil, err
	}
	if len(groups) == 0 {
		return []GroupRecord{}, nil
	}
	var participants []projection_model.GroupParticipant
	if err := r.db.WithContext(ctx).Where("instance_id = ? AND tombstoned_at IS NULL", instanceID).
		Order("group_id ASC, participant_id ASC").Find(&participants).Error; err != nil {
		return nil, err
	}
	byGroup := make(map[string][]projection_model.GroupParticipant, len(groups))
	for _, participant := range participants {
		byGroup[participant.GroupID] = append(byGroup[participant.GroupID], participant)
	}
	records := make([]GroupRecord, len(groups))
	for index := range groups {
		records[index] = GroupRecord{Group: groups[index], Participants: byGroup[groups[index].GroupID]}
	}
	return records, nil
}

type snapshotGroupField struct {
	name    string
	columns map[string]any
}

func snapshotGroupFields(group *projection_model.Group) []snapshotGroupField {
	return []snapshotGroupField{
		{name: "name", columns: map[string]any{
			"name": group.Name, "name_set_at": group.NameSetAt, "name_set_by": group.NameSetBy, "name_set_by_phone": group.NameSetByPhone,
		}},
		{name: "topic", columns: map[string]any{
			"topic": group.Topic, "topic_id": group.TopicID, "topic_set_at": group.TopicSetAt, "topic_set_by": group.TopicSetBy,
			"topic_set_by_phone": group.TopicSetByPhone, "topic_deleted": group.TopicDeleted,
		}},
		{name: "owner", columns: map[string]any{"owner_jid": group.OwnerJID, "owner_phone_jid": group.OwnerPhoneJID}},
		{name: "locked", columns: map[string]any{"locked": group.Locked}},
		{name: "announce", columns: map[string]any{"announce": group.Announce, "announce_version": group.AnnounceVersion}},
		{name: "incognito", columns: map[string]any{"incognito": group.Incognito}},
		{name: "ephemeral_enabled", columns: map[string]any{"ephemeral_enabled": group.EphemeralEnabled}},
		{name: "ephemeral_timer", columns: map[string]any{"ephemeral_timer": group.EphemeralTimer}},
		{name: "join_approval", columns: map[string]any{"join_approval_required": group.JoinApprovalRequired}},
		{name: "suspended", columns: map[string]any{"suspended": group.Suspended}},
		{name: "participant_version", columns: map[string]any{"participant_version": group.ParticipantVersion}},
		{name: "participant_count", columns: map[string]any{"participant_count": group.ParticipantCount}},
		{name: "addressing_mode", columns: map[string]any{"addressing_mode": group.AddressingMode}},
		{name: "member_add_mode", columns: map[string]any{"member_add_mode": group.MemberAddMode}},
		{name: "parent_group_id", columns: map[string]any{"parent_group_id": group.ParentGroupID}},
		{name: "is_parent", columns: map[string]any{"is_parent": group.IsParent}},
		{name: "default_membership_approval_mode", columns: map[string]any{"default_membership_approval_mode": group.DefaultApprovalMode}},
		{name: "is_default_subgroup", columns: map[string]any{"is_default_subgroup": group.IsDefaultSubgroup}},
		{name: "invite_link", columns: map[string]any{"invite_link": group.InviteLink, "invite_link_updated_at": group.InviteLinkUpdatedAt}},
		{name: "provider_created_at", columns: map[string]any{"provider_created_at": group.ProviderCreatedAt}},
		{name: "creator_country_code", columns: map[string]any{"creator_country_code": group.CreatorCountryCode}},
	}
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

func validateGroupPatch(patch GroupPatch) error {
	if patch.InstanceID == "" || patch.GroupID == "" || patch.EventKey == "" || patch.OccurredAt.IsZero() || len(patch.GroupID) > 255 || len(patch.EventKey) > 255 {
		return errors.New("group patch identity and occurrence time are required")
	}
	if patch.EphemeralTimer != nil && *patch.EphemeralTimer < 0 {
		return errors.New("group patch contains an invalid timer")
	}
	seen := make(map[string]struct{}, len(patch.ParticipantChanges))
	for _, participant := range patch.ParticipantChanges {
		if participant.ParticipantID == "" || len(participant.ParticipantID) > 255 || !validParticipantRole(participant.Role) {
			return errors.New("group patch contains an invalid participant")
		}
		if _, exists := seen[participant.ParticipantID]; exists {
			return errors.New("group patch contains duplicate participant changes")
		}
		seen[participant.ParticipantID] = struct{}{}
	}
	return nil
}

func encodeBaseGroupVersion(occurredAt time.Time, eventKey string) (json.RawMessage, error) {
	return json.Marshal(map[string]groupFieldVersion{"_snapshot": {OccurredAt: occurredAt.UTC(), EventKey: eventKey}})
}

func decodeGroupVersions(raw json.RawMessage) (map[string]groupFieldVersion, error) {
	versions := make(map[string]groupFieldVersion)
	if len(raw) == 0 {
		return versions, nil
	}
	if err := json.Unmarshal(raw, &versions); err != nil {
		return nil, errors.New("invalid group field versions")
	}
	return versions, nil
}

func newerGroupVersion(left, right groupFieldVersion) bool {
	return left.OccurredAt.After(right.OccurredAt) || (left.OccurredAt.Equal(right.OccurredAt) && left.EventKey > right.EventKey)
}

func newerOrEqualGroupVersion(left, right groupFieldVersion) bool {
	return left.OccurredAt.After(right.OccurredAt) || (left.OccurredAt.Equal(right.OccurredAt) && left.EventKey >= right.EventKey)
}
