package projection_repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type GroupRepository interface {
	ApplySnapshot(ctx context.Context, group *projection_model.Group, participants []projection_model.GroupParticipant) (bool, error)
	ApplyPatch(ctx context.Context, patch GroupPatch) (bool, error)
	Tombstone(ctx context.Context, instanceID, groupID, eventKey string, occurredAt time.Time, cause projection_model.GroupTombstoneCause) (bool, error)
	TombstoneMissing(ctx context.Context, instanceID string, activeGroupIDs []string, eventKey string, occurredAt time.Time) (int, error)
	Get(ctx context.Context, instanceID, groupID string) (*projection_model.Group, []projection_model.GroupParticipant, error)
	GetForEligibility(ctx context.Context, instanceID, instanceIdentity string, groupIDs []string) ([]GroupRecord, error)
	List(ctx context.Context, instanceID string) ([]GroupRecord, error)
	Search(ctx context.Context, instanceID, term string, limit int, cursor *GroupCursor) (*GroupPage, error)
	GetInviteLink(ctx context.Context, instanceID, groupID string) (*string, error)
	SearchManagement(ctx context.Context, instanceID, instanceIdentity string, filter GroupManagementFilter, limit int, cursor *GroupCursor) (*GroupManagementPage, error)
	GetManagement(ctx context.Context, instanceID, instanceIdentity, groupID string) (*GroupManagementRecord, error)
	ListManagementMembers(ctx context.Context, instanceID, instanceIdentity, groupID string, filter GroupMemberFilter, limit int, cursor *GroupMemberCursor) (*GroupManagementRecord, *GroupMemberPage, error)
}

type GroupRecord struct {
	Group        projection_model.Group
	Participants []projection_model.GroupParticipant
}

type GroupCursor struct{ GroupID string }

type GroupManagementFilter struct {
	Term            string
	Type            string
	MyRole          string
	SendMode        string
	State           string
	MembershipState string
}

type GroupManagementRecord struct {
	Group            projection_model.Group
	ActorParticipant *projection_model.GroupParticipant
	ActorIsOwner     bool
	OwnerPublicID    *string
	AdminCount       *int64
}

type GroupManagementPage struct {
	Items      []GroupManagementRecord
	NextCursor *GroupCursor
}

type GroupMemberFilter struct {
	Term string
	Role string
}

type GroupMemberCursor struct {
	SortKey  string
	PublicID string
}

type GroupMemberRecord struct {
	Participant projection_model.GroupParticipant
	Role        string
	IsActor     bool
}

type GroupMemberPage struct {
	Items      []GroupMemberRecord
	NextCursor *GroupMemberCursor
}

type GroupPage struct {
	Items      []GroupRecord
	NextCursor *GroupCursor
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
				"last_synced_at": now, "tombstoned_at": occurredAt, "tombstone_cause": projection_model.GroupTombstoneAccessLost, "updated_at": now,
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
	MemberAddMode        *string
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
	group.TombstoneCause = nil
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
			updates["tombstone_cause"] = nil
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
		if patch.MemberAddMode != nil {
			applyField("member_add_mode", "member_add_mode", *patch.MemberAddMode)
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
			updates["tombstone_cause"] = nil
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
				participantGroupUpdates["tombstone_cause"] = nil
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

func (r *groupRepository) Tombstone(ctx context.Context, instanceID, groupID, eventKey string, occurredAt time.Time, cause projection_model.GroupTombstoneCause) (bool, error) {
	if instanceID == "" || groupID == "" || eventKey == "" || len(eventKey) > 255 || occurredAt.IsZero() ||
		(cause != projection_model.GroupTombstoneAccessLost && cause != projection_model.GroupTombstoneDissolved) {
		return false, errors.New("group identity and occurrence time are required")
	}
	occurredAt = occurredAt.UTC()
	now := r.now().UTC()
	group := &projection_model.Group{
		InstanceID: instanceID, GroupID: groupID, SourceOccurredAt: occurredAt, SourceEventKey: eventKey,
		LastSyncedAt: now, TombstonedAt: &occurredAt, TombstoneCause: &cause,
	}
	fieldVersions, err := encodeBaseGroupVersion(occurredAt, eventKey)
	if err != nil {
		return false, err
	}
	group.FieldVersions = fieldVersions
	applied := false
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Clauses(newerGroupConflict([]string{"source_occurred_at", "source_event_key", "field_versions", "last_synced_at", "tombstoned_at", "tombstone_cause", "updated_at"})).Create(group)
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

func (r *groupRepository) GetForEligibility(ctx context.Context, instanceID, instanceIdentity string, groupIDs []string) ([]GroupRecord, error) {
	if r == nil || r.db == nil || ctx == nil || instanceID == "" || instanceIdentity == "" || len(groupIDs) == 0 || len(groupIDs) > 10_000 {
		return nil, errors.New("bounded group eligibility identities are required")
	}
	var groups []projection_model.Group
	if err := r.db.WithContext(ctx).Where("instance_id = ? AND group_id IN ?", instanceID, groupIDs).
		Order("group_id ASC").Find(&groups).Error; err != nil {
		return nil, err
	}
	if len(groups) == 0 {
		return []GroupRecord{}, nil
	}
	var participants []projection_model.GroupParticipant
	if err := r.db.WithContext(ctx).Raw(`
SELECT participant.* FROM projected_group_participants AS participant
WHERE participant.instance_id = ? AND participant.group_id IN ? AND participant.tombstoned_at IS NULL AND participant.participant_id = ?
UNION
SELECT participant.* FROM projected_group_participants AS participant
WHERE participant.instance_id = ? AND participant.group_id IN ? AND participant.tombstoned_at IS NULL AND participant.phone_number_jid = ?
UNION
SELECT participant.* FROM projected_group_participants AS participant
WHERE participant.instance_id = ? AND participant.group_id IN ? AND participant.tombstoned_at IS NULL AND participant.lid = ?
ORDER BY group_id ASC, participant_id ASC`,
		instanceID, groupIDs, instanceIdentity,
		instanceID, groupIDs, instanceIdentity,
		instanceID, groupIDs, instanceIdentity,
	).Scan(&participants).Error; err != nil {
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

func (r *groupRepository) List(ctx context.Context, instanceID string) ([]GroupRecord, error) {
	if instanceID == "" {
		return nil, errors.New("group instance identity is required")
	}
	var groups []projection_model.Group
	if err := r.db.WithContext(ctx).Where("instance_id = ? AND tombstoned_at IS NULL", instanceID).
		Order("name ASC NULLS LAST, group_id ASC").Find(&groups).Error; err != nil {
		return nil, err
	}
	return r.recordsWithParticipants(ctx, instanceID, groups)
}

func (r *groupRepository) Search(ctx context.Context, instanceID, term string, limit int, cursor *GroupCursor) (*GroupPage, error) {
	term = strings.ToLower(strings.TrimSpace(term))
	if r == nil || r.db == nil || ctx == nil || instanceID == "" || len(term) > 128 || limit < 1 || limit > 200 ||
		(cursor != nil && (cursor.GroupID == "" || len(cursor.GroupID) > 255)) {
		return nil, errors.New("valid group search parameters are required")
	}
	query := r.db.WithContext(ctx).Where("instance_id = ? AND tombstoned_at IS NULL", instanceID)
	if term != "" {
		pattern := escapeGroupSearchPattern(term) + "%"
		query = query.Where("(LOWER(group_id) LIKE ? OR LOWER(LEFT(COALESCE(name, ''), 255)) LIKE ?)", pattern, pattern)
	}
	if cursor != nil {
		query = query.Where("group_id > ?", cursor.GroupID)
	}
	var groups []projection_model.Group
	if err := query.Order("group_id ASC").Limit(limit + 1).Find(&groups).Error; err != nil {
		return nil, err
	}
	hasNext := len(groups) > limit
	if hasNext {
		groups = groups[:limit]
	}
	records, err := r.recordsWithParticipants(ctx, instanceID, groups)
	if err != nil {
		return nil, err
	}
	page := &GroupPage{Items: records}
	if hasNext {
		page.NextCursor = &GroupCursor{GroupID: groups[len(groups)-1].GroupID}
	}
	return page, nil
}

func (r *groupRepository) SearchManagement(ctx context.Context, instanceID, instanceIdentity string, filter GroupManagementFilter, limit int, cursor *GroupCursor) (*GroupManagementPage, error) {
	filter.Term = strings.ToLower(strings.TrimSpace(filter.Term))
	if r == nil || r.db == nil || ctx == nil || instanceID == "" || instanceIdentity == "" || utf8.RuneCountInString(filter.Term) > 128 || limit < 1 || limit > 200 ||
		(cursor != nil && (cursor.GroupID == "" || len(cursor.GroupID) > 255)) {
		return nil, errors.New("valid group management search parameters are required")
	}
	aliases, err := r.resolveActorAliases(ctx, instanceID, instanceIdentity)
	if err != nil {
		return nil, err
	}
	query := r.db.WithContext(ctx).Model(&projection_model.Group{}).Where("instance_id = ?", instanceID)
	if filter.Term != "" {
		pattern := escapeGroupSearchPattern(filter.Term) + "%"
		query = query.Where("(LOWER(group_id) LIKE ? OR LOWER(LEFT(COALESCE(name, ''), 255)) LIKE ?)", pattern, pattern)
	}
	query = applyManagementGroupFilters(query, filter, aliases)
	if cursor != nil {
		query = query.Where("group_id > ?", cursor.GroupID)
	}
	var groups []projection_model.Group
	if err := query.Order("group_id ASC").Limit(limit + 1).Find(&groups).Error; err != nil {
		return nil, err
	}
	hasNext := len(groups) > limit
	if hasNext {
		groups = groups[:limit]
	}
	records, err := r.managementRecords(ctx, instanceID, aliases, groups, false)
	if err != nil {
		return nil, err
	}
	page := &GroupManagementPage{Items: records}
	if hasNext {
		page.NextCursor = &GroupCursor{GroupID: groups[len(groups)-1].GroupID}
	}
	return page, nil
}

func (r *groupRepository) GetManagement(ctx context.Context, instanceID, instanceIdentity, groupID string) (*GroupManagementRecord, error) {
	if r == nil || r.db == nil || ctx == nil || instanceID == "" || instanceIdentity == "" || groupID == "" {
		return nil, errors.New("group management identity is required")
	}
	aliases, err := r.resolveActorAliases(ctx, instanceID, instanceIdentity)
	if err != nil {
		return nil, err
	}
	var group projection_model.Group
	if err := r.db.WithContext(ctx).Where("instance_id = ? AND group_id = ?", instanceID, groupID).First(&group).Error; err != nil {
		return nil, err
	}
	records, err := r.managementRecords(ctx, instanceID, aliases, []projection_model.Group{group}, true)
	if err != nil {
		return nil, err
	}
	return &records[0], nil
}

const groupMemberSortSQL = "LOWER(COALESCE(participants.display_name, ''))"

func (r *groupRepository) ListManagementMembers(ctx context.Context, instanceID, instanceIdentity, groupID string, filter GroupMemberFilter, limit int, cursor *GroupMemberCursor) (*GroupManagementRecord, *GroupMemberPage, error) {
	filter.Term = strings.ToLower(strings.TrimSpace(filter.Term))
	if r == nil || r.db == nil || ctx == nil || instanceID == "" || instanceIdentity == "" || groupID == "" ||
		utf8.RuneCountInString(filter.Term) > 128 || limit < 1 || limit > 200 ||
		(cursor != nil && (len(cursor.SortKey) > 512 || cursor.PublicID == "" || len(cursor.PublicID) > 36)) {
		return nil, nil, errors.New("valid group member search parameters are required")
	}
	aliases, err := r.resolveActorAliases(ctx, instanceID, instanceIdentity)
	if err != nil {
		return nil, nil, err
	}
	var group projection_model.Group
	if err := r.db.WithContext(ctx).Where("instance_id = ? AND group_id = ?", instanceID, groupID).First(&group).Error; err != nil {
		return nil, nil, err
	}
	groupRecords, err := r.managementRecords(ctx, instanceID, aliases, []projection_model.Group{group}, true)
	if err != nil {
		return nil, nil, err
	}
	ownerExpression := `COALESCE(
		participants.participant_id IN (groups.owner_jid, groups.owner_phone_jid)
		OR participants.phone_number_jid IN (groups.owner_jid, groups.owner_phone_jid)
		OR participants.lid IN (groups.owner_jid, groups.owner_phone_jid), FALSE)`
	query := r.db.WithContext(ctx).Table("projected_group_participants AS participants").
		Select("participants.*, "+groupMemberSortSQL+" AS sort_key").
		Joins("JOIN projected_groups AS groups ON groups.instance_id = participants.instance_id AND groups.group_id = participants.group_id").
		Where("participants.instance_id = ? AND participants.group_id = ? AND participants.tombstoned_at IS NULL", instanceID, groupID)
	if filter.Term != "" {
		query = query.Where("LOWER(COALESCE(participants.display_name, '')) LIKE ?", escapeGroupSearchPattern(filter.Term)+"%")
	}
	switch filter.Role {
	case "owner":
		query = query.Where(ownerExpression)
	case "superadmin":
		query = query.Where("participants.role = ? AND NOT "+ownerExpression, projection_model.ParticipantRoleSuperAdmin)
	case "admin":
		query = query.Where("participants.role = ? AND NOT "+ownerExpression, projection_model.ParticipantRoleAdmin)
	case "member":
		query = query.Where("participants.role = ? AND NOT "+ownerExpression, projection_model.ParticipantRoleMember)
	}
	if cursor != nil {
		query = query.Where("("+groupMemberSortSQL+" > ? OR ("+groupMemberSortSQL+" = ? AND participants.public_id > ?))", cursor.SortKey, cursor.SortKey, cursor.PublicID)
	}
	type memberSearchRow struct {
		projection_model.GroupParticipant
		SortKey string `gorm:"column:sort_key"`
	}
	var rows []memberSearchRow
	if err := query.Order(groupMemberSortSQL + " ASC, participants.public_id ASC").Limit(limit + 1).Scan(&rows).Error; err != nil {
		return nil, nil, err
	}
	hasNext := len(rows) > limit
	if hasNext {
		rows = rows[:limit]
	}
	page := &GroupMemberPage{Items: make([]GroupMemberRecord, len(rows))}
	for index := range rows {
		role := string(rows[index].Role)
		if memberMatchesOwner(rows[index].GroupParticipant, group) {
			role = "owner"
		} else if role == string(projection_model.ParticipantRoleSuperAdmin) {
			role = "superadmin"
		}
		page.Items[index] = GroupMemberRecord{
			Participant: rows[index].GroupParticipant, Role: role, IsActor: memberMatchesAliases(rows[index].GroupParticipant, aliases),
		}
	}
	if hasNext {
		last := rows[len(rows)-1]
		page.NextCursor = &GroupMemberCursor{SortKey: last.SortKey, PublicID: last.PublicID}
	}
	return &groupRecords[0], page, nil
}

func memberMatchesOwner(participant projection_model.GroupParticipant, group projection_model.Group) bool {
	values := []string{participant.ParticipantID}
	if participant.PhoneNumberJID != nil {
		values = append(values, *participant.PhoneNumberJID)
	}
	if participant.LID != nil {
		values = append(values, *participant.LID)
	}
	for _, value := range values {
		if value != "" && ((group.OwnerJID != nil && *group.OwnerJID == value) || (group.OwnerPhoneJID != nil && *group.OwnerPhoneJID == value)) {
			return true
		}
	}
	return false
}

func memberMatchesAliases(participant projection_model.GroupParticipant, aliases []string) bool {
	for _, alias := range aliases {
		if alias != "" && (participant.ParticipantID == alias || (participant.PhoneNumberJID != nil && *participant.PhoneNumberJID == alias) || (participant.LID != nil && *participant.LID == alias)) {
			return true
		}
	}
	return false
}

func (r *groupRepository) resolveActorAliases(ctx context.Context, instanceID, instanceIdentity string) ([]string, error) {
	aliases := []string{instanceIdentity}
	seen := map[string]struct{}{instanceIdentity: {}}
	add := func(values ...string) {
		for _, value := range values {
			if value == "" || len(aliases) >= 32 {
				continue
			}
			if _, exists := seen[value]; exists {
				continue
			}
			seen[value] = struct{}{}
			aliases = append(aliases, value)
		}
	}
	type participantAliases struct {
		ParticipantID  string  `gorm:"column:participant_id"`
		PhoneNumberJID *string `gorm:"column:phone_number_jid"`
		LID            *string `gorm:"column:lid"`
	}
	for iteration := 0; iteration < 2; iteration++ {
		var rows []participantAliases
		if err := r.db.WithContext(ctx).Table("projected_group_participants").
			Select("DISTINCT participant_id, phone_number_jid, lid").
			Where("instance_id = ? AND tombstoned_at IS NULL", instanceID).
			Where("participant_id IN ? OR phone_number_jid IN ? OR lid IN ?", aliases, aliases, aliases).
			Limit(32).Scan(&rows).Error; err != nil {
			return nil, err
		}
		before := len(aliases)
		for _, row := range rows {
			add(row.ParticipantID)
			if row.PhoneNumberJID != nil {
				add(*row.PhoneNumberJID)
			}
			if row.LID != nil {
				add(*row.LID)
			}
		}
		if len(aliases) == before {
			break
		}
	}
	var contactIDs []string
	if err := r.db.WithContext(ctx).Table("projected_contact_identities").Distinct("contact_id").
		Where("instance_id = ? AND identity_kind IN ? AND identity_value IN ? AND tombstoned_at IS NULL", instanceID,
			[]projection_model.ContactIdentityKind{projection_model.ContactIdentityKindJID, projection_model.ContactIdentityKindPhoneJID, projection_model.ContactIdentityKindLID}, aliases).
		Limit(8).Pluck("contact_id", &contactIDs).Error; err != nil {
		return nil, err
	}
	if len(contactIDs) > 0 {
		var projected []string
		if err := r.db.WithContext(ctx).Table("projected_contact_identities").Select("identity_value").
			Where("instance_id = ? AND contact_id IN ? AND tombstoned_at IS NULL", instanceID, contactIDs).
			Order("identity_value ASC").Limit(32).Pluck("identity_value", &projected).Error; err != nil {
			return nil, err
		}
		add(projected...)
	}
	return aliases, nil
}

func applyManagementGroupFilters(query *gorm.DB, filter GroupManagementFilter, aliases []string) *gorm.DB {
	actorExists := `EXISTS (SELECT 1 FROM projected_group_participants actor
		WHERE actor.instance_id = projected_groups.instance_id AND actor.group_id = projected_groups.group_id
		AND actor.tombstoned_at IS NULL AND (actor.participant_id IN ? OR actor.phone_number_jid IN ? OR actor.lid IN ?))`
	ownerMatches := `(COALESCE(owner_jid IN ?, FALSE) OR COALESCE(owner_phone_jid IN ?, FALSE))`
	switch filter.Type {
	case "community":
		query = query.Where("is_parent = TRUE")
	case "subgroup":
		query = query.Where("is_parent IS DISTINCT FROM TRUE AND parent_group_id IS NOT NULL")
	case "group":
		query = query.Where("is_parent = FALSE AND parent_group_id IS NULL")
	case "unknown":
		query = query.Where("is_parent IS NULL AND parent_group_id IS NULL")
	}
	switch filter.State {
	case "active":
		query = query.Where("tombstoned_at IS NULL AND suspended = FALSE")
	case "suspended":
		query = query.Where("tombstoned_at IS NULL AND suspended = TRUE")
	case "dissolved":
		query = query.Where("tombstoned_at IS NOT NULL AND tombstone_cause = ?", projection_model.GroupTombstoneDissolved)
	case "unavailable":
		query = query.Where("tombstoned_at IS NOT NULL AND tombstone_cause IS DISTINCT FROM ?", projection_model.GroupTombstoneDissolved)
	case "unknown":
		query = query.Where("tombstoned_at IS NULL AND suspended IS NULL")
	}
	switch filter.SendMode {
	case "all_members":
		query = query.Where("announce = FALSE")
	case "admins_only":
		query = query.Where("announce = TRUE")
	case "unknown":
		query = query.Where("announce IS NULL")
	}
	switch filter.MembershipState {
	case "joined":
		query = query.Where("tombstoned_at IS NULL AND (actor_membership_state = ? OR "+ownerMatches+" OR "+actorExists+")", projection_model.GroupActorMembershipJoined, aliases, aliases, aliases, aliases, aliases)
	case "left":
		query = query.Where("actor_membership_state = ?", projection_model.GroupActorMembershipLeft)
	case "removed":
		query = query.Where("actor_membership_state = ?", projection_model.GroupActorMembershipRemoved)
	case "unknown":
		query = query.Where("(actor_membership_state IS NULL OR actor_membership_state = ?) AND (tombstoned_at IS NOT NULL OR (NOT "+ownerMatches+" AND NOT "+actorExists+"))", projection_model.GroupActorMembershipUnknown, aliases, aliases, aliases, aliases, aliases)
	}
	notExplicitlyGone := "COALESCE(actor_membership_state NOT IN ?, TRUE)"
	goneStates := []projection_model.MembershipState{projection_model.GroupActorMembershipLeft, projection_model.GroupActorMembershipRemoved}
	switch filter.MyRole {
	case "owner":
		query = query.Where("tombstoned_at IS NULL AND "+ownerMatches+" AND "+notExplicitlyGone, aliases, aliases, goneStates)
	case "superadmin":
		query = query.Where("tombstoned_at IS NULL AND "+notExplicitlyGone+" AND NOT "+ownerMatches+" AND EXISTS (SELECT 1 FROM projected_group_participants actor_role WHERE actor_role.instance_id = projected_groups.instance_id AND actor_role.group_id = projected_groups.group_id AND actor_role.tombstoned_at IS NULL AND actor_role.role = ? AND (actor_role.participant_id IN ? OR actor_role.phone_number_jid IN ? OR actor_role.lid IN ?))", goneStates, aliases, aliases, projection_model.ParticipantRoleSuperAdmin, aliases, aliases, aliases)
	case "admin", "member":
		role := projection_model.ParticipantRole(filter.MyRole)
		query = query.Where("tombstoned_at IS NULL AND "+notExplicitlyGone+" AND NOT "+ownerMatches+" AND EXISTS (SELECT 1 FROM projected_group_participants actor_role WHERE actor_role.instance_id = projected_groups.instance_id AND actor_role.group_id = projected_groups.group_id AND actor_role.tombstoned_at IS NULL AND actor_role.role = ? AND (actor_role.participant_id IN ? OR actor_role.phone_number_jid IN ? OR actor_role.lid IN ?))", goneStates, aliases, aliases, role, aliases, aliases, aliases)
	case "not_member":
		query = query.Where("actor_membership_state IN ?", []projection_model.MembershipState{projection_model.GroupActorMembershipLeft, projection_model.GroupActorMembershipRemoved})
	case "unknown":
		query = query.Where(notExplicitlyGone+" AND (tombstoned_at IS NOT NULL OR (NOT "+ownerMatches+" AND NOT "+actorExists+"))", goneStates, aliases, aliases, aliases, aliases, aliases)
	}
	return query
}

func (r *groupRepository) managementRecords(ctx context.Context, instanceID string, aliases []string, groups []projection_model.Group, includeDetail bool) ([]GroupManagementRecord, error) {
	if len(groups) == 0 {
		return []GroupManagementRecord{}, nil
	}
	groupIDs := make([]string, len(groups))
	for index := range groups {
		groupIDs[index] = groups[index].GroupID
	}
	var actors []projection_model.GroupParticipant
	if err := r.db.WithContext(ctx).Where("instance_id = ? AND group_id IN ? AND tombstoned_at IS NULL", instanceID, groupIDs).
		Where("participant_id IN ? OR phone_number_jid IN ? OR lid IN ?", aliases, aliases, aliases).
		Order("group_id ASC, role DESC, participant_id ASC").Find(&actors).Error; err != nil {
		return nil, err
	}
	actorByGroup := make(map[string]*projection_model.GroupParticipant, len(actors))
	for index := range actors {
		participant := actors[index]
		current := actorByGroup[participant.GroupID]
		if current == nil || managementRoleRank(participant.Role) > managementRoleRank(current.Role) {
			copy := participant
			actorByGroup[participant.GroupID] = &copy
		}
	}
	records := make([]GroupManagementRecord, len(groups))
	for index := range groups {
		records[index] = GroupManagementRecord{Group: groups[index], ActorParticipant: actorByGroup[groups[index].GroupID], ActorIsOwner: groupOwnerMatchesAliases(groups[index], aliases)}
	}
	if !includeDetail {
		return records, nil
	}
	type countRow struct {
		GroupID string `gorm:"column:group_id"`
		Count   int64  `gorm:"column:count"`
	}
	var counts []countRow
	if err := r.db.WithContext(ctx).Table("projected_group_participants").Select("group_id, COUNT(*) AS count").
		Where("instance_id = ? AND group_id IN ? AND tombstoned_at IS NULL AND role IN ?", instanceID, groupIDs,
			[]projection_model.ParticipantRole{projection_model.ParticipantRoleAdmin, projection_model.ParticipantRoleSuperAdmin}).
		Group("group_id").Scan(&counts).Error; err != nil {
		return nil, err
	}
	for _, row := range counts {
		for index := range records {
			if records[index].Group.GroupID == row.GroupID {
				count := row.Count
				records[index].AdminCount = &count
				break
			}
		}
	}
	for index := range records {
		if records[index].AdminCount == nil {
			count := int64(0)
			records[index].AdminCount = &count
		}
	}
	type ownerRow struct {
		GroupID  string `gorm:"column:group_id"`
		PublicID string `gorm:"column:public_id"`
	}
	var owners []ownerRow
	if err := r.db.WithContext(ctx).Table("projected_group_participants AS participants").
		Select("participants.group_id, participants.public_id").
		Joins("JOIN projected_groups AS groups ON groups.instance_id = participants.instance_id AND groups.group_id = participants.group_id").
		Where("participants.instance_id = ? AND participants.group_id IN ? AND participants.tombstoned_at IS NULL", instanceID, groupIDs).
		Where(`participants.participant_id IN (groups.owner_jid, groups.owner_phone_jid)
			OR participants.phone_number_jid IN (groups.owner_jid, groups.owner_phone_jid)
			OR participants.lid IN (groups.owner_jid, groups.owner_phone_jid)`).Scan(&owners).Error; err != nil {
		return nil, err
	}
	for _, row := range owners {
		for index := range records {
			if records[index].Group.GroupID == row.GroupID && records[index].OwnerPublicID == nil {
				ownerID := row.PublicID
				records[index].OwnerPublicID = &ownerID
				break
			}
		}
	}
	return records, nil
}

func groupOwnerMatchesAliases(group projection_model.Group, aliases []string) bool {
	for _, alias := range aliases {
		if alias != "" && ((group.OwnerJID != nil && *group.OwnerJID == alias) || (group.OwnerPhoneJID != nil && *group.OwnerPhoneJID == alias)) {
			return true
		}
	}
	return false
}

func managementRoleRank(role projection_model.ParticipantRole) int {
	switch role {
	case projection_model.ParticipantRoleSuperAdmin:
		return 3
	case projection_model.ParticipantRoleAdmin:
		return 2
	case projection_model.ParticipantRoleMember:
		return 1
	default:
		return 0
	}
}

func escapeGroupSearchPattern(value string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(value)
}

func (r *groupRepository) recordsWithParticipants(ctx context.Context, instanceID string, groups []projection_model.Group) ([]GroupRecord, error) {
	if len(groups) == 0 {
		return []GroupRecord{}, nil
	}
	groupIDs := make([]string, len(groups))
	for index := range groups {
		groupIDs[index] = groups[index].GroupID
	}
	var participants []projection_model.GroupParticipant
	if err := r.db.WithContext(ctx).Where("instance_id = ? AND group_id IN ? AND tombstoned_at IS NULL", instanceID, groupIDs).
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

func (r *groupRepository) GetInviteLink(ctx context.Context, instanceID, groupID string) (*string, error) {
	if instanceID == "" || groupID == "" {
		return nil, errors.New("group identity is required")
	}
	var group projection_model.Group
	if err := r.db.WithContext(ctx).Select("invite_link").
		Where("instance_id = ? AND group_id = ? AND tombstoned_at IS NULL", instanceID, groupID).First(&group).Error; err != nil {
		return nil, err
	}
	return group.InviteLink, nil
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
		{name: "actor_membership", columns: map[string]any{"actor_membership_state": group.MembershipState, "actor_membership_changed_at": group.MembershipChangedAt}},
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
