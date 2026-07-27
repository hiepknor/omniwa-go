package minio_storage

import (
	"bytes"
	"context"
	"io"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

func TestValidMediaAssetObjectKey(t *testing.T) {
	valid := []string{
		"media-assets/11111111-1111-4111-8111-111111111111/22222222-2222-4222-8222-222222222222/canonical",
		"media-assets/11111111-1111-4111-8111-111111111111/22222222-2222-4222-8222-222222222222/provider_original",
	}
	for _, key := range valid {
		if !validMediaAssetObjectKey(key) {
			t.Fatalf("valid key rejected: %s", key)
		}
	}
	invalid := []string{
		"campaign-media/11111111-1111-4111-8111-111111111111/22222222-2222-4222-8222-222222222222/image",
		"media-assets/not-a-uuid/22222222-2222-4222-8222-222222222222/canonical",
		"media-assets/11111111-1111-4111-8111-111111111111/22222222-2222-4222-8222-222222222222/other",
		"media-assets/11111111-1111-4111-8111-111111111111/../canonical",
		"media-assets/11111111-1111-4111-8111-111111111111/22222222-2222-4222-8222-222222222222/canonical/extra",
	}
	for _, key := range invalid {
		if validMediaAssetObjectKey(key) {
			t.Fatalf("unsafe key accepted: %s", key)
		}
	}
}

func TestValidImageMIME(t *testing.T) {
	if !validImageMIME("image/jpeg") || !validImageMIME("image/png") {
		t.Fatal("supported image MIME rejected")
	}
	if validImageMIME("image/gif") || validImageMIME("IMAGE/JPEG") {
		t.Fatal("unsupported image MIME accepted")
	}
}

func TestMediaAssetStorageIntegrationIsPrivate(t *testing.T) {
	endpoint := os.Getenv("TEST_MINIO_ENDPOINT")
	if endpoint == "" {
		t.Skip("TEST_MINIO_ENDPOINT is not set")
	}
	ctx := context.Background()
	bucket := os.Getenv("TEST_MINIO_BUCKET")
	store, err := NewMediaAssetStorage(
		ctx, endpoint, os.Getenv("TEST_MINIO_ACCESS_KEY"), os.Getenv("TEST_MINIO_SECRET_KEY"),
		bucket, os.Getenv("TEST_MINIO_REGION"), false,
	)
	if err != nil {
		t.Fatal(err)
	}
	key := "media-assets/" + uuid.NewString() + "/" + uuid.NewString() + "/canonical"
	t.Cleanup(func() { _ = store.Delete(context.Background(), key) })
	want := []byte("normalized-image-placeholder")
	if err := store.Put(ctx, key, bytes.NewReader(want), int64(len(want)), "image/jpeg"); err != nil {
		t.Fatal(err)
	}
	object, err := store.Open(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	got, readErr := io.ReadAll(object)
	_ = object.Close()
	if readErr != nil || !bytes.Equal(got, want) {
		t.Fatalf("read=%q err=%v", got, readErr)
	}

	anonymous, err := minio.New(endpoint, &minio.Options{Creds: credentials.NewStatic("", "", "", credentials.SignatureAnonymous)})
	if err != nil {
		t.Fatal(err)
	}
	publicObject, err := anonymous.GetObject(ctx, bucket, key, minio.GetObjectOptions{})
	if err == nil {
		_, err = publicObject.Stat()
		_ = publicObject.Close()
	}
	if err == nil {
		t.Fatal("anonymous read unexpectedly succeeded for private media asset")
	}
}
