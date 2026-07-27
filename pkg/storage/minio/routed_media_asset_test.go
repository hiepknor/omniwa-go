package minio_storage

import (
	"context"
	"io"
	"strings"
	"testing"
)

type routedStoreFake struct {
	puts, opens, deletes []string
	health               error
}

func (f *routedStoreFake) Put(_ context.Context, key string, _ io.Reader, _ int64, _ string) error {
	f.puts = append(f.puts, key)
	return nil
}
func (f *routedStoreFake) Open(_ context.Context, key string) (io.ReadCloser, error) {
	f.opens = append(f.opens, key)
	return io.NopCloser(strings.NewReader(key)), nil
}
func (f *routedStoreFake) Delete(_ context.Context, key string) error {
	f.deletes = append(f.deletes, key)
	return nil
}
func (f *routedStoreFake) Health(context.Context) error { return f.health }

func TestRoutedMediaAssetStoragePreservesLegacyBucketRouting(t *testing.T) {
	shared, legacy := &routedStoreFake{}, &routedStoreFake{}
	store, err := NewRoutedMediaAssetStorage(shared, legacy)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	sharedKey := "media-assets/instance/asset/canonical"
	legacyKey := "campaign-media/instance/asset/image"
	if err := store.Put(ctx, sharedKey, strings.NewReader("shared"), 6, "image/jpeg"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Open(ctx, legacyKey); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, legacyKey); err != nil {
		t.Fatal(err)
	}
	if len(shared.puts) != 1 || shared.puts[0] != sharedKey || len(legacy.opens) != 1 || legacy.opens[0] != legacyKey || len(legacy.deletes) != 1 {
		t.Fatalf("unexpected routing shared=%+v legacy=%+v", shared, legacy)
	}
	if err := store.Delete(ctx, "other/key"); err == nil {
		t.Fatal("unsupported namespace was accepted")
	}
}

func TestRoutedMediaAssetPurgeStorageAllowsOnlyRequiredNamespace(t *testing.T) {
	legacy := &routedStoreFake{}
	store, err := NewRoutedMediaAssetPurgeStorage(nil, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(context.Background(), "campaign-media/instance/asset/image"); err != nil || len(legacy.deletes) != 1 {
		t.Fatalf("legacy purge delete=%v err=%v", legacy.deletes, err)
	}
	if err := store.Delete(context.Background(), "media-assets/instance/asset/canonical"); err == nil {
		t.Fatal("missing shared purge store was accepted")
	}
}
