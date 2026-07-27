package minio_storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	storage_interfaces "github.com/evolution-foundation/evolution-go/pkg/storage/interfaces"
	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type MediaAssetStorage struct {
	client     *minio.Client
	bucketName string
}

const maxStoredMediaAssetBytes int64 = 64 * 1024 * 1024

var _ storage_interfaces.MediaAssetStore = (*MediaAssetStorage)(nil)

func NewMediaAssetStorage(ctx context.Context, endpoint, accessKeyID, secretAccessKey, bucketName, region string, useSSL bool) (*MediaAssetStorage, error) {
	if ctx == nil || strings.TrimSpace(endpoint) == "" || strings.TrimSpace(accessKeyID) == "" || strings.TrimSpace(secretAccessKey) == "" || strings.TrimSpace(bucketName) == "" {
		return nil, errors.New("media asset storage configuration is required")
	}
	client, err := minio.New(endpoint, &minio.Options{
		Creds: credentials.NewStaticV4(accessKeyID, secretAccessKey, ""), Secure: useSSL, Region: region,
	})
	if err != nil {
		return nil, fmt.Errorf("create media asset storage client: %w", err)
	}
	exists, err := client.BucketExists(ctx, bucketName)
	if err != nil {
		return nil, fmt.Errorf("check media asset bucket: %w", err)
	}
	if !exists {
		return nil, errors.New("private media asset bucket does not exist")
	}
	return &MediaAssetStorage{client: client, bucketName: bucketName}, nil
}

func (s *MediaAssetStorage) Put(ctx context.Context, key string, reader io.Reader, size int64, contentType string) error {
	if err := s.validate(ctx, key); err != nil {
		return err
	}
	if reader == nil || size < 1 || size > maxStoredMediaAssetBytes || !validImageMIME(contentType) {
		return errors.New("bounded image media asset is required")
	}
	if _, err := s.client.PutObject(ctx, s.bucketName, key, reader, size, minio.PutObjectOptions{ContentType: contentType}); err != nil {
		return fmt.Errorf("put media asset object: %w", err)
	}
	return nil
}

func (s *MediaAssetStorage) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := s.validate(ctx, key); err != nil {
		return nil, err
	}
	object, err := s.client.GetObject(ctx, s.bucketName, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("open media asset object: %w", err)
	}
	if _, err := object.Stat(); err != nil {
		_ = object.Close()
		return nil, fmt.Errorf("stat media asset object: %w", err)
	}
	return object, nil
}

func (s *MediaAssetStorage) Delete(ctx context.Context, key string) error {
	if err := s.validate(ctx, key); err != nil {
		return err
	}
	if err := s.client.RemoveObject(ctx, s.bucketName, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("delete media asset object: %w", err)
	}
	return nil
}

func (s *MediaAssetStorage) Health(ctx context.Context) error {
	if s == nil || s.client == nil || strings.TrimSpace(s.bucketName) == "" || ctx == nil {
		return errors.New("media asset storage is unavailable")
	}
	exists, err := s.client.BucketExists(ctx, s.bucketName)
	if err != nil {
		return fmt.Errorf("check media asset storage health: %w", err)
	}
	if !exists {
		return errors.New("media asset bucket is unavailable")
	}
	return nil
}

func (s *MediaAssetStorage) validate(ctx context.Context, key string) error {
	if s == nil || s.client == nil || strings.TrimSpace(s.bucketName) == "" || ctx == nil || !validMediaAssetObjectKey(key) {
		return errors.New("media asset storage and tenant-scoped object key are required")
	}
	return nil
}

func validMediaAssetObjectKey(key string) bool {
	parts := strings.Split(key, "/")
	if len(parts) != 4 || parts[0] != "media-assets" || uuid.Validate(parts[1]) != nil || uuid.Validate(parts[2]) != nil {
		return false
	}
	return parts[3] == "canonical" || parts[3] == "provider_original"
}

func validImageMIME(value string) bool { return value == "image/jpeg" || value == "image/png" }
