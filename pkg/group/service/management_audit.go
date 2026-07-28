package group_service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	group_model "github.com/evolution-foundation/evolution-go/pkg/group/model"
	group_repository "github.com/evolution-foundation/evolution-go/pkg/group/repository"
	"github.com/evolution-foundation/evolution-go/pkg/utils"
	"github.com/google/uuid"
	"go.mau.fi/whatsmeow/types"
)

var ErrInvalidManagementAuditCursor = errors.New("invalid group management audit cursor")

type ManagementAuditSummary struct {
	Setting          *string `json:"setting,omitempty"`
	ParticipantCount *int    `json:"participantCount,omitempty"`
	FailureCount     *int    `json:"failureCount,omitempty"`
	Reason           *string `json:"reason,omitempty"`
}

type ManagementAuditEvent struct {
	ID            string                              `json:"id" format:"uuid"`
	EventType     string                              `json:"eventType"`
	OccurredAt    time.Time                           `json:"occurredAt"`
	ActorType     string                              `json:"actorType" enums:"instance,system"`
	CommandStatus group_model.ManagementCommandStatus `json:"commandStatus" enums:"completed,partially_completed,failed,unknown"`
	Summary       ManagementAuditSummary              `json:"summary"`
}

type ManagementAuditResult struct {
	Items      []ManagementAuditEvent
	NextCursor string
}

type managementAuditRepository interface {
	ListPublicAudit(context.Context, string, string, int, *group_repository.ManagementAuditCursor) (*group_repository.ManagementPublicAuditPage, error)
}

type ManagementAuditReader struct{ repository managementAuditRepository }

func NewManagementAuditReader(repository managementAuditRepository) *ManagementAuditReader {
	return &ManagementAuditReader{repository: repository}
}

type managementAuditCursorEnvelope struct {
	Version    int       `json:"v"`
	Kind       string    `json:"kind"`
	InstanceID string    `json:"instanceId"`
	GroupJID   string    `json:"groupJid"`
	OccurredAt time.Time `json:"occurredAt"`
	ID         string    `json:"id"`
}

func (r *ManagementAuditReader) List(ctx context.Context, instanceID, groupJID string, limit int, encodedCursor string) (*ManagementAuditResult, error) {
	jid, ok := utils.ParseJID(groupJID)
	if r == nil || r.repository == nil || ctx == nil || uuid.Validate(instanceID) != nil || !ok || jid.Server != types.GroupServer || limit < 1 || limit > 200 {
		return nil, group_repository.ErrInvalidManagementCommand
	}
	cursor, err := decodeManagementAuditCursor(encodedCursor, instanceID, jid.String())
	if err != nil {
		return nil, err
	}
	page, err := r.repository.ListPublicAudit(ctx, instanceID, jid.String(), limit, cursor)
	if err != nil {
		return nil, err
	}
	result := &ManagementAuditResult{Items: make([]ManagementAuditEvent, len(page.Items))}
	for index := range page.Items {
		record := page.Items[index]
		summary := ManagementAuditSummary{}
		_ = json.Unmarshal(record.Event.Summary, &summary)
		result.Items[index] = ManagementAuditEvent{
			ID: record.Event.ID, EventType: record.CommandType, OccurredAt: record.Event.OccurredAt.UTC(),
			ActorType: record.Event.ActorType, CommandStatus: record.CommandStatus, Summary: summary,
		}
	}
	if page.NextCursor != nil {
		result.NextCursor, err = encodeManagementAuditCursor(page.NextCursor, instanceID, jid.String())
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

func decodeManagementAuditCursor(value, instanceID, groupJID string) (*group_repository.ManagementAuditCursor, error) {
	if value == "" {
		return nil, nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, ErrInvalidManagementAuditCursor
	}
	var envelope managementAuditCursorEnvelope
	if json.Unmarshal(payload, &envelope) != nil || envelope.Version != 1 || envelope.Kind != "group_management_audit" ||
		envelope.InstanceID != instanceID || envelope.GroupJID != groupJID || envelope.OccurredAt.IsZero() || uuid.Validate(envelope.ID) != nil {
		return nil, ErrInvalidManagementAuditCursor
	}
	return &group_repository.ManagementAuditCursor{OccurredAt: envelope.OccurredAt.UTC(), ID: envelope.ID}, nil
}

func encodeManagementAuditCursor(cursor *group_repository.ManagementAuditCursor, instanceID, groupJID string) (string, error) {
	if cursor == nil || cursor.OccurredAt.IsZero() || uuid.Validate(cursor.ID) != nil || uuid.Validate(instanceID) != nil || groupJID == "" {
		return "", ErrInvalidManagementAuditCursor
	}
	payload, err := json.Marshal(managementAuditCursorEnvelope{
		Version: 1, Kind: "group_management_audit", InstanceID: instanceID, GroupJID: groupJID,
		OccurredAt: cursor.OccurredAt.UTC(), ID: cursor.ID,
	})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}
