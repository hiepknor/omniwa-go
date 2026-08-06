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

const maxHistoryPushNameBytes = 4096

type historyPushNameCandidate struct {
	jid        types.JID
	jidAlt     types.JID
	name       string
	occurredAt time.Time
}

// HistorySyncFailureDetails is safe to include in operational logs. It identifies
// the failing stage and ordinal without exposing provider identities or payloads.
type HistorySyncFailureDetails struct {
	Stage             string
	Code              string
	ConversationIndex int
	MessageIndex      int
}

type historySyncFailure struct {
	details HistorySyncFailureDetails
	cause   error
}

func (e *historySyncFailure) Error() string { return e.details.Code }
func (e *historySyncFailure) Unwrap() error { return e.cause }

func DescribeHistorySyncFailure(err error) HistorySyncFailureDetails {
	details := HistorySyncFailureDetails{Stage: "unknown", Code: "history_sync_failed", ConversationIndex: -1, MessageIndex: -1}
	var failure *historySyncFailure
	if errors.As(err, &failure) {
		return failure.details
	}
	return details
}

func newHistorySyncFailure(stage, code string, conversationIndex, messageIndex int, cause error) error {
	if cause == nil {
		cause = errors.New(code)
	}
	return &historySyncFailure{details: HistorySyncFailureDetails{
		Stage: stage, Code: code, ConversationIndex: conversationIndex, MessageIndex: messageIndex,
	}, cause: cause}
}

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
			return newHistorySyncFailure("state_transition", "history_sync_state_transition_failed", -1, -1, err)
		}
	}
	syncID := historySyncIdentity(raw.Data)
	for conversationIndex, conversation := range raw.Data.GetConversations() {
		if err := s.ingestConversation(ctx, instanceID, syncID, syncType, conversationIndex, conversation, parser); err != nil {
			return s.fail(instanceID, resources, err)
		}
	}
	if raw.Data.Progress != nil && raw.Data.GetProgress() >= 100 &&
		(historySyncCompletesChats(syncType) || historySyncCompletesMessages(syncType)) {
		if err := s.ingestCompletion(ctx, instanceID, syncID, syncType); err != nil {
			return s.fail(instanceID, resources, newHistorySyncFailure("completion", "history_sync_completion_failed", -1, -1, err))
		}
	}
	return nil
}

func (s *HistorySyncer) ingestConversation(ctx context.Context, instanceID, syncID string, syncType waHistorySync.HistorySync_HistorySyncType, conversationIndex int, conversation *waHistorySync.Conversation, parser HistoryMessageParser) error {
	if conversation == nil || conversation.GetID() == "" {
		return newHistorySyncFailure("conversation", "history_sync_conversation_identity_missing", conversationIndex, -1, nil)
	}
	chatJID, err := types.ParseJID(conversation.GetID())
	if err != nil || chatJID.IsEmpty() {
		return newHistorySyncFailure("conversation", "history_sync_conversation_identity_invalid", conversationIndex, -1, err)
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
		DisappearingTimer: conversation.EphemeralExpiration, LastActivityAt: lastActivityAt,
	}
	// INITIAL_BOOTSTRAP completes chats but not message history. Only RECENT/FULL
	// snapshots may participate in authoritative message-level unread recovery.
	if historySyncCompletesMessages(syncType) {
		payload.HistorySyncID = &syncID
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
		return newHistorySyncFailure("conversation", "history_sync_chat_event_invalid", conversationIndex, -1, err)
	}
	if _, err := s.events.Ingest(ctx, chatEvent); err != nil {
		return newHistorySyncFailure("conversation", "history_sync_chat_ingest_failed", conversationIndex, -1, err)
	}
	pushNames := make(map[string]historyPushNameCandidate)
	for messageIndex, historyMessage := range conversation.GetMessages() {
		if historyMessage == nil || historyMessage.GetMessage() == nil {
			return newHistorySyncFailure("message", "history_sync_message_missing", conversationIndex, messageIndex, nil)
		}
		source := historyMessage.GetMessage()
		// History sync conversations may contain metadata-only WebMessageInfo
		// stubs. They have no WhatsApp message payload to project and must not
		// prevent an otherwise complete authoritative snapshot from reaching its
		// completion barrier. Records that do carry a payload remain fail-closed
		// through parsing, normalization, and durable ingestion below.
		if source.GetMessage() == nil {
			continue
		}
		parsed, err := parser(chatJID, source)
		if err != nil {
			return newHistorySyncFailure("message_parse", "history_sync_message_parse_failed", conversationIndex, messageIndex, err)
		}
		if err := s.ingestMessage(ctx, instanceID, syncID, parsed, source); err != nil {
			return newHistorySyncFailure("message_ingest", "history_sync_message_ingest_failed", conversationIndex, messageIndex, err)
		}
		collectHistoryPushName(pushNames, parsed, source)
	}
	if err := s.ingestHistoryPushNames(ctx, instanceID, pushNames); err != nil {
		return newHistorySyncFailure("contact_enrichment", "history_sync_contact_enrichment_failed", conversationIndex, -1, err)
	}
	return nil
}

func collectHistoryPushName(candidates map[string]historyPushNameCandidate, parsed *events.Message, source *waWeb.WebMessageInfo) {
	if candidates == nil || parsed == nil || source == nil || parsed.Info.IsFromMe || parsed.Info.Sender.IsEmpty() {
		return
	}
	name := strings.TrimSpace(source.GetPushName())
	if name == "" || name == "-" || len(name) > maxHistoryPushNameBytes {
		return
	}
	jid := parsed.Info.Sender.ToNonAD()
	if !isContactJID(jid) {
		return
	}
	occurredAt := parsed.Info.Timestamp.UTC()
	key := jid.String()
	candidate := historyPushNameCandidate{
		jid: jid, jidAlt: parsed.Info.SenderAlt.ToNonAD(), name: name, occurredAt: occurredAt,
	}
	current, exists := candidates[key]
	if !exists || current.occurredAt.Before(occurredAt) || (current.occurredAt.Equal(occurredAt) && current.name < name) {
		candidates[key] = candidate
	}
}

func (s *HistorySyncer) ingestHistoryPushNames(ctx context.Context, instanceID string, candidates map[string]historyPushNameCandidate) error {
	keys := make([]string, 0, len(candidates))
	for key := range candidates {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		candidate := candidates[key]
		event, relevant, err := NormalizeContactEvent(instanceID, &events.PushName{
			JID: candidate.jid, JIDAlt: candidate.jidAlt,
			Message: &types.MessageInfo{Timestamp: candidate.occurredAt}, NewPushName: candidate.name,
		})
		if err != nil {
			return err
		}
		if !relevant || event == nil {
			continue
		}
		if _, err := s.events.Ingest(ctx, event); err != nil {
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
