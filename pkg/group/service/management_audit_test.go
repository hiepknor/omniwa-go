package group_service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	group_model "github.com/evolution-foundation/evolution-go/pkg/group/model"
	group_repository "github.com/evolution-foundation/evolution-go/pkg/group/repository"
	"github.com/google/uuid"
)

type managementAuditRepositoryStub struct {
	page     *group_repository.ManagementPublicAuditPage
	instance string
	groupJID string
	cursor   *group_repository.ManagementAuditCursor
}

func (s *managementAuditRepositoryStub) ListPublicAudit(_ context.Context, instanceID, groupJID string, _ int, cursor *group_repository.ManagementAuditCursor) (*group_repository.ManagementPublicAuditPage, error) {
	s.instance, s.groupJID, s.cursor = instanceID, groupJID, cursor
	return s.page, nil
}

func TestManagementAuditReaderReturnsTerminalSafeEventsAndScopedCursor(t *testing.T) {
	instanceID, eventID, nextID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	at := time.Unix(500, 0).UTC()
	repository := &managementAuditRepositoryStub{page: &group_repository.ManagementPublicAuditPage{
		Items: []group_repository.ManagementAuditRecord{{
			Event:       group_model.ManagementAuditEvent{ID: eventID, EventType: "completed", ActorType: "instance", OccurredAt: at, Summary: json.RawMessage(`{"setting":"locked"}`)},
			CommandType: "settings_updated", CommandStatus: group_model.ManagementCommandCompleted,
		}},
		NextCursor: &group_repository.ManagementAuditCursor{OccurredAt: at, ID: nextID},
	}}
	reader := NewManagementAuditReader(repository)
	result, err := reader.List(context.Background(), instanceID, "123@g.us", 50, "")
	if err != nil || len(result.Items) != 1 || result.Items[0].EventType != "settings_updated" || result.Items[0].Summary.Setting == nil || *result.Items[0].Summary.Setting != "locked" || result.NextCursor == "" {
		t.Fatalf("result=%#v error=%v", result, err)
	}
	if _, err := reader.List(context.Background(), uuid.NewString(), "123@g.us", 50, result.NextCursor); !errors.Is(err, ErrInvalidManagementAuditCursor) {
		t.Fatalf("cross-instance cursor error = %v", err)
	}
	if _, err := reader.List(context.Background(), instanceID, "456@g.us", 50, result.NextCursor); !errors.Is(err, ErrInvalidManagementAuditCursor) {
		t.Fatalf("cross-group cursor error = %v", err)
	}
}
