package projection_service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	"github.com/google/uuid"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

const (
	EventsProjectionSchemaVersion int64 = 1
	DefaultEventRetention               = 30 * 24 * time.Hour
)

type durableEventWriter interface {
	Append(context.Context, *projection_model.DurableEvent) error
}

type DurableEventService struct {
	repository durableEventWriter
	retention  time.Duration
	now        func() time.Time
}

type durableEventSummary struct {
	MessageID   string   `json:"messageId,omitempty"`
	MessageIDs  []string `json:"messageIds,omitempty"`
	ChatID      string   `json:"chatId,omitempty"`
	Direction   string   `json:"direction,omitempty"`
	MessageType string   `json:"messageType,omitempty"`
	ReceiptType string   `json:"receiptType,omitempty"`
	GroupID     string   `json:"groupId,omitempty"`
	ChangeTypes []string `json:"changeTypes,omitempty"`
	Count       int      `json:"count,omitempty"`
}

func NewDurableEventService(repository durableEventWriter, retention time.Duration) *DurableEventService {
	return &DurableEventService{repository: repository, retention: retention, now: time.Now}
}

func (s *DurableEventService) Record(ctx context.Context, instanceID, eventType string, raw any) (*projection_model.DurableEvent, error) {
	eventType = strings.TrimSpace(eventType)
	if s == nil || s.repository == nil || s.now == nil || s.retention <= 0 || ctx == nil || instanceID == "" || eventType == "" || len(eventType) > 64 {
		return nil, errors.New("durable event dependencies, identity, type, and retention are required")
	}
	now := s.now().UTC()
	occurredAt, summary := normalizeDurableEvent(raw, now)
	payload, err := json.Marshal(summary)
	if err != nil {
		return nil, err
	}
	event := &projection_model.DurableEvent{
		ID: uuid.NewString(), InstanceID: instanceID, Type: eventType,
		OccurredAt: occurredAt, IngestedAt: now, ExpiresAt: now.Add(s.retention), Summary: payload,
	}
	if err := s.repository.Append(ctx, event); err != nil {
		return nil, err
	}
	return event, nil
}

func normalizeDurableEvent(raw any, fallback time.Time) (time.Time, durableEventSummary) {
	summary := durableEventSummary{}
	switch event := raw.(type) {
	case types.MessageInfo:
		return normalizeDurableMessageInfo(event, fallback)
	case *types.MessageInfo:
		if event == nil {
			return fallback, summary
		}
		return normalizeDurableMessageInfo(*event, fallback)
	case *events.Message:
		if event == nil {
			return fallback, summary
		}
		summary.MessageID = string(event.Info.ID)
		summary.ChatID = event.Info.Chat.ToNonAD().String()
		summary.MessageType = boundedString(event.Info.Type, 64)
		if event.Info.IsFromMe {
			summary.Direction = string(projection_model.MessageDirectionOutgoing)
		} else {
			summary.Direction = string(projection_model.MessageDirectionIncoming)
		}
		return durableOccurredAt(event.Info.Timestamp, fallback), summary
	case *events.Receipt:
		if event == nil {
			return fallback, summary
		}
		summary.ChatID = event.Chat.ToNonAD().String()
		summary.ReceiptType = string(event.Type)
		summary.Count = len(event.MessageIDs)
		limit := len(event.MessageIDs)
		if limit > 100 {
			limit = 100
		}
		summary.MessageIDs = make([]string, limit)
		for index := 0; index < limit; index++ {
			summary.MessageIDs[index] = string(event.MessageIDs[index])
		}
		return durableOccurredAt(event.Timestamp, fallback), summary
	case *events.GroupInfo:
		if event == nil {
			return fallback, summary
		}
		summary.GroupID = event.JID.ToNonAD().String()
		summary.ChangeTypes = durableGroupChanges(event)
		return durableOccurredAt(event.Timestamp, fallback), summary
	case *events.JoinedGroup:
		if event == nil {
			return fallback, summary
		}
		summary.GroupID = event.JID.ToNonAD().String()
		summary.ChangeTypes = []string{"joined"}
		return durableOccurredAt(event.GroupInfo.GroupCreated, fallback), summary
	default:
		return fallback, summary
	}
}

func normalizeDurableMessageInfo(info types.MessageInfo, fallback time.Time) (time.Time, durableEventSummary) {
	summary := durableEventSummary{
		MessageID:   string(info.ID),
		ChatID:      info.Chat.ToNonAD().String(),
		MessageType: boundedString(info.Type, 64),
		Direction:   string(projection_model.MessageDirectionIncoming),
	}
	if info.IsFromMe {
		summary.Direction = string(projection_model.MessageDirectionOutgoing)
	}
	return durableOccurredAt(info.Timestamp, fallback), summary
}

func durableOccurredAt(value, fallback time.Time) time.Time {
	if value.IsZero() {
		return fallback
	}
	return value.UTC()
}

func durableGroupChanges(event *events.GroupInfo) []string {
	changes := make([]string, 0, 8)
	if event.Name != nil {
		changes = append(changes, "name")
	}
	if event.Topic != nil {
		changes = append(changes, "topic")
	}
	if event.Locked != nil {
		changes = append(changes, "locked")
	}
	if event.Announce != nil {
		changes = append(changes, "announce")
	}
	if event.Ephemeral != nil {
		changes = append(changes, "ephemeral")
	}
	if event.Delete != nil {
		changes = append(changes, "delete")
	}
	if len(event.Join)+len(event.Leave) > 0 {
		changes = append(changes, "participants")
	}
	return changes
}
