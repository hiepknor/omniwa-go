package netguard

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRequesterEnforcesExactHostAndResponseLimit(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("12345"))
	}))
	defer server.Close()
	r := testRequester(t, server, RequestSettings{AllowedHosts: []string{"service.example"}, Timeout: time.Second, MaxRequestBytes: 8, MaxResponseBytes: 4})

	if _, err := r.Do(context.Background(), http.MethodGet, "http://other.example", nil, nil); !errors.Is(err, ErrUnsafeTarget) {
		t.Fatalf("unexpected host error = %v", err)
	}
	if _, err := r.Do(context.Background(), http.MethodGet, "http://service.example:8080", nil, nil); !errors.Is(err, ErrUnsafeTarget) {
		t.Fatalf("unexpected port error = %v", err)
	}
	if _, err := r.Do(context.Background(), http.MethodGet, "http://service.example", nil, nil); !errors.Is(err, ErrResponseLarge) {
		t.Fatalf("oversized response error = %v", err)
	}
}

func TestRequesterEnforcesConfiguredContentType(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()
	r := testRequester(t, server, RequestSettings{AllowedHosts: []string{"service.example"}, AllowedContentTypes: []string{"application/json"}, Timeout: time.Second, MaxRequestBytes: 1, MaxResponseBytes: 8})
	if _, err := r.Do(context.Background(), http.MethodGet, "http://service.example", nil, nil); err == nil {
		t.Fatal("unexpected content type succeeded")
	}
}

func TestRequesterRejectsPrivateRedirectAndOversizedRequest(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "http://127.0.0.1/internal")
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()
	r := testRequester(t, server, RequestSettings{AllowedHosts: []string{"service.example", "127.0.0.1"}, Timeout: time.Second, MaxRequestBytes: 4, MaxResponseBytes: 1024})

	if _, err := r.Do(context.Background(), http.MethodPost, "http://service.example", nil, []byte("12345")); err == nil {
		t.Fatal("oversized request unexpectedly succeeded")
	}
	if _, err := r.Do(context.Background(), http.MethodGet, "http://service.example", nil, nil); !errors.Is(err, ErrUnsafeTarget) {
		t.Fatalf("private redirect error = %v", err)
	}
}

func TestRequesterAllowsExplicitPrivateConfiguredHost(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	address := server.Listener.Addr().String()
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewRequester(RequestSettings{AllowedHosts: []string{host}, AllowedPorts: []string{port}, AllowPrivate: true, Timeout: time.Second, MaxRequestBytes: 1, MaxResponseBytes: 1})
	if err != nil {
		t.Fatal(err)
	}
	response, err := r.Do(context.Background(), http.MethodGet, server.URL, nil, nil)
	if err != nil || response.StatusCode != http.StatusNoContent {
		t.Fatalf("private configured request response=%v error=%v", response, err)
	}
}

func TestRequesterTimesOutSlowDependency(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	r := testRequester(t, server, RequestSettings{AllowedHosts: []string{"service.example"}, Timeout: 20 * time.Millisecond, MaxRequestBytes: 1, MaxResponseBytes: 1})
	if _, err := r.Do(context.Background(), http.MethodGet, "http://service.example", nil, nil); err == nil {
		t.Fatal("slow dependency unexpectedly succeeded")
	}
}

func TestRequesterRejectsInvalidPortConfiguration(t *testing.T) {
	t.Parallel()
	_, err := NewRequester(RequestSettings{AllowedHosts: []string{"service.example"}, AllowedPorts: []string{"70000"}, Timeout: time.Second, MaxRequestBytes: 1, MaxResponseBytes: 1})
	if err == nil {
		t.Fatal("invalid port configuration unexpectedly succeeded")
	}
}

func testRequester(t *testing.T, server *httptest.Server, settings RequestSettings) *requester {
	t.Helper()
	serverAddress := server.Listener.Addr().String()
	resolve := func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("1.1.1.1")}}, nil
	}
	dial := func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, serverAddress)
	}
	r, err := newRequester(settings, resolve, dial)
	if err != nil {
		t.Fatalf("newRequester() error = %v", err)
	}
	return r
}
