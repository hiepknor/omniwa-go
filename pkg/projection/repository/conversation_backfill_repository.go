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
	ErrConversationBackfillLeaseHeld  = errors.New("canonical conversation backfill lease is held")
	ErrConversationBackfillLeaseLost  = errors.New("canonical conversation backfill lease was lost")
	ErrConversationBackfillValidation = errors.New("canonical conversation association validation failed")
)

type ConversationBackfillCandidate struct {
	ChatID string
}

type ConversationBackfillBatch struct {
	Items           []ConversationBackfillCandidate
	Complete        bool
	AlreadyComplete bool
}

type ConversationBackfillCounts struct {
	Scanned    int64
	Associated int64
	Absorbed   int64
	Messages   int64
	Conflicts  int64
}

type ConversationAssociationResult struct {
	Associated int64
	Absorbed   int64
	Messages   int64
}

type ConversationValidation struct {
	MissingChats                int64
	MissingMessages             int64
	RedirectChains              int64
	OrphanAliases               int64
	ConversationsWithoutAliases int64
	DirectContactMismatches     int64
	UnreadNonAuthoritative      int64
}

func (v ConversationValidation) AssociationsValid() bool {
	return v.MissingChats == 0 && v.MissingMessages == 0 && v.RedirectChains == 0 &&
		v.OrphanAliases == 0 && v.ConversationsWithoutAliases == 0 && v.DirectContactMismatches == 0
}

func (v ConversationValidation) Ready() bool {
	return v.AssociationsValid() && v.UnreadNonAuthoritative == 0
}

type ConversationBackfillRepository interface {
	ClaimBatch(context.Context, string, int, string, int, time.Time, time.Time) (*ConversationBackfillBatch, error)
	AssociateChat(context.Context, string, string, time.Time) (ConversationAssociationResult, error)
	CommitBatch(context.Context, string, int, string, *string, ConversationBackfillCounts, bool, time.Time) error
	FailBatch(context.Context, string, int, string, string, time.Time) error
	GetState(context.Context, string) (*projection_model.ConversationBackfill, error)
	Validate(context.Context, string) (ConversationValidation, error)
}

type conversationBackfillRepository struct {
	db *gorm.DB
}

func NewConversationBackfillRepository(db *gorm.DB) ConversationBackfillRepository {
	return &conversationBackfillRepository{db: db}
}

func (r *conversationBackfillRepository) ClaimBatch(
	ctx context.Context,
	instanceID string,
	version int,
	owner string,
	limit int,
	now time.Time,
	leaseUntil time.Time,
) (*ConversationBackfillBatch, error) {
	if r == nil || r.db == nil || ctx == nil || instanceID == "" || version < 1 || owner == "" || limit < 1 ||
		now.IsZero() || !leaseUntil.After(now) {
		return nil, errors.New("valid canonical conversation backfill claim is required")
	}
	var batch ConversationBackfillBatch
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		initial := projection_model.ConversationBackfill{
			InstanceID: instanceID, Version: version, Status: projection_model.ConversationBackfillPending,
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&initial).Error; err != nil {
			return err
		}
		var state projection_model.ConversationBackfill
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("instance_id = ?", instanceID).First(&state).Error; err != nil {
			return err
		}
		if state.Version != version {
			return errors.New("canonical conversation backfill version mismatch")
		}
		if state.Status == projection_model.ConversationBackfillComplete {
			batch.Items = make([]ConversationBackfillCandidate, 0)
			batch.Complete, batch.AlreadyComplete = true, true
			return nil
		}
		if state.LeaseOwner != nil && *state.LeaseOwner != owner && state.LeaseExpiresAt != nil && state.LeaseExpiresAt.After(now) {
			return ErrConversationBackfillLeaseHeld
		}
		query := tx.Model(&projection_model.Chat{}).Select("chat_id").
			Where("instance_id = ? AND tombstoned_at IS NULL", instanceID)
		if state.CursorChatID != nil {
			query = query.Where("chat_id > ?", *state.CursorChatID)
		}
		if err := query.Order("chat_id ASC").Limit(limit).Scan(&batch.Items).Error; err != nil {
			return err
		}
		if batch.Items == nil {
			batch.Items = make([]ConversationBackfillCandidate, 0)
		}
		batch.Complete = len(batch.Items) < limit
		return tx.Model(&projection_model.ConversationBackfill{}).Where("instance_id = ?", instanceID).
			Updates(map[string]any{
				"status": projection_model.ConversationBackfillRunning, "lease_owner": owner,
				"lease_expires_at": leaseUntil.UTC(), "last_error_code": nil, "updated_at": now.UTC(),
			}).Error
	})
	if err != nil {
		return nil, err
	}
	return &batch, nil
}

func (r *conversationBackfillRepository) AssociateChat(ctx context.Context, instanceID, chatID string, now time.Time) (ConversationAssociationResult, error) {
	if r == nil || r.db == nil || ctx == nil || instanceID == "" || chatID == "" || now.IsZero() {
		return ConversationAssociationResult{}, errors.New("valid canonical conversation chat association is required")
	}
	result := ConversationAssociationResult{}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockProjectionEntity(tx, "chat", instanceID, chatID); err != nil {
			return err
		}
		var chat projection_model.Chat
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("instance_id = ? AND chat_id = ? AND tombstoned_at IS NULL", instanceID, chatID).First(&chat).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		expectedID, _ := deterministicConversationIdentity(&chat)
		chatScope := tx.Model(&projection_model.Chat{}).
			Where("instance_id = ? AND tombstoned_at IS NULL", instanceID)
		if chat.Type == projection_model.ChatTypeDirect && chat.ContactID != nil && *chat.ContactID != "" {
			chatScope = chatScope.Where("contact_id = ? AND chat_type = ?", *chat.ContactID, projection_model.ChatTypeDirect)
		} else {
			chatScope = chatScope.Where("chat_id = ?", chatID)
		}
		if err := chatScope.Where("conversation_id IS DISTINCT FROM ?", expectedID).Count(&result.Associated).Error; err != nil {
			return err
		}
		if err := chatScope.Where("conversation_id IS NOT NULL AND conversation_id IS DISTINCT FROM ?", expectedID).Count(&result.Absorbed).Error; err != nil {
			return err
		}
		messageScope := tx.Table("projected_messages AS messages").
			Joins("JOIN projected_chats AS chats ON chats.instance_id = messages.instance_id AND chats.chat_id = messages.chat_id").
			Where("messages.instance_id = ? AND messages.deleted_at IS NULL AND chats.tombstoned_at IS NULL", instanceID)
		if chat.Type == projection_model.ChatTypeDirect && chat.ContactID != nil && *chat.ContactID != "" {
			messageScope = messageScope.Where("chats.contact_id = ? AND chats.chat_type = ?", *chat.ContactID, projection_model.ChatTypeDirect)
		} else {
			messageScope = messageScope.Where("chats.chat_id = ?", chatID)
		}
		if err := messageScope.Where("messages.conversation_id IS DISTINCT FROM ?", expectedID).Count(&result.Messages).Error; err != nil {
			return err
		}
		return ensureChatConversation(tx, &chat, now.UTC())
	})
	return result, err
}

func (r *conversationBackfillRepository) CommitBatch(
	ctx context.Context,
	instanceID string,
	version int,
	owner string,
	cursor *string,
	counts ConversationBackfillCounts,
	complete bool,
	now time.Time,
) error {
	if r == nil || r.db == nil || ctx == nil || instanceID == "" || version < 1 || owner == "" || now.IsZero() ||
		counts.Scanned < 0 || counts.Associated < 0 || counts.Absorbed < 0 || counts.Messages < 0 || counts.Conflicts < 0 {
		return errors.New("valid canonical conversation backfill commit is required")
	}
	updates := map[string]any{
		"lease_owner": nil, "lease_expires_at": nil, "last_error_code": nil,
		"scanned_count":    gorm.Expr("scanned_count + ?", counts.Scanned),
		"associated_count": gorm.Expr("associated_count + ?", counts.Associated),
		"absorbed_count":   gorm.Expr("absorbed_count + ?", counts.Absorbed),
		"message_count":    gorm.Expr("message_count + ?", counts.Messages),
		"conflict_count":   gorm.Expr("conflict_count + ?", counts.Conflicts),
		"updated_at":       now.UTC(),
	}
	if cursor != nil {
		updates["cursor_chat_id"] = *cursor
	}
	if complete {
		updates["status"] = projection_model.ConversationBackfillComplete
		updates["completed_at"] = now.UTC()
	} else {
		updates["status"] = projection_model.ConversationBackfillPending
	}
	result := r.db.WithContext(ctx).Model(&projection_model.ConversationBackfill{}).
		Where("instance_id = ? AND version = ? AND lease_owner = ?", instanceID, version, owner).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrConversationBackfillLeaseLost
	}
	return nil
}

func (r *conversationBackfillRepository) FailBatch(ctx context.Context, instanceID string, version int, owner, code string, now time.Time) error {
	if r == nil || r.db == nil || ctx == nil || instanceID == "" || version < 1 || owner == "" || code == "" || len(code) > 64 || now.IsZero() {
		return errors.New("valid canonical conversation backfill failure is required")
	}
	updates := map[string]any{
		"status": projection_model.ConversationBackfillFailed, "lease_owner": nil, "lease_expires_at": nil,
		"failure_count": gorm.Expr("failure_count + 1"), "last_error_code": code, "updated_at": now.UTC(),
	}
	if code == "association_validation_failed" {
		updates["conflict_count"] = gorm.Expr("conflict_count + 1")
	}
	result := r.db.WithContext(ctx).Model(&projection_model.ConversationBackfill{}).
		Where("instance_id = ? AND version = ? AND lease_owner = ?", instanceID, version, owner).
		Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrConversationBackfillLeaseLost
	}
	return nil
}

func (r *conversationBackfillRepository) GetState(ctx context.Context, instanceID string) (*projection_model.ConversationBackfill, error) {
	if r == nil || r.db == nil || ctx == nil || instanceID == "" {
		return nil, errors.New("canonical conversation backfill instance is required")
	}
	var state projection_model.ConversationBackfill
	err := r.db.WithContext(ctx).Where("instance_id = ?", instanceID).First(&state).Error
	return &state, err
}

func (r *conversationBackfillRepository) Validate(ctx context.Context, instanceID string) (ConversationValidation, error) {
	if r == nil || r.db == nil || ctx == nil || instanceID == "" {
		return ConversationValidation{}, errors.New("canonical conversation validation instance is required")
	}
	validation := ConversationValidation{}
	queries := []struct {
		destination *int64
		table       string
		alias       string
		joins       []string
		where       string
	}{
		{&validation.MissingChats, "projected_chats AS chats", "chats", []string{
			"LEFT JOIN projected_chat_aliases AS aliases ON aliases.instance_id = chats.instance_id AND aliases.chat_id = chats.chat_id",
		}, "chats.tombstoned_at IS NULL AND (chats.conversation_id IS NULL OR aliases.chat_id IS NULL OR aliases.conversation_id IS DISTINCT FROM chats.conversation_id)"},
		{&validation.MissingMessages, "projected_messages AS messages", "messages", []string{
			"LEFT JOIN projected_chat_aliases AS aliases ON aliases.instance_id = messages.instance_id AND aliases.chat_id = messages.chat_id",
		}, "messages.deleted_at IS NULL AND (messages.conversation_id IS NULL OR aliases.chat_id IS NULL OR aliases.conversation_id IS DISTINCT FROM messages.conversation_id)"},
		{&validation.RedirectChains, "projected_conversation_redirects AS redirects", "redirects", []string{
			"JOIN projected_conversation_redirects AS next_redirect ON next_redirect.instance_id = redirects.instance_id AND next_redirect.absorbed_conversation_id = redirects.canonical_conversation_id",
		}, "TRUE"},
		{&validation.OrphanAliases, "projected_chat_aliases AS aliases", "aliases", []string{
			"LEFT JOIN projected_conversations AS conversations ON conversations.instance_id = aliases.instance_id AND conversations.conversation_id = aliases.conversation_id",
		}, "(conversations.conversation_id IS NULL OR conversations.tombstoned_at IS NOT NULL)"},
		{&validation.ConversationsWithoutAliases, "projected_conversations AS conversations", "conversations", []string{
			"LEFT JOIN projected_chat_aliases AS aliases ON aliases.instance_id = conversations.instance_id AND aliases.conversation_id = conversations.conversation_id",
		}, "conversations.tombstoned_at IS NULL AND aliases.chat_id IS NULL"},
		{&validation.DirectContactMismatches, "projected_conversations AS conversations", "conversations", []string{
			"JOIN projected_chat_aliases AS aliases ON aliases.instance_id = conversations.instance_id AND aliases.conversation_id = conversations.conversation_id",
			"JOIN projected_chats AS chats ON chats.instance_id = aliases.instance_id AND chats.chat_id = aliases.chat_id",
		}, "conversations.tombstoned_at IS NULL AND chats.tombstoned_at IS NULL AND conversations.conversation_type = 'direct' AND conversations.contact_id IS DISTINCT FROM chats.contact_id"},
		{&validation.UnreadNonAuthoritative, "projected_conversations AS conversations", "conversations", nil,
			"conversations.tombstoned_at IS NULL AND conversations.unread_authoritative = FALSE"},
	}
	for _, query := range queries {
		db := r.db.WithContext(ctx).Table(query.table)
		for _, join := range query.joins {
			db = db.Joins(join)
		}
		if err := db.Where(query.alias+".instance_id = ?", instanceID).
			Where(query.where).Count(query.destination).Error; err != nil {
			return ConversationValidation{}, err
		}
	}
	if !validation.AssociationsValid() {
		return validation, ErrConversationBackfillValidation
	}
	return validation, nil
}
