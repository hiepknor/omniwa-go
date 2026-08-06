package whatsmeow_service

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/evolution-foundation/evolution-go/pkg/config"
	logger_wrapper "github.com/evolution-foundation/evolution-go/pkg/logger"
	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_service "github.com/evolution-foundation/evolution-go/pkg/projection/service"
	"go.mau.fi/whatsmeow"
	waE2E "go.mau.fi/whatsmeow/proto/waE2E"
	waHistorySync "go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

type captureProjectionEventService struct {
	event *projection_model.Event
	calls int
}

type inboundCaptureFake struct {
	assetID string
	err     error
}

type blockingHistoryEvents struct {
	started chan struct{}
	release chan struct{}
}

func (f *blockingHistoryEvents) Ingest(ctx context.Context, _ *projection_model.Event) (bool, error) {
	select {
	case <-f.started:
	default:
		close(f.started)
	}
	select {
	case <-f.release:
		return true, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

type historyStateNoop struct{}

func (*historyStateNoop) Get(string, string) (*projection_model.State, error) {
	return nil, gorm.ErrRecordNotFound
}

func (*historyStateNoop) MarkSyncing(string, string, int64) error { return nil }
func (*historyStateNoop) MarkStale(string, string, int64) error   { return nil }
func (*historyStateNoop) MarkFailed(string, string, int64) error  { return nil }

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

func TestHistoryProjectionIngestionAppliesBackpressure(t *testing.T) {
	logDirectory := t.TempDir()
	loggerManager := logger_wrapper.NewLoggerManager(&config.Config{LogDirectory: logDirectory, LogMaxSize: 1, LogMaxBackups: 1, LogMaxAge: 1})
	eventsCapture := &blockingHistoryEvents{started: make(chan struct{}), release: make(chan struct{})}
	client := &MyClient{
		userID:           "instance-a",
		WAClient:         &whatsmeow.Client{},
		projectionEvents: &captureProjectionEventService{},
		historySyncer:    projection_service.NewHistorySyncer(eventsCapture, &historyStateNoop{}),
		loggerWrapper:    loggerManager,
		appCtx:           context.Background(),
	}
	raw := &events.HistorySync{Data: &waHistorySync.HistorySync{
		SyncType:      waHistorySync.HistorySync_RECENT.Enum(),
		Conversations: []*waHistorySync.Conversation{{ID: proto.String("history-provider-chat-sensitive@g.us")}},
	}}

	returned := make(chan struct{})
	go func() {
		client.myEventHandler(raw)
		close(returned)
	}()

	select {
	case <-eventsCapture.started:
	case <-time.After(time.Second):
		t.Fatal("history ingestion did not start")
	}
	select {
	case <-returned:
		t.Fatal("history callback returned before durable ingestion completed")
	default:
	}
	stateLockAvailable := make(chan struct{})
	go func() {
		client.stateMu.Lock()
		client.stateMu.Unlock()
		close(stateLockAvailable)
	}()
	select {
	case <-stateLockAvailable:
	case <-time.After(time.Second):
		close(eventsCapture.release)
		t.Fatal("history ingestion held the general event-state lock")
	}
	close(eventsCapture.release)
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("history callback did not return after durable ingestion completed")
	}
	if err := loggerManager.GetLogger("instance-a").Close(); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(logDirectory, "instance-a", "instance.log"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(contents), "history-provider-chat-sensitive") {
		t.Fatalf("history provider identity leaked into log: %s", contents)
	}
}

func TestUndecryptableMessageLogOmitsProviderPayloadAndIdentity(t *testing.T) {
	logDirectory := t.TempDir()
	loggerManager := logger_wrapper.NewLoggerManager(&config.Config{LogDirectory: logDirectory, LogMaxSize: 1, LogMaxBackups: 1, LogMaxAge: 1})
	client := &MyClient{userID: "instance-a", loggerWrapper: loggerManager}
	messageID := "provider-message-id-sensitive"
	client.myEventHandler(&events.UndecryptableMessage{
		Info:            types.MessageInfo{ID: messageID},
		UnavailableType: "ciphertext",
	})
	if err := loggerManager.GetLogger("instance-a").Close(); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(logDirectory, "instance-a", "instance.log"))
	if err != nil {
		t.Fatal(err)
	}
	logText := string(contents)
	if strings.Contains(logText, messageID) {
		t.Fatalf("provider message identity leaked into log: %s", logText)
	}
	if !strings.Contains(logText, "message_ref="+logger_wrapper.OpaqueCorrelationID(messageID)) ||
		!strings.Contains(logText, "action=undecryptable_message") {
		t.Fatalf("safe undecryptable-message diagnostics missing: %s", logText)
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
