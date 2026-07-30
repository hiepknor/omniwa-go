package projection_repository

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const maxProjectionPageSize = 200

type ChatAspect string

const (
	ChatAspectIdentity       ChatAspect = "identity"
	ChatAspectActivity       ChatAspect = "activity"
	ChatAspectSettings       ChatAspect = "settings"
	ChatAspectArchived       ChatAspect = "archived"
	ChatAspectPinned         ChatAspect = "pinned"
	ChatAspectMuted          ChatAspect = "muted"
	ChatAspectUnreadSnapshot ChatAspect = "unreadSnapshot"
	ChatAspectDeletion       ChatAspect = "deletion"
)

type MessageAspect string

const (
	MessageAspectEnvelope  MessageAspect = "envelope"
	MessageAspectContent   MessageAspect = "content"
	MessageAspectMedia     MessageAspect = "media"
	MessageAspectLifecycle MessageAspect = "lifecycle"
	MessageAspectRetention MessageAspect = "retention"
	MessageAspectUnread    MessageAspect = "unread"
)

type ChatCursor struct {
	LastActivityAt *time.Time
	ChatID         string
}

type MessageCursor struct {
	ProviderTimestamp time.Time
	MessageID         string
}

type ChatPage struct {
	Items      []projection_model.Chat
	NextCursor *ChatCursor
	Total      int64
}

type MessagePage struct {
	Items      []projection_model.ProjectedMessage
	NextCursor *MessageCursor
}

type ConversationCursor struct {
	LastActivityAt *time.Time
	ConversationID string
}

type ConversationRecord struct {
	Conversation projection_model.Conversation
	Aliases      []projection_model.ChatAlias
}

type ConversationPage struct {
	Items      []ConversationRecord
	NextCursor *ConversationCursor
	Total      int64
}

type ChatMessageRepository interface {
	ApplyChat(context.Context, *projection_model.Chat, ...ChatAspect) (bool, error)
	ApplyMessage(context.Context, *projection_model.ProjectedMessage, ...MessageAspect) (bool, error)
	ApplyReceipt(context.Context, *projection_model.MessageReceipt) (bool, error)
	MarkMessageRead(context.Context, string, string, time.Time) (bool, error)
	ReconcileUnreadSnapshots(context.Context, string) error
	GetChat(context.Context, string, string) (*projection_model.Chat, error)
	ListChats(context.Context, string, int, *ChatCursor) (*ChatPage, error)
	GetMessage(context.Context, string, string) (*projection_model.ProjectedMessage, error)
	ListMessages(context.Context, string, string, int, *MessageCursor) (*MessagePage, error)
	ListReceipts(context.Context, string, string) ([]projection_model.MessageReceipt, error)
	GetConversation(context.Context, string, string) (*ConversationRecord, error)
	ListConversations(context.Context, string, int, *ConversationCursor) (*ConversationPage, error)
	ListConversationMessages(context.Context, string, string, int, *MessageCursor) (*MessagePage, error)
}

type chatMessageRepository struct {
	db  *gorm.DB
	now func() time.Time
}

type projectionFieldVersion struct {
	OccurredAt time.Time `json:"occurredAt"`
	EventKey   string    `json:"eventKey"`
}

func NewChatMessageRepository(db *gorm.DB) ChatMessageRepository {
	return &chatMessageRepository{db: db, now: time.Now}
}

func (r *chatMessageRepository) ApplyChat(ctx context.Context, incoming *projection_model.Chat, aspects ...ChatAspect) (bool, error) {
	if err := validateChatApply(incoming, aspects); err != nil {
		return false, err
	}
	incoming.SourceOccurredAt = incoming.SourceOccurredAt.UTC()
	now := r.now().UTC()
	applied := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockProjectionEntity(tx, "chat", incoming.InstanceID, incoming.ChatID); err != nil {
			return err
		}
		var stored projection_model.Chat
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("instance_id = ? AND chat_id = ?", incoming.InstanceID, incoming.ChatID).First(&stored).Error
		created := errors.Is(err, gorm.ErrRecordNotFound)
		if err != nil && !created {
			return err
		}
		if created {
			if !containsChatAspect(aspects, ChatAspectIdentity) {
				return errors.New("chat identity aspect is required for creation")
			}
			stored = projection_model.Chat{
				InstanceID: incoming.InstanceID, ChatID: incoming.ChatID,
				FieldVersions: json.RawMessage(`{}`),
			}
		}
		if containsChatAspect(aspects, ChatAspectIdentity) {
			if err := resolveChatIdentity(tx, incoming, &stored, created, now); err != nil {
				return err
			}
		}
		versions, err := decodeProjectionVersions(stored.FieldVersions)
		if err != nil {
			return fmt.Errorf("decode chat field versions: %w", err)
		}
		version := projectionFieldVersion{OccurredAt: incoming.SourceOccurredAt, EventKey: incoming.SourceEventKey}
		for _, aspect := range aspects {
			current, exists := versions[string(aspect)]
			if exists && !projectionVersionLess(current, version) {
				continue
			}
			applyChatAspect(&stored, incoming, aspect)
			versions[string(aspect)] = version
			applied = true
		}
		if !applied {
			return ensureChatConversation(tx, &stored, now)
		}
		if projectionVersionLess(projectionFieldVersion{OccurredAt: stored.SourceOccurredAt, EventKey: stored.SourceEventKey}, version) {
			stored.SourceOccurredAt = incoming.SourceOccurredAt
			stored.SourceEventKey = incoming.SourceEventKey
		}
		stored.LastSyncedAt = now
		stored.FieldVersions, err = json.Marshal(versions)
		if err != nil {
			return fmt.Errorf("encode chat field versions: %w", err)
		}
		if created {
			if err := tx.Create(&stored).Error; err != nil {
				return err
			}
		} else if err := tx.Save(&stored).Error; err != nil {
			return err
		}
		return ensureChatConversation(tx, &stored, now)
	})
	if err != nil {
		return false, fmt.Errorf("apply chat projection: %w", err)
	}
	return applied, nil
}

func resolveChatIdentity(tx *gorm.DB, incoming, stored *projection_model.Chat, created bool, now time.Time) error {
	if incoming.Type == projection_model.ChatTypeDirect {
		var contact projection_model.Contact
		err := tx.Table("projected_contacts AS contacts").Select("contacts.*").
			Joins("JOIN projected_contact_identities AS identities ON identities.instance_id = contacts.instance_id AND identities.contact_id = contacts.contact_id").
			Where("identities.instance_id = ? AND identities.identity_kind = ? AND identities.identity_value = ?", incoming.InstanceID, projection_model.ContactIdentityKindJID, incoming.ChatID).
			Where("contacts.tombstoned_at IS NULL AND identities.tombstoned_at IS NULL").First(&contact).Error
		switch {
		case err == nil:
			incoming.ContactID = &contact.ContactID
			if name, source := contact.CanonicalDisplayName(); name != "" {
				incoming.DisplayName, incoming.DisplayNameSource = &name, &source
				updatedAt := contact.UpdatedAt.UTC()
				incoming.DisplayNameUpdatedAt = &updatedAt
			} else if incoming.DisplayName != nil && *incoming.DisplayName != "" {
				source, updatedAt := "provider_chat", now
				incoming.DisplayNameSource, incoming.DisplayNameUpdatedAt = &source, &updatedAt
			} else if !created && stored.DisplayNameSource != nil && *stored.DisplayNameSource == "provider_chat" {
				incoming.DisplayName, incoming.DisplayNameSource = stored.DisplayName, stored.DisplayNameSource
				incoming.DisplayNameUpdatedAt = stored.DisplayNameUpdatedAt
			} else {
				incoming.DisplayName, incoming.DisplayNameSource, incoming.DisplayNameUpdatedAt = nil, nil, nil
			}
			return nil
		case !errors.Is(err, gorm.ErrRecordNotFound):
			return err
		}
		if incoming.DisplayName != nil && *incoming.DisplayName != "" {
			source, updatedAt := "provider_chat", now
			incoming.DisplayNameSource, incoming.DisplayNameUpdatedAt = &source, &updatedAt
		} else if !created {
			incoming.ContactID = stored.ContactID
			incoming.DisplayName, incoming.DisplayNameSource = stored.DisplayName, stored.DisplayNameSource
			incoming.DisplayNameUpdatedAt = stored.DisplayNameUpdatedAt
		}
		return nil
	}

	incoming.ContactID = nil
	if incoming.DisplayName == nil || *incoming.DisplayName == "" {
		if !created && stored.Type == incoming.Type {
			incoming.DisplayName, incoming.DisplayNameSource = stored.DisplayName, stored.DisplayNameSource
			incoming.DisplayNameUpdatedAt = stored.DisplayNameUpdatedAt
		}
		return nil
	}
	source := map[projection_model.ChatType]string{
		projection_model.ChatTypeGroup:      "group_subject",
		projection_model.ChatTypeNewsletter: "newsletter_name",
		projection_model.ChatTypeBroadcast:  "broadcast_name",
	}[incoming.Type]
	if source == "" {
		source = "provider_chat"
	}
	incoming.DisplayNameSource, incoming.DisplayNameUpdatedAt = &source, &now
	return nil
}

func (r *chatMessageRepository) ApplyMessage(ctx context.Context, incoming *projection_model.ProjectedMessage, aspects ...MessageAspect) (bool, error) {
	if err := validateMessageApply(incoming, aspects); err != nil {
		return false, err
	}
	incoming.SourceOccurredAt = incoming.SourceOccurredAt.UTC()
	now := r.now().UTC()
	applied := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockProjectionEntity(tx, "message", incoming.InstanceID, incoming.MessageID); err != nil {
			return err
		}
		var stored projection_model.ProjectedMessage
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("instance_id = ? AND message_id = ?", incoming.InstanceID, incoming.MessageID).First(&stored).Error
		created := errors.Is(err, gorm.ErrRecordNotFound)
		if err != nil && !created {
			return err
		}
		if created {
			if !containsMessageAspect(aspects, MessageAspectEnvelope) {
				return errors.New("message envelope aspect is required for creation")
			}
			stored = projection_model.ProjectedMessage{
				InstanceID: incoming.InstanceID, MessageID: incoming.MessageID,
				FieldVersions: json.RawMessage(`{}`),
			}
		}
		versions, err := decodeProjectionVersions(stored.FieldVersions)
		if err != nil {
			return fmt.Errorf("decode message field versions: %w", err)
		}
		version := projectionFieldVersion{OccurredAt: incoming.SourceOccurredAt, EventKey: incoming.SourceEventKey}
		for _, aspect := range aspects {
			current, exists := versions[string(aspect)]
			if exists && !projectionVersionLess(current, version) {
				continue
			}
			applyMessageAspect(&stored, incoming, aspect)
			versions[string(aspect)] = version
			applied = true
		}
		if !applied {
			return nil
		}
		if err := associateMessageConversation(tx, &stored); err != nil {
			return err
		}
		if projectionVersionLess(projectionFieldVersion{OccurredAt: stored.SourceOccurredAt, EventKey: stored.SourceEventKey}, version) {
			stored.SourceOccurredAt = incoming.SourceOccurredAt
			stored.SourceEventKey = incoming.SourceEventKey
		}
		stored.LastSyncedAt = now
		stored.FieldVersions, err = json.Marshal(versions)
		if err != nil {
			return fmt.Errorf("encode message field versions: %w", err)
		}
		if created {
			if err := tx.Create(&stored).Error; err != nil {
				return err
			}
		} else if err := tx.Save(&stored).Error; err != nil {
			return err
		}
		if stored.ConversationID != nil && stored.IsUnread != nil {
			return recomputeConversationUnread(tx, stored.InstanceID, *stored.ConversationID, now)
		}
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("apply message projection: %w", err)
	}
	return applied, nil
}

func (r *chatMessageRepository) ApplyReceipt(ctx context.Context, incoming *projection_model.MessageReceipt) (bool, error) {
	if err := validateReceipt(incoming); err != nil {
		return false, err
	}
	incoming.SourceOccurredAt = incoming.SourceOccurredAt.UTC()
	incoming.ReceiptAt = incoming.ReceiptAt.UTC()
	now := r.now().UTC()
	applied := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockProjectionEntity(tx, "receipt", incoming.InstanceID, incoming.MessageID+"\x00"+incoming.RecipientJID+"\x00"+incoming.ReceiptType); err != nil {
			return err
		}
		var stored projection_model.MessageReceipt
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where(
			"instance_id = ? AND message_id = ? AND recipient_jid = ? AND receipt_type = ?",
			incoming.InstanceID, incoming.MessageID, incoming.RecipientJID, incoming.ReceiptType,
		).First(&stored).Error
		created := errors.Is(err, gorm.ErrRecordNotFound)
		if err != nil && !created {
			return err
		}
		incomingVersion := projectionFieldVersion{OccurredAt: incoming.SourceOccurredAt, EventKey: incoming.SourceEventKey}
		currentVersion := projectionFieldVersion{OccurredAt: stored.SourceOccurredAt, EventKey: stored.SourceEventKey}
		if !created && !projectionVersionLess(currentVersion, incomingVersion) {
			return nil
		}
		createdAt := stored.CreatedAt
		stored = *incoming
		stored.CreatedAt = createdAt
		stored.LastSyncedAt = now
		applied = true
		if created {
			return tx.Create(&stored).Error
		}
		return tx.Save(&stored).Error
	})
	if err != nil {
		return false, fmt.Errorf("apply message receipt projection: %w", err)
	}
	return applied, nil
}

func (r *chatMessageRepository) MarkMessageRead(ctx context.Context, instanceID, messageID string, readAt time.Time) (bool, error) {
	if r == nil || r.db == nil || ctx == nil || instanceID == "" || messageID == "" || readAt.IsZero() {
		return false, errors.New("message read identity and timestamp are required")
	}
	changed := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockProjectionEntity(tx, "message", instanceID, messageID); err != nil {
			return err
		}
		var message projection_model.ProjectedMessage
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("instance_id = ? AND message_id = ?", instanceID, messageID).First(&message).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		versions, err := decodeProjectionVersions(message.FieldVersions)
		if err != nil {
			return fmt.Errorf("decode message field versions: %w", err)
		}
		readVersion := projectionFieldVersion{OccurredAt: readAt.UTC(), EventKey: "zz:read:" + messageID}
		if current, exists := versions[string(MessageAspectUnread)]; exists && !projectionVersionLess(current, readVersion) &&
			message.IsUnread != nil && !*message.IsUnread {
			return nil
		}
		versions[string(MessageAspectUnread)] = readVersion
		encodedVersions, err := json.Marshal(versions)
		if err != nil {
			return fmt.Errorf("encode message field versions: %w", err)
		}
		value := false
		message.IsUnread = &value
		if message.ReadAt == nil || readAt.Before(*message.ReadAt) {
			at := readAt.UTC()
			message.ReadAt = &at
		}
		if err := tx.Model(&projection_model.ProjectedMessage{}).
			Where("instance_id = ? AND message_id = ?", instanceID, messageID).
			Updates(map[string]any{"is_unread": false, "read_at": message.ReadAt, "field_versions": encodedVersions, "updated_at": r.now().UTC()}).Error; err != nil {
			return err
		}
		changed = true
		if message.ConversationID != nil {
			return recomputeConversationUnread(tx, instanceID, *message.ConversationID, r.now().UTC())
		}
		return nil
	})
	return changed, err
}

// ReconcileUnreadSnapshots converts every retained provider chat snapshot into
// message-level state after a RECENT/FULL completion barrier. History sync IDs
// identify individual chunks, so limiting reconciliation to the final chunk
// would leave earlier chunks permanently non-authoritative. Newer live/receipt
// unread versions always win over an older snapshot.
func (r *chatMessageRepository) ReconcileUnreadSnapshots(ctx context.Context, instanceID string) error {
	if r == nil || r.db == nil || ctx == nil || instanceID == "" {
		return errors.New("unread snapshot instance identity is required")
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`WITH ranked AS (
    SELECT messages.instance_id, messages.message_id, messages.chat_id,
           ROW_NUMBER() OVER (PARTITION BY messages.chat_id ORDER BY messages.provider_timestamp DESC, messages.message_id DESC) AS unread_rank
    FROM projected_messages AS messages
    JOIN projected_chats AS chats
      ON chats.instance_id = messages.instance_id AND chats.chat_id = messages.chat_id
    WHERE messages.instance_id = ? AND messages.deleted_at IS NULL
      AND messages.direction = 'incoming' AND chats.unread_snapshot_sync_id IS NOT NULL
      AND chats.unread_authoritative = FALSE
      AND (chats.last_activity_at IS NULL OR messages.provider_timestamp <= chats.last_activity_at)
), eligible AS (
    SELECT chats.chat_id, chats.unread_count, chats.unread_snapshot_sync_id,
           COALESCE(chats.last_activity_at, to_timestamp(0)) AS version_at
    FROM projected_chats AS chats
    WHERE chats.instance_id = ? AND chats.unread_snapshot_sync_id IS NOT NULL
      AND chats.unread_authoritative = FALSE
      AND (SELECT COUNT(*) FROM ranked WHERE ranked.chat_id = chats.chat_id) >= chats.unread_count
)
UPDATE projected_messages AS messages
SET is_unread = ranked.unread_rank <= eligible.unread_count,
    field_versions = jsonb_set(COALESCE(messages.field_versions, '{}'::jsonb), '{unread}',
        jsonb_build_object('occurredAt', eligible.version_at, 'eventKey', 'zz:snapshot:' || eligible.unread_snapshot_sync_id), TRUE),
    updated_at = NOW()
FROM ranked JOIN eligible ON eligible.chat_id = ranked.chat_id
WHERE messages.instance_id = ranked.instance_id AND messages.message_id = ranked.message_id
  AND (
    messages.field_versions->'unread' IS NULL
    OR (messages.field_versions->'unread'->>'occurredAt')::timestamptz < eligible.version_at
    OR ((messages.field_versions->'unread'->>'occurredAt')::timestamptz = eligible.version_at
        AND COALESCE(messages.field_versions->'unread'->>'eventKey', '') < ('zz:snapshot:' || eligible.unread_snapshot_sync_id))
  )`, instanceID, instanceID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`UPDATE projected_messages AS messages
SET is_unread = FALSE, updated_at = NOW()
FROM projected_chats AS chats
WHERE messages.instance_id = ? AND chats.instance_id = messages.instance_id
  AND chats.chat_id = messages.chat_id AND chats.unread_snapshot_sync_id IS NOT NULL
  AND chats.unread_authoritative = FALSE
  AND messages.direction = 'outgoing' AND messages.deleted_at IS NULL`, instanceID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`UPDATE projected_chats AS chats
SET unread_authoritative = (
    SELECT COUNT(*) FROM projected_messages AS messages
    WHERE messages.instance_id = chats.instance_id AND messages.chat_id = chats.chat_id
      AND messages.direction = 'incoming' AND messages.deleted_at IS NULL
      AND (chats.last_activity_at IS NULL OR messages.provider_timestamp <= chats.last_activity_at)
) >= chats.unread_count, updated_at = NOW()
WHERE chats.instance_id = ? AND chats.unread_snapshot_sync_id IS NOT NULL
  AND chats.unread_authoritative = FALSE`, instanceID).Error; err != nil {
			return err
		}
		var conversationIDs []string
		if err := tx.Table("projected_chat_aliases AS aliases").Distinct("aliases.conversation_id").
			Joins("JOIN projected_chats AS chats ON chats.instance_id = aliases.instance_id AND chats.chat_id = aliases.chat_id").
			Where("aliases.instance_id = ? AND chats.unread_snapshot_sync_id IS NOT NULL", instanceID).Pluck("aliases.conversation_id", &conversationIDs).Error; err != nil {
			return err
		}
		for _, conversationID := range conversationIDs {
			if err := recomputeConversationUnread(tx, instanceID, conversationID, r.now().UTC()); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *chatMessageRepository) GetChat(ctx context.Context, instanceID, chatID string) (*projection_model.Chat, error) {
	if instanceID == "" || chatID == "" {
		return nil, errors.New("chat projection identity is required")
	}
	var chat projection_model.Chat
	err := r.db.WithContext(ctx).Where("instance_id = ? AND chat_id = ? AND tombstoned_at IS NULL", instanceID, chatID).First(&chat).Error
	return &chat, err
}

func (r *chatMessageRepository) ListChats(ctx context.Context, instanceID string, limit int, cursor *ChatCursor) (*ChatPage, error) {
	if instanceID == "" || limit < 1 || limit > maxProjectionPageSize || (cursor != nil && cursor.ChatID == "") {
		return nil, errors.New("valid chat projection instance, limit, and cursor are required")
	}
	base := r.db.WithContext(ctx).Model(&projection_model.Chat{}).Where("instance_id = ? AND tombstoned_at IS NULL", instanceID)
	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, err
	}
	query := r.db.WithContext(ctx).Where("instance_id = ? AND tombstoned_at IS NULL", instanceID)
	if cursor != nil && cursor.LastActivityAt == nil {
		query = query.Where("last_activity_at IS NULL AND chat_id < ?", cursor.ChatID)
	} else if cursor != nil {
		at := cursor.LastActivityAt.UTC()
		query = query.Where("last_activity_at < ? OR (last_activity_at = ? AND chat_id < ?) OR last_activity_at IS NULL", at, at, cursor.ChatID)
	}
	var chats []projection_model.Chat
	if err := query.Order("last_activity_at DESC NULLS LAST, chat_id DESC").Limit(limit + 1).Find(&chats).Error; err != nil {
		return nil, err
	}
	page := &ChatPage{Items: chats, Total: total}
	if len(chats) > limit {
		page.Items = chats[:limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = &ChatCursor{LastActivityAt: last.LastActivityAt, ChatID: last.ChatID}
	}
	return page, nil
}

func (r *chatMessageRepository) GetConversation(ctx context.Context, instanceID, identity string) (*ConversationRecord, error) {
	if r == nil || r.db == nil || ctx == nil || instanceID == "" || identity == "" {
		return nil, errors.New("canonical conversation identity is required")
	}
	conversationID := ""
	if uuid.Validate(identity) == nil {
		var conversation projection_model.Conversation
		err := r.db.WithContext(ctx).Where("instance_id = ? AND conversation_id = ? AND tombstoned_at IS NULL", instanceID, identity).First(&conversation).Error
		if err == nil {
			return r.loadConversationRecord(ctx, &conversation)
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		var redirect projection_model.ConversationRedirect
		err = r.db.WithContext(ctx).Where("instance_id = ? AND absorbed_conversation_id = ?", instanceID, identity).First(&redirect).Error
		if err == nil {
			conversationID = redirect.CanonicalConversationID
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
	}
	if conversationID == "" {
		var alias projection_model.ChatAlias
		if err := r.db.WithContext(ctx).Where("instance_id = ? AND chat_id = ?", instanceID, identity).First(&alias).Error; err != nil {
			return nil, err
		}
		conversationID = alias.ConversationID
	}
	var conversation projection_model.Conversation
	if err := r.db.WithContext(ctx).Where("instance_id = ? AND conversation_id = ? AND tombstoned_at IS NULL", instanceID, conversationID).First(&conversation).Error; err != nil {
		return nil, err
	}
	return r.loadConversationRecord(ctx, &conversation)
}

func (r *chatMessageRepository) loadConversationRecord(ctx context.Context, conversation *projection_model.Conversation) (*ConversationRecord, error) {
	var aliases []projection_model.ChatAlias
	if err := r.db.WithContext(ctx).Where("instance_id = ? AND conversation_id = ?", conversation.InstanceID, conversation.ConversationID).
		Order("chat_id ASC").Find(&aliases).Error; err != nil {
		return nil, err
	}
	return &ConversationRecord{Conversation: *conversation, Aliases: aliases}, nil
}

func (r *chatMessageRepository) ListConversations(ctx context.Context, instanceID string, limit int, cursor *ConversationCursor) (*ConversationPage, error) {
	if r == nil || r.db == nil || ctx == nil || instanceID == "" || limit < 1 || limit > maxProjectionPageSize ||
		(cursor != nil && cursor.ConversationID == "") {
		return nil, errors.New("valid canonical conversation list parameters are required")
	}
	base := r.db.WithContext(ctx).Model(&projection_model.Conversation{}).Where("instance_id = ? AND tombstoned_at IS NULL", instanceID)
	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, err
	}
	query := r.db.WithContext(ctx).Where("instance_id = ? AND tombstoned_at IS NULL", instanceID)
	if cursor != nil && cursor.LastActivityAt == nil {
		query = query.Where("last_activity_at IS NULL AND conversation_id < ?", cursor.ConversationID)
	} else if cursor != nil {
		at := cursor.LastActivityAt.UTC()
		query = query.Where("last_activity_at < ? OR (last_activity_at = ? AND conversation_id < ?) OR last_activity_at IS NULL", at, at, cursor.ConversationID)
	}
	var conversations []projection_model.Conversation
	if err := query.Order("last_activity_at DESC NULLS LAST, conversation_id DESC").Limit(limit + 1).Find(&conversations).Error; err != nil {
		return nil, err
	}
	hasMore := len(conversations) > limit
	if hasMore {
		conversations = conversations[:limit]
	}
	page := &ConversationPage{Items: make([]ConversationRecord, len(conversations)), Total: total}
	if len(conversations) == 0 {
		return page, nil
	}
	conversationIDs := make([]string, len(conversations))
	for index := range conversations {
		conversationIDs[index] = conversations[index].ConversationID
		page.Items[index].Conversation = conversations[index]
	}
	var aliases []projection_model.ChatAlias
	if err := r.db.WithContext(ctx).Where("instance_id = ? AND conversation_id IN ?", instanceID, conversationIDs).
		Order("conversation_id ASC, chat_id ASC").Find(&aliases).Error; err != nil {
		return nil, err
	}
	positions := make(map[string]int, len(page.Items))
	for index := range page.Items {
		positions[page.Items[index].Conversation.ConversationID] = index
	}
	for _, alias := range aliases {
		index := positions[alias.ConversationID]
		page.Items[index].Aliases = append(page.Items[index].Aliases, alias)
	}
	if hasMore {
		last := conversations[len(conversations)-1]
		page.NextCursor = &ConversationCursor{LastActivityAt: last.LastActivityAt, ConversationID: last.ConversationID}
	}
	return page, nil
}

func (r *chatMessageRepository) ListConversationMessages(ctx context.Context, instanceID, conversationID string, limit int, cursor *MessageCursor) (*MessagePage, error) {
	if r == nil || r.db == nil || ctx == nil || instanceID == "" || conversationID == "" || limit < 1 || limit > maxProjectionPageSize ||
		(cursor != nil && (cursor.MessageID == "" || cursor.ProviderTimestamp.IsZero())) {
		return nil, errors.New("valid canonical conversation message parameters are required")
	}
	query := r.db.WithContext(ctx).Where("instance_id = ? AND conversation_id = ? AND deleted_at IS NULL", instanceID, conversationID)
	if cursor != nil {
		at := cursor.ProviderTimestamp.UTC()
		query = query.Where("provider_timestamp < ? OR (provider_timestamp = ? AND message_id < ?)", at, at, cursor.MessageID)
	}
	var messages []projection_model.ProjectedMessage
	if err := query.Order("provider_timestamp DESC, message_id DESC").Limit(limit + 1).Find(&messages).Error; err != nil {
		return nil, err
	}
	page := &MessagePage{Items: messages}
	if len(messages) > limit {
		page.Items = messages[:limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = &MessageCursor{ProviderTimestamp: last.ProviderTimestamp, MessageID: last.MessageID}
	}
	return page, nil
}

func (r *chatMessageRepository) GetMessage(ctx context.Context, instanceID, messageID string) (*projection_model.ProjectedMessage, error) {
	if instanceID == "" || messageID == "" {
		return nil, errors.New("message projection identity is required")
	}
	var message projection_model.ProjectedMessage
	err := r.db.WithContext(ctx).Where("instance_id = ? AND message_id = ? AND deleted_at IS NULL", instanceID, messageID).First(&message).Error
	return &message, err
}

func (r *chatMessageRepository) ListMessages(ctx context.Context, instanceID, chatID string, limit int, cursor *MessageCursor) (*MessagePage, error) {
	if instanceID == "" || chatID == "" || limit < 1 || limit > maxProjectionPageSize ||
		(cursor != nil && (cursor.MessageID == "" || cursor.ProviderTimestamp.IsZero())) {
		return nil, errors.New("valid message projection identity, limit, and cursor are required")
	}
	query := r.db.WithContext(ctx).Where("instance_id = ? AND chat_id = ? AND deleted_at IS NULL", instanceID, chatID)
	if cursor != nil {
		at := cursor.ProviderTimestamp.UTC()
		query = query.Where("provider_timestamp < ? OR (provider_timestamp = ? AND message_id < ?)", at, at, cursor.MessageID)
	}
	var messages []projection_model.ProjectedMessage
	if err := query.Order("provider_timestamp DESC, message_id DESC").Limit(limit + 1).Find(&messages).Error; err != nil {
		return nil, err
	}
	page := &MessagePage{Items: messages}
	if len(messages) > limit {
		page.Items = messages[:limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = &MessageCursor{ProviderTimestamp: last.ProviderTimestamp, MessageID: last.MessageID}
	}
	return page, nil
}

func (r *chatMessageRepository) ListReceipts(ctx context.Context, instanceID, messageID string) ([]projection_model.MessageReceipt, error) {
	if instanceID == "" || messageID == "" {
		return nil, errors.New("message receipt projection identity is required")
	}
	var receipts []projection_model.MessageReceipt
	err := r.db.WithContext(ctx).Where("instance_id = ? AND message_id = ?", instanceID, messageID).
		Order("receipt_at ASC, recipient_jid ASC, receipt_type ASC").Find(&receipts).Error
	return receipts, err
}

func validateChatApply(chat *projection_model.Chat, aspects []ChatAspect) error {
	if chat == nil || chat.InstanceID == "" || chat.ChatID == "" || len(chat.ChatID) > 255 ||
		chat.SourceOccurredAt.IsZero() || chat.SourceEventKey == "" || len(chat.SourceEventKey) > 255 {
		return errors.New("chat projection identity and source version are required")
	}
	if len(aspects) == 0 || hasDuplicateOrInvalidChatAspects(aspects) {
		return errors.New("at least one unique valid chat aspect is required")
	}
	if containsChatAspect(aspects, ChatAspectIdentity) && !validChatType(chat.Type) {
		return errors.New("chat projection type is invalid")
	}
	if containsChatAspect(aspects, ChatAspectActivity) && pointerStringTooLong(chat.LastMessageID, 255) {
		return errors.New("chat last message identity exceeds storage limits")
	}
	if chat.UnreadCount < 0 {
		return errors.New("chat unread count cannot be negative")
	}
	return nil
}

func validateMessageApply(message *projection_model.ProjectedMessage, aspects []MessageAspect) error {
	if message == nil || message.InstanceID == "" || message.MessageID == "" || len(message.MessageID) > 255 ||
		message.SourceOccurredAt.IsZero() || message.SourceEventKey == "" || len(message.SourceEventKey) > 255 {
		return errors.New("message projection identity and source version are required")
	}
	if len(aspects) == 0 || hasDuplicateOrInvalidMessageAspects(aspects) {
		return errors.New("at least one unique valid message aspect is required")
	}
	if containsMessageAspect(aspects, MessageAspectEnvelope) &&
		(message.ChatID == "" || len(message.ChatID) > 255 || !validMessageDirection(message.Direction) || message.MessageType == "" ||
			len(message.MessageType) > 64 || pointerStringTooLong(message.SenderJID, 255) || pointerStringTooLong(message.RecipientJID, 255) ||
			pointerStringTooLong(message.ParticipantJID, 255) || pointerStringTooLong(message.HistorySyncID, 255) ||
			message.ProviderTimestamp.IsZero() || !validMessageProvenance(message.Provenance)) {
		return errors.New("message projection envelope is invalid")
	}
	if containsMessageAspect(aspects, MessageAspectContent) && pointerStringTooLong(message.QuotedMessageID, 255) {
		return errors.New("quoted message identity exceeds storage limits")
	}
	if containsMessageAspect(aspects, MessageAspectMedia) && (pointerStringTooLong(message.MediaType, 64) || pointerStringTooLong(message.MediaMIMEType, 255)) {
		return errors.New("message media metadata exceeds storage limits")
	}
	if containsMessageAspect(aspects, MessageAspectLifecycle) && pointerStringTooLong(message.Status, 32) {
		return errors.New("message lifecycle status exceeds storage limits")
	}
	if containsMessageAspect(aspects, MessageAspectMedia) && message.MediaSize != nil && *message.MediaSize < 0 {
		return errors.New("message media size cannot be negative")
	}
	if containsMessageAspect(aspects, MessageAspectUnread) && message.IsUnread == nil {
		return errors.New("message unread state is required")
	}
	return nil
}

func validateReceipt(receipt *projection_model.MessageReceipt) error {
	if receipt == nil || receipt.InstanceID == "" || receipt.MessageID == "" || len(receipt.MessageID) > 255 ||
		receipt.RecipientJID == "" || len(receipt.RecipientJID) > 255 || !validReceiptType(receipt.ReceiptType) ||
		receipt.ReceiptAt.IsZero() || receipt.SourceOccurredAt.IsZero() || receipt.SourceEventKey == "" || len(receipt.SourceEventKey) > 255 {
		return errors.New("message receipt projection identity, type, timestamps, and source version are required")
	}
	return nil
}

func applyChatAspect(stored, incoming *projection_model.Chat, aspect ChatAspect) {
	switch aspect {
	case ChatAspectIdentity:
		stored.ContactID, stored.Type, stored.DisplayName = incoming.ContactID, incoming.Type, incoming.DisplayName
		stored.DisplayNameSource, stored.DisplayNameUpdatedAt = incoming.DisplayNameSource, incoming.DisplayNameUpdatedAt
	case ChatAspectActivity:
		stored.LastMessageID, stored.LastMessageAt, stored.LastActivityAt = incoming.LastMessageID, incoming.LastMessageAt, incoming.LastActivityAt
	case ChatAspectSettings:
		stored.UnreadCount, stored.Archived, stored.Pinned = incoming.UnreadCount, incoming.Archived, incoming.Pinned
		stored.MutedUntil, stored.DisappearingTimer = incoming.MutedUntil, incoming.DisappearingTimer
	case ChatAspectArchived:
		stored.Archived = incoming.Archived
	case ChatAspectPinned:
		stored.Pinned = incoming.Pinned
	case ChatAspectMuted:
		stored.MutedUntil = incoming.MutedUntil
	case ChatAspectUnreadSnapshot:
		stored.UnreadAuthoritative, stored.UnreadSnapshotSyncID = incoming.UnreadAuthoritative, incoming.UnreadSnapshotSyncID
	case ChatAspectDeletion:
		stored.TombstonedAt = incoming.TombstonedAt
	}
}

func applyMessageAspect(stored, incoming *projection_model.ProjectedMessage, aspect MessageAspect) {
	switch aspect {
	case MessageAspectEnvelope:
		stored.ChatID, stored.SenderJID, stored.RecipientJID, stored.ParticipantJID = incoming.ChatID, incoming.SenderJID, incoming.RecipientJID, incoming.ParticipantJID
		stored.Direction, stored.MessageType, stored.ProviderTimestamp = incoming.Direction, incoming.MessageType, incoming.ProviderTimestamp.UTC()
		stored.Provenance, stored.HistorySyncID = incoming.Provenance, incoming.HistorySyncID
	case MessageAspectContent:
		stored.ContentText, stored.Caption, stored.ContentSummary = incoming.ContentText, incoming.Caption, incoming.ContentSummary
		stored.QuotedMessageID = incoming.QuotedMessageID
	case MessageAspectMedia:
		stored.MediaType, stored.MediaMIMEType, stored.MediaFileName = incoming.MediaType, incoming.MediaMIMEType, incoming.MediaFileName
		stored.MediaSize, stored.MediaDuration, stored.MediaWidth, stored.MediaHeight = incoming.MediaSize, incoming.MediaDuration, incoming.MediaWidth, incoming.MediaHeight
		stored.MediaObjectKey, stored.MediaAssetID = incoming.MediaObjectKey, incoming.MediaAssetID
	case MessageAspectLifecycle:
		stored.Status, stored.SentAt, stored.DeliveredAt = incoming.Status, incoming.SentAt, incoming.DeliveredAt
		stored.ReadAt, stored.PlayedAt = incoming.ReadAt, incoming.PlayedAt
	case MessageAspectRetention:
		stored.RetentionExpiresAt, stored.DeletedAt = incoming.RetentionExpiresAt, incoming.DeletedAt
	case MessageAspectUnread:
		stored.IsUnread = incoming.IsUnread
	}
}

func decodeProjectionVersions(raw json.RawMessage) (map[string]projectionFieldVersion, error) {
	versions := make(map[string]projectionFieldVersion)
	if len(raw) == 0 {
		return versions, nil
	}
	if err := json.Unmarshal(raw, &versions); err != nil {
		return nil, err
	}
	return versions, nil
}

func projectionVersionLess(left, right projectionFieldVersion) bool {
	return left.OccurredAt.Before(right.OccurredAt) || (left.OccurredAt.Equal(right.OccurredAt) && left.EventKey < right.EventKey)
}

func lockProjectionEntity(tx *gorm.DB, resource, instanceID, entityID string) error {
	identity := fmt.Sprintf("%d:%s%d:%s%d:%s", len(resource), resource, len(instanceID), instanceID, len(entityID), entityID)
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(identity)))
	return tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", key).Error
}

func containsChatAspect(aspects []ChatAspect, wanted ChatAspect) bool {
	for _, aspect := range aspects {
		if aspect == wanted {
			return true
		}
	}
	return false
}

func containsMessageAspect(aspects []MessageAspect, wanted MessageAspect) bool {
	for _, aspect := range aspects {
		if aspect == wanted {
			return true
		}
	}
	return false
}

func hasDuplicateOrInvalidChatAspects(aspects []ChatAspect) bool {
	seen := make(map[ChatAspect]struct{}, len(aspects))
	for _, aspect := range aspects {
		if aspect != ChatAspectIdentity && aspect != ChatAspectActivity && aspect != ChatAspectSettings &&
			aspect != ChatAspectArchived && aspect != ChatAspectPinned && aspect != ChatAspectMuted &&
			aspect != ChatAspectUnreadSnapshot && aspect != ChatAspectDeletion {
			return true
		}
		if _, exists := seen[aspect]; exists {
			return true
		}
		seen[aspect] = struct{}{}
	}
	return false
}

func hasDuplicateOrInvalidMessageAspects(aspects []MessageAspect) bool {
	seen := make(map[MessageAspect]struct{}, len(aspects))
	for _, aspect := range aspects {
		if aspect != MessageAspectEnvelope && aspect != MessageAspectContent && aspect != MessageAspectMedia && aspect != MessageAspectLifecycle && aspect != MessageAspectRetention && aspect != MessageAspectUnread {
			return true
		}
		if _, exists := seen[aspect]; exists {
			return true
		}
		seen[aspect] = struct{}{}
	}
	return false
}

func validChatType(value projection_model.ChatType) bool {
	return value == projection_model.ChatTypeDirect || value == projection_model.ChatTypeGroup || value == projection_model.ChatTypeNewsletter ||
		value == projection_model.ChatTypeBroadcast || value == projection_model.ChatTypeUnknown
}

func validMessageDirection(value projection_model.MessageDirection) bool {
	return value == projection_model.MessageDirectionIncoming || value == projection_model.MessageDirectionOutgoing || value == projection_model.MessageDirectionSystem
}

func validMessageProvenance(value projection_model.MessageProvenance) bool {
	return value == projection_model.MessageProvenanceLive || value == projection_model.MessageProvenanceHistorySync || value == projection_model.MessageProvenanceWriteThrough
}

func validReceiptType(value string) bool {
	return value == "sent" || value == "delivered" || value == "read" || value == "played" || value == "error"
}

func pointerStringTooLong(value *string, limit int) bool {
	return value != nil && len(*value) > limit
}
