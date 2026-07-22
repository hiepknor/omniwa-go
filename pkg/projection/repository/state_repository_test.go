package projection_repository

import (
	"testing"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
)

func TestValidSyncStatus(t *testing.T) {
	for _, status := range []projection_model.SyncStatus{
		projection_model.SyncStatusNotStarted,
		projection_model.SyncStatusSyncing,
		projection_model.SyncStatusReady,
		projection_model.SyncStatusStale,
		projection_model.SyncStatusFailed,
	} {
		if !validSyncStatus(status) {
			t.Fatalf("status %q rejected", status)
		}
	}
	if validSyncStatus("unknown") {
		t.Fatal("unknown status accepted")
	}
}
