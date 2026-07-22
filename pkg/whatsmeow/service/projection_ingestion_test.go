package whatsmeow_service

import (
	"context"
	"testing"
	"time"

	"github.com/evolution-foundation/evolution-go/pkg/config"
	logger_wrapper "github.com/evolution-foundation/evolution-go/pkg/logger"
	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_service "github.com/evolution-foundation/evolution-go/pkg/projection/service"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

type captureProjectionEventService struct {
	event *projection_model.Event
	calls int
}

func (s *captureProjectionEventService) Ingest(_ context.Context, event *projection_model.Event) (bool, error) {
	s.calls++
	s.event = event
	return true, nil
}

func (s *captureProjectionEventService) ProcessBatch(context.Context, int, projection_service.EventHandler) (projection_service.EventBatchResult, error) {
	return projection_service.EventBatchResult{}, nil
}

func (s *captureProjectionEventService) ProcessBatchFor(context.Context, string, []string, int, projection_service.EventHandler) (projection_service.EventBatchResult, error) {
	return projection_service.EventBatchResult{}, nil
}

func TestMyClientIngestsRelevantGroupEvents(t *testing.T) {
	loggerManager := logger_wrapper.NewLoggerManager(&config.Config{LogDirectory: t.TempDir(), LogMaxSize: 1, LogMaxBackups: 1, LogMaxAge: 1})
	defer loggerManager.GetLogger("instance-a").Close()
	capture := &captureProjectionEventService{}
	client := &MyClient{userID: "instance-a", projectionEvents: capture, loggerWrapper: loggerManager}
	raw := &events.GroupInfo{JID: types.NewJID("12345", types.GroupServer), Timestamp: time.Now()}

	client.ingestProjectionEvent(raw)

	if capture.calls != 1 || capture.event == nil || capture.event.InstanceID != "instance-a" || capture.event.Resource != "groups" {
		t.Fatalf("projection event was not ingested: calls=%d event=%#v", capture.calls, capture.event)
	}
	client.ingestProjectionEvent(struct{}{})
	if capture.calls != 1 {
		t.Fatalf("unrelated event reached projection inbox: calls=%d", capture.calls)
	}
}
