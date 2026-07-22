package webhook_producer

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/evolution-foundation/evolution-go/pkg/netguard"
)

func TestWebhookUsesBoundedConfiguredRequester(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("unexpected request method=%s content-type=%s", r.Method, r.Header.Get("Content-Type"))
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	requester, err := netguard.NewRequester(netguard.RequestSettings{
		AllowedHosts: []string{parsed.Hostname()}, AllowedPorts: []string{parsed.Port()}, AllowPrivate: true, Timeout: time.Second,
		MaxRequestBytes: 1024, MaxResponseBytes: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	producer := &webhookProducer{requester: requester}
	if sendErr, status := producer.sendWebhook(server.URL, []byte(`{"ok":true}`)); sendErr != nil || status != http.StatusNoContent {
		t.Fatalf("sendWebhook() status=%d error=%v", status, sendErr)
	}
}

func TestWebhookRejectsUnconfiguredTarget(t *testing.T) {
	producer := &webhookProducer{}
	err, _ := producer.sendWebhook("http://"+net.IPv4(127, 0, 0, 1).String(), nil)
	if !errors.Is(err, netguard.ErrUnsafeTarget) {
		t.Fatalf("sendWebhook() error=%v, want ErrUnsafeTarget", err)
	}
}
