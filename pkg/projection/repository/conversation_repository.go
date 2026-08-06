package projection_repository

import (
	"errors"
	"fmt"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	"github.com/google/uuid"
	"go.mau.fi/whatsmeow/types"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var conversationIdentityNamespace = uuid.MustParse("d992e923-cc36-4d42-a0d2-19076f087766")

func ensureChatConversation(tx *gorm.DB, chat *projection_model.Chat, now time.Time) error {
	if tx == nil || chat == nil || chat.InstanceID == "" || chat.ChatID == "" || now.IsZero() {
		return errors.New("chat conversation identity is required")
	}
	conversationID, identityKey := deterministicConversationIdentity(chat)
	if err := lockProjectionEntity(tx, "conversation-identity", chat.InstanceID, identityKey); err != nil {
		return err
	}
	if err := ensureConversationRow(tx, chat, conversationID, now); err != nil {
		return err
	}

	chats := []projection_model.Chat{*chat}
	if chat.Type == projection_model.ChatTypeDirect && chat.ContactID != nil && *chat.ContactID != "" {
		if err := tx.Where("instance_id = ? AND contact_id = ? AND chat_type = ? AND tombstoned_at IS NULL", chat.InstanceID, *chat.ContactID, projection_model.ChatTypeDirect).
			Order("chat_id ASC").Find(&chats).Error; err != nil {
			return err
		}
	}
	for index := range chats {
		if err := associateChatAlias(tx, &chats[index], conversationID, now); err != nil {
			return err
		}
	}
	if err := recomputeConversationSummary(tx, chat.InstanceID, conversationID, now); err != nil {
		return err
	}
	chat.ConversationID = &conversationID
	return nil
}

func deterministicConversationIdentity(chat *projection_model.Chat) (string, string) {
	identity := string(chat.Type) + ":chat:" + chat.ChatID
	if chat.Type == projection_model.ChatTypeDirect && chat.ContactID != nil && *chat.ContactID != "" {
		identity = "direct:contact:" + *chat.ContactID
	}
	key := chat.InstanceID + "\x00" + identity
	return uuid.NewSHA1(conversationIdentityNamespace, []byte(key)).String(), identity
}

func ensureConversationRow(tx *gorm.DB, chat *projection_model.Chat, conversationID string, now time.Time) error {
	conversation := projection_model.Conversation{
		InstanceID: chat.InstanceID, ConversationID: conversationID, ContactID: chat.ContactID, Type: chat.Type,
		AddressingJID: addressingJID(chat), DisplayName: chat.DisplayName, DisplayNameSource: chat.DisplayNameSource,
		DisplayNameUpdatedAt: chat.DisplayNameUpdatedAt, LastMessageID: chat.LastMessageID, LastMessageAt: chat.LastMessageAt,
		LastActivityAt: chat.LastActivityAt, UnreadCount: chat.UnreadCount, Archived: chat.Archived, Pinned: chat.Pinned,
		MutedUntil: chat.MutedUntil, DisappearingTimer: chat.DisappearingTimer, FieldVersions: chat.FieldVersions,
		LastSyncedAt: now.UTC(), CreatedAt: now.UTC(), UpdatedAt: now.UTC(),
	}
	if len(conversation.FieldVersions) == 0 {
		conversation.FieldVersions = []byte(`{}`)
	}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&conversation).Error
}

func associateChatAlias(tx *gorm.DB, chat *projection_model.Chat, conversationID string, now time.Time) error {
	var existing projection_model.ChatAlias
	err := tx.Where("instance_id = ? AND chat_id = ?", chat.InstanceID, chat.ChatID).First(&existing).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	previousConversationID := existing.ConversationID
	alias := projection_model.ChatAlias{
		InstanceID: chat.InstanceID, ChatID: chat.ChatID, ConversationID: conversationID, AliasKind: projectedChatAliasKind(chat),
		CreatedAt: now.UTC(), UpdatedAt: now.UTC(),
	}
	if err := tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "instance_id"}, {Name: "chat_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"conversation_id": conversationID, "alias_kind": alias.AliasKind, "updated_at": now.UTC(),
		}),
	}).Create(&alias).Error; err != nil {
		return err
	}
	if err := tx.Model(&projection_model.Chat{}).
		Where("instance_id = ? AND chat_id = ? AND conversation_id IS DISTINCT FROM ?", chat.InstanceID, chat.ChatID, conversationID).
		Update("conversation_id", conversationID).Error; err != nil {
		return err
	}
	// Only retained messages participate in canonical Conversation history and
	// structural readiness. Include the partial-index predicate and skip rows
	// that are already associated: ApplyChat runs for every message event, so an
	// unconditional update otherwise scans and rewrites the whole message table
	// once per event during a large history import.
	if err := tx.Model(&projection_model.ProjectedMessage{}).
		Where("instance_id = ? AND chat_id = ? AND deleted_at IS NULL AND conversation_id IS DISTINCT FROM ?", chat.InstanceID, chat.ChatID, conversationID).
		Update("conversation_id", conversationID).Error; err != nil {
		return err
	}
	if previousConversationID != "" && previousConversationID != conversationID {
		return retireConversationIfUnreferenced(tx, chat.InstanceID, previousConversationID, conversationID, now)
	}
	return nil
}

func retireConversationIfUnreferenced(tx *gorm.DB, instanceID, absorbedID, canonicalID string, now time.Time) error {
	var aliases int64
	if err := tx.Model(&projection_model.ChatAlias{}).
		Where("instance_id = ? AND conversation_id = ?", instanceID, absorbedID).Count(&aliases).Error; err != nil {
		return err
	}
	if aliases > 0 {
		return nil
	}
	if err := tx.Model(&projection_model.ConversationRedirect{}).
		Where("instance_id = ? AND canonical_conversation_id = ?", instanceID, absorbedID).
		Updates(map[string]any{"canonical_conversation_id": canonicalID, "updated_at": now.UTC()}).Error; err != nil {
		return err
	}
	redirect := projection_model.ConversationRedirect{
		InstanceID: instanceID, AbsorbedConversationID: absorbedID, CanonicalConversationID: canonicalID,
		CreatedAt: now.UTC(), UpdatedAt: now.UTC(),
	}
	if err := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "instance_id"}, {Name: "absorbed_conversation_id"}},
		DoUpdates: clause.Assignments(map[string]any{"canonical_conversation_id": canonicalID, "updated_at": now.UTC()}),
	}).Create(&redirect).Error; err != nil {
		return err
	}
	return tx.Model(&projection_model.Conversation{}).
		Where("instance_id = ? AND conversation_id = ?", instanceID, absorbedID).
		Updates(map[string]any{"contact_id": nil, "tombstoned_at": now.UTC(), "updated_at": now.UTC()}).Error
}

func recomputeConversationSummary(tx *gorm.DB, instanceID, conversationID string, now time.Time) error {
	var chats []projection_model.Chat
	if err := tx.Table("projected_chats AS chats").
		Joins("JOIN projected_chat_aliases AS aliases ON aliases.instance_id = chats.instance_id AND aliases.chat_id = chats.chat_id").
		Where("aliases.instance_id = ? AND aliases.conversation_id = ? AND chats.tombstoned_at IS NULL", instanceID, conversationID).
		Order("chats.last_activity_at DESC NULLS LAST, chats.source_occurred_at DESC, chats.chat_id DESC").Find(&chats).Error; err != nil {
		return err
	}
	if len(chats) == 0 {
		return errors.New("canonical conversation has no active chat aliases")
	}
	primary := chats[0]
	archivedSource, err := latestConversationSettingSource(chats, ChatAspectArchived, ChatAspectSettings)
	if err != nil {
		return err
	}
	pinnedSource, err := latestConversationSettingSource(chats, ChatAspectPinned, ChatAspectSettings)
	if err != nil {
		return err
	}
	mutedSource, err := latestConversationSettingSource(chats, ChatAspectMuted, ChatAspectSettings)
	if err != nil {
		return err
	}
	disappearingSource, err := latestConversationSettingSource(chats, ChatAspectSettings)
	if err != nil {
		return err
	}
	if archivedSource == nil {
		archivedSource = &primary
	}
	if pinnedSource == nil {
		pinnedSource = &primary
	}
	if mutedSource == nil {
		mutedSource = &primary
	}
	if disappearingSource == nil {
		disappearingSource = &primary
	}
	lastSyncedAt := primary.LastSyncedAt
	for index := range chats {
		if chats[index].LastSyncedAt.After(lastSyncedAt) {
			lastSyncedAt = chats[index].LastSyncedAt
		}
	}
	updates := map[string]any{
		"contact_id": primary.ContactID, "conversation_type": primary.Type, "addressing_jid": addressingJID(&primary),
		"display_name": primary.DisplayName, "display_name_source": primary.DisplayNameSource,
		"display_name_updated_at": primary.DisplayNameUpdatedAt, "last_message_id": primary.LastMessageID,
		"last_message_at": primary.LastMessageAt, "last_activity_at": primary.LastActivityAt,
		"archived": archivedSource.Archived, "pinned": pinnedSource.Pinned, "muted_until": mutedSource.MutedUntil,
		"disappearing_timer": disappearingSource.DisappearingTimer, "field_versions": primary.FieldVersions,
		"last_synced_at": lastSyncedAt.UTC(), "tombstoned_at": nil, "updated_at": now.UTC(),
	}
	if primary.Type == projection_model.ChatTypeDirect && primary.ContactID != nil {
		var contact projection_model.Contact
		if err := tx.Where("instance_id = ? AND contact_id = ? AND tombstoned_at IS NULL", instanceID, *primary.ContactID).First(&contact).Error; err != nil {
			return err
		}
		updates["addressing_jid"] = contact.PreferredJID
		if name, source := contact.CanonicalDisplayName(); name != "" {
			updatedAt := contact.UpdatedAt.UTC()
			updates["display_name"], updates["display_name_source"], updates["display_name_updated_at"] = name, source, updatedAt
		}
	}
	if err := tx.Model(&projection_model.Conversation{}).
		Where("instance_id = ? AND conversation_id = ?", instanceID, conversationID).Updates(updates).Error; err != nil {
		return err
	}
	return recomputeConversationUnread(tx, instanceID, conversationID, now)
}

func latestConversationSettingSource(chats []projection_model.Chat, aspects ...ChatAspect) (*projection_model.Chat, error) {
	var selected *projection_model.Chat
	var selectedVersion projectionFieldVersion
	for index := range chats {
		versions, err := decodeProjectionVersions(chats[index].FieldVersions)
		if err != nil {
			return nil, fmt.Errorf("decode conversation setting versions: %w", err)
		}
		var candidate projectionFieldVersion
		found := false
		for _, aspect := range aspects {
			version, exists := versions[string(aspect)]
			if exists && (!found || projectionVersionLess(candidate, version)) {
				candidate, found = version, true
			}
		}
		if found && (selected == nil || projectionVersionLess(selectedVersion, candidate)) {
			selected, selectedVersion = &chats[index], candidate
		}
	}
	return selected, nil
}

func recomputeConversationUnread(tx *gorm.DB, instanceID, conversationID string, now time.Time) error {
	if tx == nil || instanceID == "" || conversationID == "" || now.IsZero() {
		return errors.New("canonical conversation unread identity is required")
	}
	var unreadCount int64
	if err := tx.Model(&projection_model.ProjectedMessage{}).
		Where("instance_id = ? AND conversation_id = ? AND deleted_at IS NULL AND is_unread = TRUE", instanceID, conversationID).
		Count(&unreadCount).Error; err != nil {
		return err
	}
	var incompleteMessages int64
	if err := tx.Model(&projection_model.ProjectedMessage{}).
		Where("instance_id = ? AND conversation_id = ? AND deleted_at IS NULL AND direction = ? AND is_unread IS NULL",
			instanceID, conversationID, projection_model.MessageDirectionIncoming).Count(&incompleteMessages).Error; err != nil {
		return err
	}
	var incompleteChats int64
	if err := tx.Table("projected_chat_aliases AS aliases").
		Joins("JOIN projected_chats AS chats ON chats.instance_id = aliases.instance_id AND chats.chat_id = aliases.chat_id").
		Where("aliases.instance_id = ? AND aliases.conversation_id = ? AND chats.tombstoned_at IS NULL AND chats.unread_authoritative = FALSE",
			instanceID, conversationID).Count(&incompleteChats).Error; err != nil {
		return err
	}
	return tx.Model(&projection_model.Conversation{}).
		Where("instance_id = ? AND conversation_id = ?", instanceID, conversationID).
		Updates(map[string]any{
			"unread_count": unreadCount, "unread_authoritative": incompleteMessages == 0 && incompleteChats == 0,
			"updated_at": now.UTC(),
		}).Error
}

func addressingJID(chat *projection_model.Chat) *string {
	if chat == nil || chat.ChatID == "" {
		return nil
	}
	value := chat.ChatID
	return &value
}

func projectedChatAliasKind(chat *projection_model.Chat) string {
	if chat == nil {
		return "unknown"
	}
	jid, err := types.ParseJID(chat.ChatID)
	if err == nil {
		switch jid.Server {
		case types.DefaultUserServer:
			return "phone_jid"
		case types.HiddenUserServer:
			return "lid"
		case types.GroupServer:
			return "group"
		case types.NewsletterServer:
			return "newsletter"
		case types.BroadcastServer:
			return "broadcast"
		}
	}
	switch chat.Type {
	case projection_model.ChatTypeGroup:
		return "group"
	case projection_model.ChatTypeNewsletter:
		return "newsletter"
	case projection_model.ChatTypeBroadcast:
		return "broadcast"
	}
	return "unknown"
}

func associateMessageConversation(tx *gorm.DB, message *projection_model.ProjectedMessage) error {
	if tx == nil || message == nil || message.InstanceID == "" || message.ChatID == "" {
		return errors.New("message conversation identity is required")
	}
	var alias projection_model.ChatAlias
	if err := tx.Where("instance_id = ? AND chat_id = ?", message.InstanceID, message.ChatID).First(&alias).Error; err != nil {
		return fmt.Errorf("resolve message chat alias: %w", err)
	}
	message.ConversationID = &alias.ConversationID
	return nil
}

func reconcileDirectConversationsForContact(tx *gorm.DB, instanceID, contactID string, now time.Time) error {
	if tx == nil || instanceID == "" || contactID == "" || now.IsZero() {
		return errors.New("canonical contact conversation identity is required")
	}
	var chat projection_model.Chat
	err := tx.Where("instance_id = ? AND contact_id = ? AND chat_type = ? AND tombstoned_at IS NULL", instanceID, contactID, projection_model.ChatTypeDirect).
		Order("chat_id ASC").First(&chat).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return ensureChatConversation(tx, &chat, now)
}
