package minio_storage

import (
	"bytes"
	"context"
	"io"
	"os"
	"testing"

	"github.com/google/uuid"
)

func TestCampaignObjectKeyValidation(t *testing.T) {
	valid := "campaign-media/4c8df493-dbbb-4b4d-9794-b728be2e0693/927beb51-46c2-4331-b3b4-d96f67280bd3/image"
	if !validCampaignObjectKey(valid) {
		t.Fatal("expected bounded campaign object key to be valid")
	}
	for _, value := range []string{"other/key", "campaign-media/../secret", "campaign-media/a\nheader", "campaign-media/" + string(make([]byte, 600))} {
		if validCampaignObjectKey(value) {
			t.Fatalf("unsafe object key accepted: %q", value)
		}
	}
}

func TestCampaignMediaStorageIntegration(t *testing.T) {
	endpoint := os.Getenv("TEST_MINIO_ENDPOINT")
	if endpoint == "" {
		t.Skip("TEST_MINIO_ENDPOINT is not set")
	}
	store, err := NewCampaignMediaStorage(
		context.Background(), endpoint, os.Getenv("TEST_MINIO_ACCESS_KEY"), os.Getenv("TEST_MINIO_SECRET_KEY"),
		os.Getenv("TEST_MINIO_BUCKET"), os.Getenv("TEST_MINIO_REGION"), false,
	)
	if err != nil {
		t.Fatal(err)
	}
	key := "campaign-media/" + uuid.NewString() + "/" + uuid.NewString() + "/image"
	t.Cleanup(func() { _ = store.Delete(context.Background(), key) })
	want := []byte("normalized-image-placeholder")
	if err := store.Put(context.Background(), key, bytes.NewReader(want), int64(len(want)), "image/jpeg"); err != nil {
		t.Fatal(err)
	}
	object, err := store.Open(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(object)
	_ = object.Close()
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("read=%q err=%v", got, err)
	}
	if err := store.Delete(context.Background(), key); err != nil {
		t.Fatal(err)
	}
}
