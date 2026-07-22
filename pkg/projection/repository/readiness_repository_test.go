package projection_repository

import "testing"

func TestReadinessRepositoryRejectsIncompleteQuery(t *testing.T) {
	repository := &readinessRepository{}
	if _, err := repository.HasUnprocessedEvents(t.Context(), "", "labels", []string{"label_edit"}, "sync-event"); err == nil {
		t.Fatal("readiness query without instance was accepted")
	}
	if _, err := repository.HasUnprocessedEvents(t.Context(), "instance-a", "labels", nil, "sync-event"); err == nil {
		t.Fatal("readiness query without event types was accepted")
	}
}
