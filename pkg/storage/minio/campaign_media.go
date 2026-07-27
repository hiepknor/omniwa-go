package minio_storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	storage_interfaces "github.com/evolution-foundation/evolution-go/pkg/storage/interfaces"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type CampaignMediaStorage struct {
	client     *minio.Client
	bucketName string
}

var _ storage_interfaces.CampaignMediaStore = (*CampaignMediaStorage)(nil)

func NewCampaignMediaStorage(ctx context.Context, endpoint, accessKeyID, secretAccessKey, bucketName, region string, useSSL bool) (*CampaignMediaStorage, error) {
	if ctx == nil || strings.TrimSpace(endpoint) == "" || strings.TrimSpace(accessKeyID) == "" || strings.TrimSpace(secretAccessKey) == "" || strings.TrimSpace(bucketName) == "" {
		return nil, errors.New("campaign media storage configuration is required")
	}
	client, err := minio.New(endpoint, &minio.Options{
		Creds: credentials.NewStaticV4(accessKeyID, secretAccessKey, ""), Secure: useSSL, Region: region,
	})
	if err != nil {
		return nil, fmt.Errorf("create campaign media storage client: %w", err)
	}
	exists, err := client.BucketExists(ctx, bucketName)
	if err != nil {
		return nil, fmt.Errorf("check campaign media bucket: %w", err)
	}
	if !exists {
		return nil, errors.New("private campaign media bucket does not exist")
	}
	return &CampaignMediaStorage{client: client, bucketName: bucketName}, nil
}

func (s *CampaignMediaStorage) Put(ctx context.Context, key string, reader io.Reader, size int64, contentType string) error {
	if err := s.validate(ctx, key); err != nil || reader == nil || size < 1 || strings.TrimSpace(contentType) == "" {
		if err != nil {
			return err
		}
		return errors.New("bounded campaign media object is required")
	}
	_, err := s.client.PutObject(ctx, s.bucketName, key, reader, size, minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return fmt.Errorf("put campaign media object: %w", err)
	}
	return nil
}

func (s *CampaignMediaStorage) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := s.validate(ctx, key); err != nil {
		return nil, err
	}
	object, err := s.client.GetObject(ctx, s.bucketName, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("open campaign media object: %w", err)
	}
	if _, err := object.Stat(); err != nil {
		_ = object.Close()
		return nil, fmt.Errorf("stat campaign media object: %w", err)
	}
	return object, nil
}

func (s *CampaignMediaStorage) Delete(ctx context.Context, key string) error {
	if err := s.validate(ctx, key); err != nil {
		return err
	}
	if err := s.client.RemoveObject(ctx, s.bucketName, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("delete campaign media object: %w", err)
	}
	return nil
}

func (s *CampaignMediaStorage) Health(ctx context.Context) error {
	if s == nil || s.client == nil || strings.TrimSpace(s.bucketName) == "" || ctx == nil {
		return errors.New("campaign media storage is unavailable")
	}
	exists, err := s.client.BucketExists(ctx, s.bucketName)
	if err != nil {
		return fmt.Errorf("check campaign media storage health: %w", err)
	}
	if !exists {
		return errors.New("campaign media bucket is unavailable")
	}
	return nil
}

func (s *CampaignMediaStorage) validate(ctx context.Context, key string) error {
	if s == nil || s.client == nil || strings.TrimSpace(s.bucketName) == "" || ctx == nil || !validCampaignObjectKey(key) {
		return errors.New("campaign media storage and safe object key are required")
	}
	return nil
}

func validCampaignObjectKey(key string) bool {
	return strings.HasPrefix(key, "campaign-media/") && len(key) <= 512 && !strings.Contains(key, "..") && !strings.ContainsAny(key, "\x00\r\n")
}
