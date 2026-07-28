package group_list_repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	group_list_model "github.com/evolution-foundation/evolution-go/pkg/groupList/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrNotFound        = errors.New("group list not found")
	ErrNameConflict    = errors.New("group list name already exists")
	ErrVersionConflict = errors.New("group list version changed concurrently")
	ErrEntryLimit      = errors.New("group list entry limit exceeded")
)

type EntryInput struct {
	GroupJID          string
	GroupNameSnapshot string
}

type CreateInput struct {
	ID                         string
	InstanceID                 string
	Name                       string
	NormalizedName             string
	Description                *string
	AuthorizationSource        string
	AuthorizationReferenceHash string
	AuthorizedAt               time.Time
	ActorReferenceHash         string
	Entries                    []EntryInput
	InstanceJID                string
	GroupJIDs                  []string
	AuditMetadata              json.RawMessage
}

type UpdateInput struct {
	Name                       string
	NormalizedName             string
	Description                *string
	ExpectedVersion            int64
	AuthorizationSource        string
	AuthorizationReferenceHash string
	AuthorizedAt               time.Time
	ActorReferenceHash         string
	Entries                    []EntryInput
	InstanceJID                string
	GroupJIDs                  []string
	AuditMetadata              json.RawMessage
}

type Summary struct {
	group_list_model.GroupList `gorm:"embedded"`
	GroupCount                 int64 `json:"groupCount" gorm:"column:group_count"`
}

type ListCursor struct {
	NormalizedName string
	ID             string
}

type ListPage struct {
	Items      []Summary
	NextCursor *ListCursor
}

type EntryCursor struct{ GroupJID string }

type EntryPage struct {
	Items      []group_list_model.Entry
	NextCursor *EntryCursor
}

type AuditCursor struct {
	OccurredAt time.Time
	ID         string
}

type AuditPage struct {
	Items      []group_list_model.AuditEvent
	NextCursor *AuditCursor
}

type Repository interface {
	Create(context.Context, CreateInput) (*group_list_model.GroupList, error)
	Get(context.Context, string, string) (*Summary, error)
	List(context.Context, string, string, int, *ListCursor) (*ListPage, error)
	ListEntries(context.Context, string, string, int, *EntryCursor) (*EntryPage, error)
	GetEligibilitySnapshot(context.Context, string, string, int) (*Summary, []group_list_model.Entry, error)
	Update(context.Context, string, string, UpdateInput) (*group_list_model.GroupList, error)
	Delete(context.Context, string, string, string) error
	ListAudit(context.Context, string, string, int, *AuditCursor) (*AuditPage, error)
}

func (r *repository) GetEligibilitySnapshot(ctx context.Context, instanceID, groupListID string, maxEntries int) (*Summary, []group_list_model.Entry, error) {
	if err := validateRead(r, ctx, instanceID, groupListID); err != nil || maxEntries < 1 || maxEntries > 10_000 {
		if err != nil {
			return nil, nil, err
		}
		return nil, nil, errors.New("bounded eligibility snapshot is required")
	}
	var summary *Summary
	var entries []group_list_model.Entry
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txRepository := &repository{db: tx, now: r.now}
		current, err := txRepository.Get(ctx, instanceID, groupListID)
		if err != nil {
			return err
		}
		summary = current
		if err := tx.Where("instance_id = ? AND group_list_id = ?", instanceID, groupListID).
			Order("group_jid ASC").Limit(maxEntries + 1).Find(&entries).Error; err != nil {
			return err
		}
		if len(entries) > maxEntries {
			return ErrEntryLimit
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return summary, entries, err
}

type repository struct {
	db          *gorm.DB
	now         func() time.Time
	eligibility MutationEligibilityEvaluator
}

type MutationEligibilityEvaluator func(context.Context, *gorm.DB, string, string, []string) ([]EntryInput, error)

type RepositoryOption func(*repository)

func WithMutationEligibilityEvaluator(evaluator MutationEligibilityEvaluator) RepositoryOption {
	return func(repository *repository) { repository.eligibility = evaluator }
}

func New(db *gorm.DB, options ...RepositoryOption) Repository {
	repository := &repository{db: db, now: time.Now}
	for _, option := range options {
		option(repository)
	}
	return repository
}

func (r *repository) Create(ctx context.Context, input CreateInput) (*group_list_model.GroupList, error) {
	if r == nil || r.db == nil || r.now == nil || ctx == nil || uuid.Validate(input.ID) != nil || uuid.Validate(input.InstanceID) != nil || len(input.Entries) == 0 && len(input.GroupJIDs) == 0 {
		return nil, errors.New("group list repository and identities are required")
	}
	now := r.now().UTC()
	list := &group_list_model.GroupList{
		ID: input.ID, InstanceID: input.InstanceID, Name: input.Name, NormalizedName: input.NormalizedName,
		Description: input.Description, Version: 1, AuthorizationSource: input.AuthorizationSource,
		AuthorizationReferenceHash: input.AuthorizationReferenceHash, AuthorizedAt: input.AuthorizedAt.UTC(),
		CreatedAt: now, UpdatedAt: now,
	}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		entryInputs := input.Entries
		if r.eligibility != nil {
			var err error
			entryInputs, err = r.eligibility(ctx, tx, input.InstanceID, input.InstanceJID, input.GroupJIDs)
			if err != nil {
				return err
			}
		}
		if len(entryInputs) == 0 {
			return errors.New("validated group list entries are required")
		}
		if err := tx.Create(list).Error; err != nil {
			return err
		}
		entries := buildEntries(list, entryInputs, now)
		if err := tx.CreateInBatches(&entries, 500).Error; err != nil {
			return err
		}
		audit := newAudit(list, "created", nil, 1, input.ActorReferenceHash, input.AuditMetadata, now)
		return tx.Create(audit).Error
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if uniqueViolation(err) {
		return nil, ErrNameConflict
	}
	return list, err
}

func (r *repository) Get(ctx context.Context, instanceID, groupListID string) (*Summary, error) {
	if err := validateRead(r, ctx, instanceID, groupListID); err != nil {
		return nil, err
	}
	var summary Summary
	err := r.db.WithContext(ctx).Table("group_lists").
		Select("group_lists.*, COUNT(group_list_entries.group_jid) AS group_count").
		Joins("LEFT JOIN group_list_entries ON group_list_entries.group_list_id = group_lists.id AND group_list_entries.instance_id = group_lists.instance_id").
		Where("group_lists.instance_id = ? AND group_lists.id = ? AND group_lists.deleted_at IS NULL", instanceID, groupListID).
		Group("group_lists.id").Take(&summary).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &summary, err
}

func (r *repository) List(ctx context.Context, instanceID, search string, limit int, cursor *ListCursor) (*ListPage, error) {
	if r == nil || r.db == nil || ctx == nil || uuid.Validate(instanceID) != nil || limit < 1 || limit > 100 {
		return nil, errors.New("bounded group list query is required")
	}
	query := r.db.WithContext(ctx).Table("group_lists").
		Select("group_lists.*, COUNT(group_list_entries.group_jid) AS group_count").
		Joins("LEFT JOIN group_list_entries ON group_list_entries.group_list_id = group_lists.id AND group_list_entries.instance_id = group_lists.instance_id").
		Where("group_lists.instance_id = ? AND group_lists.deleted_at IS NULL", instanceID)
	if search != "" {
		query = query.Where("group_lists.normalized_name LIKE ? ESCAPE E'\\\\'", escapeLike(search)+"%")
	}
	if cursor != nil {
		query = query.Where("(group_lists.normalized_name, group_lists.id) > (?, ?)", cursor.NormalizedName, cursor.ID)
	}
	var items []Summary
	if err := query.Group("group_lists.id").Order("group_lists.normalized_name ASC, group_lists.id ASC").Limit(limit + 1).Scan(&items).Error; err != nil {
		return nil, err
	}
	page := &ListPage{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = &ListCursor{NormalizedName: last.NormalizedName, ID: last.ID}
	}
	return page, nil
}

func (r *repository) ListEntries(ctx context.Context, instanceID, groupListID string, limit int, cursor *EntryCursor) (*EntryPage, error) {
	if err := validateRead(r, ctx, instanceID, groupListID); err != nil || limit < 1 || limit > 100 {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("bounded group list entry query is required")
	}
	query := r.db.WithContext(ctx).Table("group_list_entries").
		Joins("JOIN group_lists ON group_lists.id = group_list_entries.group_list_id AND group_lists.instance_id = group_list_entries.instance_id AND group_lists.deleted_at IS NULL").
		Where("group_list_entries.instance_id = ? AND group_list_entries.group_list_id = ?", instanceID, groupListID)
	if cursor != nil {
		query = query.Where("group_list_entries.group_jid > ?", cursor.GroupJID)
	}
	var items []group_list_model.Entry
	if err := query.Select("group_list_entries.*").Order("group_list_entries.group_jid ASC").Limit(limit + 1).Scan(&items).Error; err != nil {
		return nil, err
	}
	if len(items) == 0 {
		if _, err := r.Get(ctx, instanceID, groupListID); err != nil {
			return nil, err
		}
	}
	page := &EntryPage{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		page.NextCursor = &EntryCursor{GroupJID: page.Items[len(page.Items)-1].GroupJID}
	}
	return page, nil
}

func (r *repository) Update(ctx context.Context, instanceID, groupListID string, input UpdateInput) (*group_list_model.GroupList, error) {
	if err := validateRead(r, ctx, instanceID, groupListID); err != nil || input.ExpectedVersion < 1 || len(input.Entries) == 0 && len(input.GroupJIDs) == 0 {
		if err == nil {
			err = errors.New("non-empty versioned group list update is required")
		}
		return nil, err
	}
	now := r.now().UTC()
	var list group_list_model.GroupList
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("instance_id = ? AND id = ? AND deleted_at IS NULL", instanceID, groupListID).First(&list).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		if list.Version != input.ExpectedVersion {
			return ErrVersionConflict
		}
		entryInputs := input.Entries
		if r.eligibility != nil {
			var validationErr error
			entryInputs, validationErr = r.eligibility(ctx, tx, instanceID, input.InstanceJID, input.GroupJIDs)
			if validationErr != nil {
				return validationErr
			}
		}
		if len(entryInputs) == 0 {
			return errors.New("validated group list entries are required")
		}
		previousVersion := list.Version
		list.Name = input.Name
		list.NormalizedName = input.NormalizedName
		list.Description = input.Description
		list.Version++
		list.AuthorizationSource = input.AuthorizationSource
		list.AuthorizationReferenceHash = input.AuthorizationReferenceHash
		list.AuthorizedAt = input.AuthorizedAt.UTC()
		list.UpdatedAt = now
		result := tx.Model(&group_list_model.GroupList{}).Where("instance_id = ? AND id = ? AND version = ? AND deleted_at IS NULL", instanceID, groupListID, previousVersion).
			Updates(map[string]any{
				"name": list.Name, "normalized_name": list.NormalizedName, "description": list.Description, "version": list.Version,
				"authorization_source": list.AuthorizationSource, "authorization_reference_hash": list.AuthorizationReferenceHash,
				"authorized_at": list.AuthorizedAt, "updated_at": now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrVersionConflict
		}
		groupIDs := make([]string, len(entryInputs))
		for index := range entryInputs {
			groupIDs[index] = entryInputs[index].GroupJID
		}
		if err := tx.Where("instance_id = ? AND group_list_id = ? AND group_jid NOT IN ?", instanceID, groupListID, groupIDs).
			Delete(&group_list_model.Entry{}).Error; err != nil {
			return err
		}
		entries := buildEntries(&list, entryInputs, now)
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "group_list_id"}, {Name: "group_jid"}},
			DoUpdates: clause.AssignmentColumns([]string{"group_name_snapshot"}),
		}).CreateInBatches(&entries, 500).Error; err != nil {
			return err
		}
		audit := newAudit(&list, "updated", &previousVersion, list.Version, input.ActorReferenceHash, input.AuditMetadata, now)
		return tx.Create(audit).Error
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if uniqueViolation(err) {
		return nil, ErrNameConflict
	}
	if serializationFailure(err) {
		return nil, ErrVersionConflict
	}
	return &list, err
}

func (r *repository) Delete(ctx context.Context, instanceID, groupListID, actorReferenceHash string) error {
	if err := validateRead(r, ctx, instanceID, groupListID); err != nil || actorReferenceHash == "" {
		if err != nil {
			return err
		}
		return errors.New("group list deletion actor is required")
	}
	now := r.now().UTC()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var list group_list_model.GroupList
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("instance_id = ? AND id = ? AND deleted_at IS NULL", instanceID, groupListID).First(&list).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		previousVersion := list.Version
		list.Version++
		result := tx.Model(&group_list_model.GroupList{}).Where("instance_id = ? AND id = ? AND version = ? AND deleted_at IS NULL", instanceID, groupListID, previousVersion).
			Updates(map[string]any{"version": list.Version, "deleted_at": now, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrVersionConflict
		}
		metadata := json.RawMessage(`{"entryCountPreserved":true}`)
		return tx.Create(newAudit(&list, "deleted", &previousVersion, list.Version, actorReferenceHash, metadata, now)).Error
	})
}

func (r *repository) ListAudit(ctx context.Context, instanceID, groupListID string, limit int, cursor *AuditCursor) (*AuditPage, error) {
	if r == nil || r.db == nil || ctx == nil || uuid.Validate(instanceID) != nil || uuid.Validate(groupListID) != nil || limit < 1 || limit > 100 {
		return nil, errors.New("bounded group list audit query is required")
	}
	var exists int64
	if err := r.db.WithContext(ctx).Model(&group_list_model.GroupList{}).Where("instance_id = ? AND id = ?", instanceID, groupListID).Count(&exists).Error; err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, ErrNotFound
	}
	query := r.db.WithContext(ctx).Where("instance_id = ? AND group_list_id = ?", instanceID, groupListID)
	if cursor != nil {
		query = query.Where("(occurred_at, id) > (?, ?)", cursor.OccurredAt.UTC(), cursor.ID)
	}
	var items []group_list_model.AuditEvent
	if err := query.Order("occurred_at ASC, id ASC").Limit(limit + 1).Find(&items).Error; err != nil {
		return nil, err
	}
	page := &AuditPage{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = &AuditCursor{OccurredAt: last.OccurredAt, ID: last.ID}
	}
	return page, nil
}

func validateRead(r *repository, ctx context.Context, instanceID, groupListID string) error {
	if r == nil || r.db == nil || r.now == nil || ctx == nil || uuid.Validate(instanceID) != nil || uuid.Validate(groupListID) != nil {
		return errors.New("group list repository and identities are required")
	}
	return nil
}

func buildEntries(list *group_list_model.GroupList, inputs []EntryInput, now time.Time) []group_list_model.Entry {
	entries := make([]group_list_model.Entry, len(inputs))
	for index := range inputs {
		entries[index] = group_list_model.Entry{
			GroupListID: list.ID, InstanceID: list.InstanceID, GroupJID: inputs[index].GroupJID,
			GroupNameSnapshot: inputs[index].GroupNameSnapshot, CreatedAt: now,
		}
	}
	return entries
}

func newAudit(list *group_list_model.GroupList, eventType string, fromVersion *int64, toVersion int64, actorHash string, metadata json.RawMessage, now time.Time) *group_list_model.AuditEvent {
	if len(metadata) == 0 {
		metadata = json.RawMessage(`{}`)
	}
	return &group_list_model.AuditEvent{
		ID: uuid.NewString(), GroupListID: list.ID, InstanceID: list.InstanceID, EventType: eventType,
		ActorType: "instance", ActorReferenceHash: actorHash, FromVersion: fromVersion, ToVersion: toVersion,
		Metadata: metadata, OccurredAt: now,
	}
}

func uniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	type sqlStateError interface{ SQLState() string }
	var state sqlStateError
	return errors.As(err, &state) && state.SQLState() == "23505"
}

func serializationFailure(err error) bool {
	type sqlState interface{ SQLState() string }
	var state sqlState
	return errors.As(err, &state) && state.SQLState() == "40001"
}

func escapeLike(value string) string {
	result := ""
	for _, char := range value {
		if char == '\\' || char == '%' || char == '_' {
			result += "\\"
		}
		result += string(char)
	}
	return result
}
