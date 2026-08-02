package bootstrap

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestStandbyRuntimeControlPlaneAndLifecycle(t *testing.T) {
	state := NewProcessState(nil)
	runtime, err := NewStandbyRuntime(state)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := runtime.Start()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot := state.Snapshot(); snapshot.Role != ProcessRoleStandby {
		t.Fatalf("role after start = %q", snapshot.Role)
	}

	assertStandbyResponse(t, handler, "/server/ok", http.StatusOK, "ok")
	assertStandbyResponse(t, handler, "/server/live", http.StatusOK, "ok")
	assertStandbyResponse(t, handler, "/server/ready", http.StatusServiceUnavailable, "not_ready")

	request := httptest.NewRequest(http.MethodGet, "/server/capabilities", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("data-plane route status = %d", response.Code)
	}

	if err := runtime.BeginDrain(); err != nil {
		t.Fatal(err)
	}
	if snapshot := state.Snapshot(); snapshot.Role != ProcessRoleDraining {
		t.Fatalf("role after drain = %q", snapshot.Role)
	}
	assertStandbyResponse(t, handler, "/server/live", http.StatusOK, "ok")
	assertStandbyResponse(t, handler, "/server/ready", http.StatusServiceUnavailable, "not_ready")
	if err := runtime.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Stop(); err != nil {
		t.Fatalf("repeated stop: %v", err)
	}
	if snapshot := state.Snapshot(); snapshot.Role != ProcessRoleTerminated {
		t.Fatalf("role after stop = %q", snapshot.Role)
	}
	assertStandbyResponse(t, handler, "/server/live", http.StatusServiceUnavailable, "not_live")
}

func TestStandbyRuntimeStartIsSingleAttempt(t *testing.T) {
	runtime, err := NewStandbyRuntime(NewProcessState(nil))
	if err != nil {
		t.Fatal(err)
	}
	const callers = 32
	var wait sync.WaitGroup
	results := make(chan error, callers)
	for index := 0; index < callers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, startErr := runtime.Start()
			results <- startErr
		}()
	}
	wait.Wait()
	close(results)
	successes := 0
	for result := range results {
		if result == nil {
			successes++
			continue
		}
		if !errors.Is(result, ErrStandbyRuntimeStarted) {
			t.Fatalf("unexpected start error: %v", result)
		}
	}
	if successes != 1 {
		t.Fatalf("successful starts = %d; want 1", successes)
	}
}

func TestStandbyControlPlaneRejectsNonGET(t *testing.T) {
	runtime, _ := NewStandbyRuntime(NewProcessState(nil))
	handler, _ := runtime.Start()
	request := httptest.NewRequest(http.MethodPost, "/server/live", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d; want %d", response.Code, http.StatusMethodNotAllowed)
	}
}

func assertStandbyResponse(t *testing.T, handler http.Handler, path string, wantCode int, wantStatus string) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != wantCode {
		t.Fatalf("GET %s status = %d; want %d", path, response.Code, wantCode)
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("GET %s body: %v", path, err)
	}
	if body.Status != wantStatus {
		t.Fatalf("GET %s status body = %q; want %q", path, body.Status, wantStatus)
	}
	if path != "/server/ok" && response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("GET %s missing no-store", path)
	}
}
