package websocket_producer

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/evolution-foundation/evolution-go/pkg/httpapi"
	"github.com/gorilla/websocket"
)

// testWSHandler mirrors the /ws handler wiring in cmd/evolution-go/main.go:
// it reads the token from Sec-WebSocket-Protocol, rejects a mismatch with 401,
// and otherwise upgrades the connection via ServeWs.
func testWSHandler(expectedToken string, producer *websocketProducer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := TokenFromProtocolHeader(r.Header.Get("Sec-WebSocket-Protocol"))
		if token != expectedToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		ServeWs(w, r, r.URL.Query().Get("instanceId"), producer)
	}
}

func TestServeWs_SubprotocolHandshake(t *testing.T) {
	const token = "test-key"

	// nil loggerWrapper is safe here: we exercise the broadcast path
	// (no instanceId), which only touches the package-level logger.
	producer := NewWebsocketProducer(nil)
	srv := httptest.NewServer(testWSHandler(token, producer))
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	t.Run("valid token upgrades and echoes only the apikey scheme", func(t *testing.T) {
		dialer := websocket.Dialer{Subprotocols: []string{"apikey", token}}
		conn, resp, err := dialer.Dial(wsURL, nil)
		if err != nil {
			t.Fatalf("dial failed: %v", err)
		}
		defer conn.Close()

		if resp.StatusCode != http.StatusSwitchingProtocols {
			t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusSwitchingProtocols)
		}
		if got := conn.Subprotocol(); got != "apikey" {
			t.Errorf("negotiated subprotocol = %q, want %q", got, "apikey")
		}
		// The token must never be reflected back in the handshake response.
		if echoed := resp.Header.Get("Sec-WebSocket-Protocol"); strings.Contains(echoed, token) {
			t.Errorf("response Sec-WebSocket-Protocol %q leaked the token", echoed)
		}
	})

	t.Run("wrong token is rejected with 401", func(t *testing.T) {
		dialer := websocket.Dialer{Subprotocols: []string{"apikey", "wrong-token"}}
		conn, resp, err := dialer.Dial(wsURL, nil)
		if err == nil {
			conn.Close()
			t.Fatal("expected handshake to fail, but it succeeded")
		}
		if resp == nil {
			t.Fatalf("expected an HTTP response, got err only: %v", err)
		}
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
		}
	})

	t.Run("token in query string no longer authenticates", func(t *testing.T) {
		// Old-style clients that pass ?token= without the subprotocol must fail.
		dialer := websocket.Dialer{}
		conn, resp, err := dialer.Dial(wsURL+"?token="+token, nil)
		if err == nil {
			conn.Close()
			t.Fatal("expected query-string token to be rejected, but handshake succeeded")
		}
		if resp == nil || resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected 401 for query-string token, got resp=%v err=%v", resp, err)
		}
	})
}

func TestServeWsEnforcesConfiguredOriginPolicy(t *testing.T) {
	const token = "test-key"
	policy, err := httpapi.NewOriginPolicy([]string{"https://console.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	producer := NewWebsocketProducer(nil, policy)
	server := httptest.NewServer(testWSHandler(token, producer))
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	dialer := websocket.Dialer{Subprotocols: []string{"apikey", token}}

	rejectedHeader := http.Header{"Origin": []string{"https://evil.example"}}
	connection, response, err := dialer.Dial(wsURL, rejectedHeader)
	if err == nil {
		connection.Close()
		t.Fatal("disallowed origin upgraded successfully")
	}
	if response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("disallowed origin response=%v error=%v", response, err)
	}

	allowedHeader := http.Header{"Origin": []string{"https://console.example.com"}}
	connection, response, err = dialer.Dial(wsURL, allowedHeader)
	if err != nil {
		t.Fatalf("allowed origin failed: response=%v error=%v", response, err)
	}
	connection.Close()
}
