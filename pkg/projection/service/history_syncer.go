package projection_service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	waHistorySync "go.mau.fi/whatsmeow/proto/waHistorySync"
	waWeb "go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"gorm.io/gorm"
)

type HistoryMessageParser func(types.JID, *waWeb.WebMessageInfo) (*events.Message, error)

type historySyncEvents interface {
	Ingest(context.Context, *projection_model.Event) (bool, error)
}

type historySyncState interface {
	Get(instanceID, resource string) (*projection_model.State, error)
	MarkSyncing(instanceID, resource string, schemaVersion int64) error
	MarkStale(instanceID, resource string, schemaVersion int64) error
	MarkFailed(instanceID, resource string, schemaVersion int64) error
}

type HistorySyncer struct {
	events historySyncEvents
	state  historySyncState
	now    func() time.Time
	locks  sync.Map
}

func NewHistorySyncer(events historySyncEvents, state historySyncState) *HistorySyncer {
	return &HistorySyncer{events: events, state: state, now: time.Now}
}

func (s *HistorySyncer) Sync(ctx context.Context, instanceID string, raw *events.HistorySync, parser HistoryMessageParser) error {
	if s == nil || s.events == nil || s.state == nil || s.now == nil || instanceID == "" || raw == nil || raw.Data == nil || parser == nil {
		return errors.New("history sync dependencies, instance identity, data, and parser are required")
	}
	syncType := raw.Data.GetSyncType()
	resources, relevant := historySyncResources(syncType)
	if !relevant {
		return nil
	}
	lockValue, _ := s.locks.LoadOrStore(instanceID, &sync.Mutex{})
	lock := lockValue.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()

	for _, resource := range resources {
		if err := s.ensureSyncing(instanceID, resource, historyResourceSchemaVersion(resource)); err != nil {
			return err
		}
	}
	syncID := historySyncIdentity(raw.Data)
	for _, conversation := range raw.Data.GetConversations() {
		if err := s.ingestConversation(ctx, instanceID, syncID, conversation, parser); err != nil {
			return s.fail(instanceID, resources, err)
		}
	}
	if raw.Data.Progress != nil && raw.Data.GetProgress() >= 100 &&
		(historySyncCompletesChats(syncType) || historySyncCompletesMessages(syncType)) {
		if err := s.ingestCompletion(ctx, instanceID, syncID, syncType); err != nil {
			return s.fail(instanceID, resources, err)
		}
	}
	return nil
}

func (s *HistorySyncer) ingestConversation(ctx context.Context, instanceID, syncID string, conversation *waHistorySync.Conversation, parser HistoryMessageParser) error {
	if conversation == nil || conversation.GetID() == "" {
		return errors.New("history sync conversation has no identity")
	}
	chatJID, err := types.ParseJID(conversation.GetID())
	if err != nil || chatJID.IsEmpty() {
		return errors.New("history sync conversation identity is invalid")
	}
	lastActivityAt := historyConversationTime(conversation)
	name := conversation.GetName()
	if name == "" {
		name = conversation.GetDisplayName()
	}
	unread := int(conversation.GetUnreadCount())
	var pinned *bool
	if conversation.Pinned != nil {
		value := conversation.GetPinned() > 0
		pinned = &value
	}
	payload := messageEventPayload{
		ChatID: chatJID.ToNonAD().String(), ChatType: projectedChatType(chatJID), DisplayName: boundedTextPointer(name, 4096),
		UnreadCount: &unread, Archived: conversation.Archived, Pinned: pinned,
		DisappearingTimer: conversation.EphemeralExpiration, LastActivityAt: lastActivityAt, HistorySyncID: &syncID,
	}
	if conversation.MuteEndTime != nil && conversation.GetMuteEndTime() > 0 && conversation.GetMuteEndTime() <= math.MaxInt64 {
		mutedUntil := time.Unix(int64(conversation.GetMuteEndTime()), 0).UTC()
		payload.MutedUntil = &mutedUntil
	}
	occurredAt := time.Unix(0, 0).UTC()
	if lastActivityAt != nil {
		occurredAt = *lastActivityAt
	}
	chatEvent, _, err := newMessageProjectionEvent(instanceID, "history_chat", payload.ChatID, occurredAt, payload)
	if err != nil {
		return err
	}
	if _, err := s.events.Ingest(ctx, chatEvent); err != nil {
		return err
	}
	for _, historyMessage := range conversation.GetMessages() {
		if historyMessage == nil || historyMessage.GetMessage() == nil {
			return errors.New("history sync contains an empty message")
		}
		parsed, err := parser(chatJID, historyMessage.GetMessage())
		if err != nil {
			return fmt.Errorf("parse history message: %w", err)
		}
		if err := s.ingestMessage(ctx, instanceID, syncID, parsed, historyMessage.GetMessage()); err != nil {
			return err
		}
	}
	return nil
}

func (s *HistorySyncer) ingestMessage(ctx context.Context, instanceID, syncID string, parsed *events.Message, source *waWeb.WebMessageInfo) error {
	event, relevant, err := NormalizeChatMessageEvent(instanceID, parsed)
	if err != nil {
		return err
	}
	if !relevant || event == nil {
		return errors.New("parsed history message is not projection-relevant")
	}
	var payload messageEventPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return errors.New("invalid normalized history message payload")
	}
	payload.Provenance = projection_model.MessageProvenanceHistorySync
	payload.HistorySyncID = &syncID
	if source != nil && source.Status != nil {
		status := historyMessageStatus(source.GetStatus())
		payload.Status = &status
	}
	event, _, err = newMessageProjectionEvent(instanceID, "history_message", payload.MessageID, event.OccurredAt, payload)
	if err != nil {
		return err
	}
	_, err = s.events.Ingest(ctx, event)
	return err
}

func (s *HistorySyncer) ingestCompletion(ctx context.Context, instanceID, syncID string, syncType waHistorySync.HistorySync_HistorySyncType) error {
	completedAt := s.now().UTC()
	typeName := syncType.String()
	payload := messageEventPayload{
		ChatID: "history-sync", HistorySyncID: &syncID, HistorySyncType: &typeName, CompletedAt: &completedAt,
		ChatsReady: historySyncCompletesChats(syncType), MessagesReady: historySyncCompletesMessages(syncType),
	}
	event, _, err := newMessageProjectionEvent(instanceID, "history_sync_complete", syncID, completedAt, payload)
	if err != nil {
		return err
	}
	_, err = s.events.Ingest(ctx, event)
	return err
}

func (s *HistorySyncer) ensureSyncing(instanceID, resource string, version int64) error {
	state, err := s.state.Get(instanceID, resource)
	if err == nil && state != nil && state.SyncStatus == projection_model.SyncStatusReady && state.SchemaVersion >= version {
		return nil
	}
	if err == nil && state != nil && state.SyncStatus == projection_model.SyncStatusSyncing && state.SchemaVersion >= version {
		return nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return s.state.MarkSyncing(instanceID, resource, version)
}

func (s *HistorySyncer) fail(instanceID string, resources []string, syncErr error) error {
	errorsList := []error{syncErr}
	for _, resource := range resources {
		state, err := s.state.Get(instanceID, resource)
		if err == nil && state != nil && state.LastReconciledAt != nil {
			errorsList = append(errorsList, s.state.MarkStale(instanceID, resource, historyResourceSchemaVersion(resource)))
		} else {
			errorsList = append(errorsList, s.state.MarkFailed(instanceID, resource, historyResourceSchemaVersion(resource)))
		}
	}
	return errors.Join(errorsList...)
}

func historySyncResources(syncType waHistorySync.HistorySync_HistorySyncType) ([]string, bool) {
	switch syncType {
	case waHistorySync.HistorySync_INITIAL_BOOTSTRAP:
		return []string{"chats"}, true
	case waHistorySync.HistorySync_RECENT, waHistorySync.HistorySync_FULL:
		return []string{"chats", messageResource}, true
	case waHistorySync.HistorySync_ON_DEMAND:
		return nil, true
	default:
		return nil, false
	}
}

func historyResourceSchemaVersion(resource string) int64 {
	if resource == "chats" {
		return ChatsProjectionSchemaVersion
	}
	return MessagesProjectionSchemaVersion
}

func historySyncCompletesChats(syncType waHistorySync.HistorySync_HistorySyncType) bool {
	return syncType == waHistorySync.HistorySync_INITIAL_BOOTSTRAP || syncType == waHistorySync.HistorySync_RECENT || syncType == waHistorySync.HistorySync_FULL
}

func historySyncCompletesMessages(syncType waHistorySync.HistorySync_HistorySyncType) bool {
	return syncType == waHistorySync.HistorySync_RECENT || syncType == waHistorySync.HistorySync_FULL
}

func historyConversationTime(conversation *waHistorySync.Conversation) *time.Time {
	latest := maxUnixSeconds(conversation.GetConversationTimestamp(), conversation.GetLastMsgTimestamp())
	for _, historyMessage := range conversation.GetMessages() {
		if historyMessage != nil && historyMessage.GetMessage() != nil {
			latest = maxUnixSeconds(latest, historyMessage.GetMessage().GetMessageTimestamp())
		}
	}
	if latest == 0 || latest > math.MaxInt64 {
		return nil
	}
	value := time.Unix(int64(latest), 0).UTC()
	return &value
}

func maxUnixSeconds(values ...uint64) uint64 {
	var result uint64
	for _, value := range values {
		if value > result {
			result = value
		}
	}
	return result
}

func historySyncIdentity(data *waHistorySync.HistorySync) string {
	parts := []string{data.GetSyncType().String(), fmt.Sprint(data.GetChunkOrder()), fmt.Sprint(data.GetProgress())}
	for _, conversation := range data.GetConversations() {
		if conversation == nil {
			continue
		}
		parts = append(parts, "chat:"+conversation.GetID())
		for _, message := range conversation.GetMessages() {
			if message != nil && message.GetMessage() != nil && message.GetMessage().GetKey() != nil {
				parts = append(parts, "message:"+message.GetMessage().GetKey().GetID())
			}
		}
	}
	sort.Strings(parts[3:])
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func historyMessageStatus(status waWeb.WebMessageInfo_Status) string {
	switch status {
	case waWeb.WebMessageInfo_PENDING:
		return "pending"
	case waWeb.WebMessageInfo_SERVER_ACK:
		return "sent"
	case waWeb.WebMessageInfo_DELIVERY_ACK:
		return "delivered"
	case waWeb.WebMessageInfo_READ:
		return "read"
	case waWeb.WebMessageInfo_PLAYED:
		return "played"
	default:
		return "error"
	}
}
