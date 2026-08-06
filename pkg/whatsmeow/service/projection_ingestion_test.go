package whatsmeow_service

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/evolution-foundation/evolution-go/pkg/config"
	logger_wrapper "github.com/evolution-foundation/evolution-go/pkg/logger"
	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_service "github.com/evolution-foundation/evolution-go/pkg/projection/service"
	waE2E "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

type captureProjectionEventService struct {
	event *projection_model.Event
	calls int
}

type inboundCaptureFake struct {
	assetID string
	err     error
}

func (f *inboundCaptureFake) Capture(context.Context, string, any) (string, bool, error) {
	return f.assetID, true, f.err
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

func TestMyClientAttachesOpaqueInboundAssetAndFailsOpen(t *testing.T) {
	loggerManager := logger_wrapper.NewLoggerManager(&config.Config{LogDirectory: t.TempDir(), LogMaxSize: 1, LogMaxBackups: 1, LogMaxAge: 1})
	defer loggerManager.GetLogger("instance-a").Close()
	raw := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: types.NewJID("15550001", types.DefaultUserServer)},
			ID:            "message-image", Type: "image", Timestamp: time.Now().UTC(),
		},
		Message: &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Mimetype: proto.String("image/png")}},
	}
	assetID := "927beb51-46c2-4331-b3b4-d96f67280bd3"
	capture := &captureProjectionEventService{}
	client := &MyClient{userID: "instance-a", projectionEvents: capture, inboundMedia: &inboundCaptureFake{assetID: assetID}, loggerWrapper: loggerManager}
	client.ingestProjectionEvent(raw)
	if capture.calls != 1 || capture.event == nil || !bytes.Contains(capture.event.Payload, []byte(assetID)) {
		t.Fatalf("opaque asset was not attached: calls=%d event=%+v", capture.calls, capture.event)
	}

	capture = &captureProjectionEventService{}
	client.projectionEvents = capture
	client.inboundMedia = &inboundCaptureFake{err: errors.New("database unavailable")}
	client.ingestProjectionEvent(raw)
	if capture.calls != 1 || capture.event == nil || bytes.Contains(capture.event.Payload, []byte("mediaAssetId")) {
		t.Fatalf("capture failure blocked or polluted projection: calls=%d event=%+v", capture.calls, capture.event)
	}
}

func TestFullSyncAppStateEventsAreSuppressedFromLegacyFanout(t *testing.T) {
	client := &MyClient{}
	if !client.handleFullSyncAppStateEvent(&events.Pin{FromFullSync: true}) {
		t.Fatal("full-sync app-state event was not suppressed")
	}
	if client.handleFullSyncAppStateEvent(&events.Pin{FromFullSync: false}) {
		t.Fatal("live app-state event was suppressed")
	}
}

func TestContactSnapshotUsesIndependentBoundedBudgetAndSafeFailureCodes(t *testing.T) {
	if contactSnapshotSyncTimeout <= contactProjectionSyncTimeout {
		t.Fatalf("contact snapshot timeout %s must exceed setup timeout %s", contactSnapshotSyncTimeout, contactProjectionSyncTimeout)
	}
	for _, test := range []struct {
		name string
		err  error
		want string
	}{
		{name: "deadline", err: context.DeadlineExceeded, want: "snapshot_timeout"},
		{name: "wrapped deadline", err: errors.Join(errors.New("apply contact"), context.DeadlineExceeded), want: "snapshot_timeout"},
		{name: "canceled", err: context.Canceled, want: "snapshot_canceled"},
		{name: "provider or storage", err: errors.New("snapshot failed"), want: "snapshot_failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := contactSnapshotFailureCode(test.err); got != test.want {
				t.Fatalf("failure code = %q, want %q", got, test.want)
			}
		})
	}
}
