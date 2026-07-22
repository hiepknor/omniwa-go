package websocket_producer

import (
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestTokenFromProtocolHeader(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   string
	}{
		{"valid with space", "apikey, secret123", "secret123"},
		{"valid without space", "apikey,secret123", "secret123"},
		{"token surrounded by spaces is trimmed", "apikey,  secret123 ", "secret123"},
		{"extra protocols after token are ignored", "apikey, secret123, foo", "secret123"},
		{"empty header", "", ""},
		{"only the scheme, no token", "apikey", ""},
		{"scheme present but token empty", "apikey, ", ""},
		{"wrong scheme", "bearer, secret123", ""},
		{"scheme not first", "foo, apikey, secret123", ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := TokenFromProtocolHeader(c.header); got != c.want {
				t.Errorf("TokenFromProtocolHeader(%q) = %q, want %q", c.header, got, c.want)
			}
		})
	}
}

func TestInstanceSessionsDoNotReplaceEachOther(t *testing.T) {
	const token = "test-admin-key"
	producer := NewWebsocketProducer(nil)
	server := httptest.NewServer(testWSHandler(token, producer))
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "?instanceId=instance-a"

	first := dialTestWebsocket(t, wsURL, token)
	defer first.Close()
	second := dialTestWebsocket(t, wsURL, token)
	defer second.Close()
	waitForSessionCount(t, producer, "instance-a", 2)

	if err := producer.Produce("Message", []byte(`{"id":"one"}`), "instance-a", ""); err != nil {
		t.Fatal(err)
	}
	assertWebsocketEvent(t, first, "message", `{"id":"one"}`)
	assertWebsocketEvent(t, second, "message", `{"id":"one"}`)

	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	waitForSessionCount(t, producer, "instance-a", 1)
	if err := producer.Produce("Message", []byte(`{"id":"two"}`), "instance-a", ""); err != nil {
		t.Fatal(err)
	}
	assertWebsocketEvent(t, second, "message", `{"id":"two"}`)
}

func TestBroadcastAndInstanceSessionsBothReceiveEvents(t *testing.T) {
	const token = "test-admin-key"
	producer := NewWebsocketProducer(nil)
	server := httptest.NewServer(testWSHandler(token, producer))
	defer server.Close()
	baseURL := "ws" + strings.TrimPrefix(server.URL, "http")

	broadcast := dialTestWebsocket(t, baseURL, token)
	defer broadcast.Close()
	instance := dialTestWebsocket(t, baseURL+"?instanceId=instance-a", token)
	defer instance.Close()
	waitForSessionCount(t, producer, "", 1)
	waitForSessionCount(t, producer, "instance-a", 1)

	if err := producer.Produce("Connected", []byte(`{"status":"open"}`), "instance-a", ""); err != nil {
		t.Fatal(err)
	}
	assertWebsocketEvent(t, broadcast, "connected", `{"status":"open"}`)
	assertWebsocketEvent(t, instance, "connected", `{"status":"open"}`)
}

func TestSlowSessionIsDisconnectedWithoutBlockingProducer(t *testing.T) {
	producer := NewWebsocketProducer(nil)
	slow := &websocketSession{
		id:         1,
		instanceID: "instance-a",
		send:       make(chan []byte, 1),
		done:       make(chan struct{}),
		producer:   producer,
	}
	healthy := &websocketSession{
		id:         2,
		instanceID: "instance-a",
		send:       make(chan []byte, 1),
		done:       make(chan struct{}),
		producer:   producer,
	}
	producer.clients["instance-a"] = map[uint64]*websocketSession{slow.id: slow, healthy.id: healthy}
	slow.send <- []byte("already full")

	started := time.Now()
	if err := producer.Produce("Message", []byte(`{}`), "instance-a", ""); err != nil {
		t.Fatal(err)
	}
	if time.Since(started) > 100*time.Millisecond {
		t.Fatal("Produce blocked on a full session queue")
	}
	select {
	case <-slow.done:
	default:
		t.Fatal("slow session was not closed")
	}
	select {
	case <-healthy.send:
	default:
		t.Fatal("healthy session did not receive the event")
	}
	if got := producer.sessionCount("instance-a"); got != 1 {
		t.Fatalf("session count=%d, want 1", got)
	}
}

func TestConcurrentProduceUsesOneSessionWriter(t *testing.T) {
	const token = "test-admin-key"
	producer := NewWebsocketProducer(nil)
	server := httptest.NewServer(testWSHandler(token, producer))
	defer server.Close()
	conn := dialTestWebsocket(t, "ws"+strings.TrimPrefix(server.URL, "http")+"?instanceId=instance-a", token)
	defer conn.Close()
	waitForSessionCount(t, producer, "instance-a", 1)

	const events = 100
	var wait sync.WaitGroup
	wait.Add(events)
	for range events {
		go func() {
			defer wait.Done()
			if err := producer.Produce("Message", []byte(`{}`), "instance-a", ""); err != nil {
				t.Errorf("Produce() error=%v", err)
			}
		}()
	}
	wait.Wait()
	for range events {
		assertWebsocketEvent(t, conn, "message", `{}`)
	}
}

func TestCloseTerminatesAllSessions(t *testing.T) {
	const token = "test-admin-key"
	producer := NewWebsocketProducer(nil)
	server := httptest.NewServer(testWSHandler(token, producer))
	defer server.Close()
	baseURL := "ws" + strings.TrimPrefix(server.URL, "http")
	first := dialTestWebsocket(t, baseURL, token)
	defer first.Close()
	second := dialTestWebsocket(t, baseURL+"?instanceId=instance-a", token)
	defer second.Close()
	waitForSessionCount(t, producer, "", 1)
	waitForSessionCount(t, producer, "instance-a", 1)

	producer.Close()
	if got := producer.sessionCount(""); got != 0 {
		t.Fatalf("broadcast session count=%d, want 0", got)
	}
	if got := producer.sessionCount("instance-a"); got != 0 {
		t.Fatalf("instance session count=%d, want 0", got)
	}
	if err := first.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := first.ReadMessage(); err == nil {
		t.Fatal("broadcast connection remained open after Close")
	}
	producer.Close()
}

func dialTestWebsocket(t *testing.T, url, token string) *websocket.Conn {
	t.Helper()
	dialer := websocket.Dialer{Subprotocols: []string{"apikey", token}}
	conn, _, err := dialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	return conn
}

func assertWebsocketEvent(t *testing.T, conn *websocket.Conn, queue, payload string) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var event struct {
		Queue   string `json:"queue"`
		Payload string `json:"payload"`
	}
	if err := conn.ReadJSON(&event); err != nil {
		t.Fatalf("read websocket event: %v", err)
	}
	if event.Queue != queue || event.Payload != payload {
		t.Fatalf("event=%+v, want queue=%q payload=%q", event, queue, payload)
	}
}

func waitForSessionCount(t *testing.T, producer *websocketProducer, instanceID string, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if producer.sessionCount(instanceID) == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("session count for %q=%d, want %d", instanceID, producer.sessionCount(instanceID), want)
}

func (p *websocketProducer) sessionCount(instanceID string) int {
	p.clientsMux.RLock()
	defer p.clientsMux.RUnlock()
	if instanceID == "" {
		return len(p.broadcast)
	}
	return len(p.clients[instanceID])
}
