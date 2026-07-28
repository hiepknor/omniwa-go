package group_repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	group_model "github.com/evolution-foundation/evolution-go/pkg/group/model"
	"github.com/google/uuid"
	"go.mau.fi/whatsmeow/types"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrManagementCommandNotFound     = errors.New("group management command not found")
	ErrManagementCommandConflict     = errors.New("group management command state conflict")
	ErrManagementIdempotencyConflict = errors.New("group management idempotency key reused with different input")
	ErrInvalidManagementCommand      = errors.New("invalid group management command")
)

const (
	maxManagementJSONBytes = 64 << 10
	maxManagementPageSize  = 200
)

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type CreateManagementCommandInput struct {
	ID                 string
	InstanceID         string
	GroupJID           string
	CommandType        string
	IdempotencyKeyHash *string
	RequestFingerprint string
	RequestID          *string
	ActorType          string
	ActorReferenceHash string
}

type CompleteManagementCommandInput struct {
	Status       group_model.ManagementCommandStatus
	SafeOutcome  json.RawMessage
	AuditSummary json.RawMessage
}

type ManagementAuditCursor struct {
	OccurredAt time.Time
	ID         string
}

type ManagementAuditPage struct {
	Items      []group_model.ManagementAuditEvent
	NextCursor *ManagementAuditCursor
}

type ManagementCommandRepository interface {
	Create(context.Context, CreateManagementCommandInput) (*group_model.ManagementCommand, bool, error)
	Get(context.Context, string, string) (*group_model.ManagementCommand, error)
	GetByIdempotencyHash(context.Context, string, string) (*group_model.ManagementCommand, error)
	MarkExecuting(context.Context, string, string) (*group_model.ManagementCommand, error)
	Complete(context.Context, string, string, CompleteManagementCommandInput) (*group_model.ManagementCommand, error)
	RecoverStaleExecuting(context.Context, time.Time, int) (int64, error)
	ListAudit(context.Context, string, string, int, *ManagementAuditCursor) (*ManagementAuditPage, error)
}

type managementCommandRepository struct {
	db  *gorm.DB
	now func() time.Time
}

func NewManagementCommandRepository(db *gorm.DB) ManagementCommandRepository {
	return &managementCommandRepository{db: db, now: time.Now}
}

func (r *managementCommandRepository) Create(ctx context.Context, input CreateManagementCommandInput) (*group_model.ManagementCommand, bool, error) {
	if err := validateCreateManagementCommand(r, ctx, input); err != nil {
		return nil, false, err
	}
	now := r.now().UTC()
	command := &group_model.ManagementCommand{
		ID: input.ID, InstanceID: input.InstanceID, GroupJID: input.GroupJID, CommandType: strings.TrimSpace(input.CommandType),
		Status: group_model.ManagementCommandRequested, IdempotencyKeyHash: input.IdempotencyKeyHash,
		RequestFingerprint: input.RequestFingerprint, RequestID: input.RequestID, ActorType: input.ActorType,
		ActorReferenceHash: input.ActorReferenceHash, SafeOutcome: json.RawMessage(`{}`), CreatedAt: now, UpdatedAt: now,
	}
	created := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(command)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			if input.IdempotencyKeyHash == nil {
				return ErrManagementCommandConflict
			}
			var existing group_model.ManagementCommand
			if err := tx.Where("instance_id = ? AND idempotency_key_hash = ?", input.InstanceID, *input.IdempotencyKeyHash).First(&existing).Error; err != nil {
				return err
			}
			if existing.RequestFingerprint != input.RequestFingerprint {
				return ErrManagementIdempotencyConflict
			}
			*command = existing
			return nil
		}
		created = true
		return tx.Create(newManagementAudit(command, string(group_model.ManagementCommandRequested), command.ActorType, command.ActorReferenceHash, now, map[string]any{
			"command": command.CommandType,
		})).Error
	})
	if err != nil {
		return nil, false, mapManagementRepositoryError(err)
	}
	return command, created, nil
}

func (r *managementCommandRepository) Get(ctx context.Context, instanceID, commandID string) (*group_model.ManagementCommand, error) {
	if !validManagementRead(r, ctx, instanceID, commandID) {
		return nil, ErrInvalidManagementCommand
	}
	var command group_model.ManagementCommand
	err := r.db.WithContext(ctx).Where("instance_id = ? AND id = ?", instanceID, commandID).First(&command).Error
	return &command, mapManagementRepositoryError(err)
}

func (r *managementCommandRepository) GetByIdempotencyHash(ctx context.Context, instanceID, hash string) (*group_model.ManagementCommand, error) {
	if r == nil || r.db == nil || ctx == nil || uuid.Validate(instanceID) != nil || !sha256Pattern.MatchString(hash) {
		return nil, ErrInvalidManagementCommand
	}
	var command group_model.ManagementCommand
	err := r.db.WithContext(ctx).Where("instance_id = ? AND idempotency_key_hash = ?", instanceID, hash).First(&command).Error
	return &command, mapManagementRepositoryError(err)
}

func (r *managementCommandRepository) MarkExecuting(ctx context.Context, instanceID, commandID string) (*group_model.ManagementCommand, error) {
	if !validManagementRead(r, ctx, instanceID, commandID) || r.now == nil {
		return nil, ErrInvalidManagementCommand
	}
	var command group_model.ManagementCommand
	now := r.now().UTC()
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("instance_id = ? AND id = ?", instanceID, commandID).First(&command).Error; err != nil {
			return err
		}
		if command.Status != group_model.ManagementCommandRequested {
			return ErrManagementCommandConflict
		}
		command.Status = group_model.ManagementCommandExecuting
		command.ExecutionStartedAt = &now
		command.UpdatedAt = now
		if err := tx.Model(&group_model.ManagementCommand{}).Where("instance_id = ? AND id = ?", instanceID, commandID).Updates(map[string]any{
			"status": command.Status, "execution_started_at": now, "updated_at": now,
		}).Error; err != nil {
			return err
		}
		return tx.Create(newManagementAudit(&command, string(command.Status), command.ActorType, command.ActorReferenceHash, now, map[string]any{
			"command": command.CommandType,
		})).Error
	})
	return &command, mapManagementRepositoryError(err)
}

func (r *managementCommandRepository) Complete(ctx context.Context, instanceID, commandID string, input CompleteManagementCommandInput) (*group_model.ManagementCommand, error) {
	if !validManagementRead(r, ctx, instanceID, commandID) || r.now == nil || !terminalManagementStatus(input.Status) ||
		!validJSONObject(input.SafeOutcome) || !validJSONObject(input.AuditSummary) {
		return nil, ErrInvalidManagementCommand
	}
	var command group_model.ManagementCommand
	now := r.now().UTC()
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("instance_id = ? AND id = ?", instanceID, commandID).First(&command).Error; err != nil {
			return err
		}
		if command.Status != group_model.ManagementCommandRequested && command.Status != group_model.ManagementCommandExecuting {
			return ErrManagementCommandConflict
		}
		command.Status = input.Status
		command.SafeOutcome = append(json.RawMessage(nil), input.SafeOutcome...)
		command.CompletedAt = &now
		command.UpdatedAt = now
		if err := tx.Model(&group_model.ManagementCommand{}).Where("instance_id = ? AND id = ?", instanceID, commandID).Updates(map[string]any{
			"status": command.Status, "safe_outcome": command.SafeOutcome, "completed_at": now, "updated_at": now,
		}).Error; err != nil {
			return err
		}
		return tx.Create(newManagementAudit(&command, string(command.Status), command.ActorType, command.ActorReferenceHash, now, managementAuditSummary(input.AuditSummary))).Error
	})
	return &command, mapManagementRepositoryError(err)
}

func (r *managementCommandRepository) RecoverStaleExecuting(ctx context.Context, cutoff time.Time, limit int) (int64, error) {
	if r == nil || r.db == nil || r.now == nil || ctx == nil || cutoff.IsZero() || limit < 1 || limit > 1000 {
		return 0, ErrInvalidManagementCommand
	}
	now := r.now().UTC()
	var recovered int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var commands []group_model.ManagementCommand
		if err := tx.Raw(`SELECT * FROM group_management_commands
WHERE status = 'executing' AND updated_at < ?
ORDER BY updated_at ASC, id ASC
LIMIT ?
FOR UPDATE SKIP LOCKED`, cutoff.UTC(), limit).Scan(&commands).Error; err != nil {
			return err
		}
		for index := range commands {
			command := &commands[index]
			outcome := json.RawMessage(`{"reason":"recovery_timeout"}`)
			if err := tx.Model(&group_model.ManagementCommand{}).Where("instance_id = ? AND id = ? AND status = ?", command.InstanceID, command.ID, group_model.ManagementCommandExecuting).Updates(map[string]any{
				"status": group_model.ManagementCommandUnknown, "safe_outcome": outcome, "completed_at": now, "updated_at": now,
			}).Error; err != nil {
				return err
			}
			command.Status = group_model.ManagementCommandUnknown
			command.SafeOutcome = outcome
			command.CompletedAt = &now
			command.UpdatedAt = now
			if err := tx.Create(newManagementAudit(command, string(command.Status), "system", systemActorHash(), now, map[string]any{"reason": "recovery_timeout"})).Error; err != nil {
				return err
			}
			recovered++
		}
		return nil
	})
	return recovered, err
}

func (r *managementCommandRepository) ListAudit(ctx context.Context, instanceID, groupJID string, limit int, cursor *ManagementAuditCursor) (*ManagementAuditPage, error) {
	if r == nil || r.db == nil || ctx == nil || uuid.Validate(instanceID) != nil || !canonicalGroupJID(groupJID) || limit < 1 || limit > maxManagementPageSize ||
		(cursor != nil && (cursor.OccurredAt.IsZero() || uuid.Validate(cursor.ID) != nil)) {
		return nil, ErrInvalidManagementCommand
	}
	query := r.db.WithContext(ctx).Where("instance_id = ? AND group_jid = ?", instanceID, groupJID)
	if cursor != nil {
		at := cursor.OccurredAt.UTC()
		query = query.Where("occurred_at < ? OR (occurred_at = ? AND id < ?)", at, at, cursor.ID)
	}
	var events []group_model.ManagementAuditEvent
	if err := query.Order("occurred_at DESC, id DESC").Limit(limit + 1).Find(&events).Error; err != nil {
		return nil, err
	}
	page := &ManagementAuditPage{Items: events}
	if len(events) > limit {
		page.Items = events[:limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = &ManagementAuditCursor{OccurredAt: last.OccurredAt, ID: last.ID}
	}
	return page, nil
}

func validateCreateManagementCommand(r *managementCommandRepository, ctx context.Context, input CreateManagementCommandInput) error {
	if r == nil || r.db == nil || r.now == nil || ctx == nil || uuid.Validate(input.ID) != nil || uuid.Validate(input.InstanceID) != nil ||
		!canonicalGroupJID(input.GroupJID) || strings.TrimSpace(input.CommandType) == "" || len(strings.TrimSpace(input.CommandType)) > 64 ||
		!sha256Pattern.MatchString(input.RequestFingerprint) || !sha256Pattern.MatchString(input.ActorReferenceHash) ||
		(input.ActorType != "instance" && input.ActorType != "system") ||
		(input.IdempotencyKeyHash != nil && !sha256Pattern.MatchString(*input.IdempotencyKeyHash)) ||
		(input.RequestID != nil && len(*input.RequestID) > 255) {
		return ErrInvalidManagementCommand
	}
	return nil
}

func validManagementRead(r *managementCommandRepository, ctx context.Context, instanceID, commandID string) bool {
	return r != nil && r.db != nil && ctx != nil && uuid.Validate(instanceID) == nil && uuid.Validate(commandID) == nil
}

func canonicalGroupJID(value string) bool {
	jid, err := types.ParseJID(value)
	return err == nil && jid.Server == types.GroupServer && jid.String() == value
}

func terminalManagementStatus(status group_model.ManagementCommandStatus) bool {
	return status == group_model.ManagementCommandCompleted || status == group_model.ManagementCommandPartiallyCompleted ||
		status == group_model.ManagementCommandFailed || status == group_model.ManagementCommandUnknown
}

func validJSONObject(value json.RawMessage) bool {
	if len(value) == 0 || len(value) > maxManagementJSONBytes {
		return false
	}
	var object map[string]any
	return json.Unmarshal(value, &object) == nil && object != nil
}

func managementAuditSummary(outcome json.RawMessage) map[string]any {
	var summary map[string]any
	if !validJSONObject(outcome) || json.Unmarshal(outcome, &summary) != nil {
		return map[string]any{}
	}
	return summary
}

func newManagementAudit(command *group_model.ManagementCommand, eventType, actorType, actorHash string, at time.Time, summary map[string]any) *group_model.ManagementAuditEvent {
	encoded, _ := json.Marshal(summary)
	return &group_model.ManagementAuditEvent{
		ID: uuid.NewString(), CommandID: command.ID, InstanceID: command.InstanceID, GroupJID: command.GroupJID,
		EventType: eventType, ActorType: actorType, ActorReferenceHash: actorHash, Summary: encoded, OccurredAt: at,
	}
}

func systemActorHash() string {
	sum := sha256.Sum256([]byte("group-management-system"))
	return hex.EncodeToString(sum[:])
}

func mapManagementRepositoryError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrManagementCommandNotFound
	}
	return err
}
