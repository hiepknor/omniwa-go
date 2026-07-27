package media_service

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	media_model "github.com/evolution-foundation/evolution-go/pkg/media/model"
)

type cleanupRepositoryFake struct {
	assets      []media_model.Asset
	variants    map[string][]media_model.AssetVariant
	claimErr    error
	listErr     error
	completeErr error
	releaseErr  error
	completed   []string
	released    []string
}

func (f *cleanupRepositoryFake) ClaimExpired(context.Context, int, time.Duration) ([]media_model.Asset, error) {
	return append([]media_model.Asset(nil), f.assets...), f.claimErr
}
func (f *cleanupRepositoryFake) ListVariants(_ context.Context, _, assetID string) ([]media_model.AssetVariant, error) {
	return append([]media_model.AssetVariant(nil), f.variants[assetID]...), f.listErr
}
func (f *cleanupRepositoryFake) CompleteCleanup(_ context.Context, asset *media_model.Asset) error {
	if f.completeErr == nil {
		f.completed = append(f.completed, asset.ID)
	}
	return f.completeErr
}
func (f *cleanupRepositoryFake) ReleaseCleanup(_ context.Context, asset *media_model.Asset) error {
	f.released = append(f.released, asset.ID)
	return f.releaseErr
}

type cleanupStoreFake struct {
	failKey string
	deleted []string
}

func (f *cleanupStoreFake) Put(context.Context, string, io.Reader, int64, string) error { return nil }
func (f *cleanupStoreFake) Open(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (f *cleanupStoreFake) Delete(_ context.Context, key string) error {
	if key == f.failKey {
		return errors.New("storage unavailable")
	}
	f.deleted = append(f.deleted, key)
	return nil
}
func (f *cleanupStoreFake) Health(context.Context) error { return nil }

func TestCleanupWorkerDeletesAllVariantsBeforeCompleting(t *testing.T) {
	token := "11111111-1111-4111-8111-111111111111"
	repository := &cleanupRepositoryFake{
		assets: []media_model.Asset{{ID: "asset-1", InstanceID: "instance-1", CleanupClaimToken: &token}},
		variants: map[string][]media_model.AssetVariant{"asset-1": {
			{ObjectKey: "original"}, {ObjectKey: "canonical"},
		}},
	}
	store := &cleanupStoreFake{}
	worker := NewCleanupWorker(repository, store, 10, time.Minute, time.Minute, nil)
	cleaned, err := worker.RunOnce(context.Background())
	if err != nil || cleaned != 1 || len(store.deleted) != 2 || len(repository.completed) != 1 || len(repository.released) != 0 {
		t.Fatalf("cleanup result cleaned=%d err=%v deleted=%v completed=%v released=%v", cleaned, err, store.deleted, repository.completed, repository.released)
	}
}

func TestCleanupWorkerReleasesClaimAfterObjectFailure(t *testing.T) {
	token := "11111111-1111-4111-8111-111111111111"
	repository := &cleanupRepositoryFake{
		assets:   []media_model.Asset{{ID: "asset-1", InstanceID: "instance-1", CleanupClaimToken: &token}},
		variants: map[string][]media_model.AssetVariant{"asset-1": {{ObjectKey: "canonical"}}},
	}
	worker := NewCleanupWorker(repository, &cleanupStoreFake{failKey: "canonical"}, 10, time.Minute, time.Minute, nil)
	cleaned, err := worker.RunOnce(context.Background())
	if err == nil || cleaned != 0 || len(repository.completed) != 0 || len(repository.released) != 1 {
		t.Fatalf("failure result cleaned=%d err=%v completed=%v released=%v", cleaned, err, repository.completed, repository.released)
	}
}

func TestCleanupWorkerReportsEverySweep(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reported := make(chan error, 1)
	worker := NewCleanupWorker(&cleanupRepositoryFake{claimErr: errors.New("database unavailable")}, &cleanupStoreFake{}, 1, time.Minute, time.Millisecond,
		func(_ int, err error) {
			reported <- err
			cancel()
		})
	if err := worker.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-reported; err == nil {
		t.Fatal("cleanup sweep error was not reported")
	}
}
