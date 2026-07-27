package minio_storage

import (
	"context"
	"errors"
	"io"
	"strings"

	storage_interfaces "github.com/evolution-foundation/evolution-go/pkg/storage/interfaces"
)

// RoutedMediaAssetStorage keeps backfilled campaign objects in their existing
// private bucket while all new shared objects use the generic media bucket.
type RoutedMediaAssetStorage struct {
	shared storage_interfaces.MediaAssetStore
	legacy storage_interfaces.CampaignMediaStore
}

var _ storage_interfaces.MediaAssetStore = (*RoutedMediaAssetStorage)(nil)

func NewRoutedMediaAssetStorage(shared storage_interfaces.MediaAssetStore, legacy storage_interfaces.CampaignMediaStore) (*RoutedMediaAssetStorage, error) {
	if shared == nil || legacy == nil {
		return nil, errors.New("shared and legacy private media stores are required")
	}
	return &RoutedMediaAssetStorage{shared: shared, legacy: legacy}, nil
}

// NewRoutedMediaAssetPurgeStorage permits one unused namespace to be absent so
// rollback-time instance deletion does not require provisioning an unrelated
// bucket. The purge plan determines which namespaces are actually needed.
func NewRoutedMediaAssetPurgeStorage(shared storage_interfaces.MediaAssetStore, legacy storage_interfaces.CampaignMediaStore) (*RoutedMediaAssetStorage, error) {
	if shared == nil && legacy == nil {
		return nil, errors.New("at least one private media store is required")
	}
	return &RoutedMediaAssetStorage{shared: shared, legacy: legacy}, nil
}

func (s *RoutedMediaAssetStorage) Put(ctx context.Context, key string, reader io.Reader, size int64, contentType string) error {
	store, err := s.store(key)
	if err != nil {
		return err
	}
	return store.Put(ctx, key, reader, size, contentType)
}

func (s *RoutedMediaAssetStorage) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	store, err := s.store(key)
	if err != nil {
		return nil, err
	}
	return store.Open(ctx, key)
}

func (s *RoutedMediaAssetStorage) Delete(ctx context.Context, key string) error {
	store, err := s.store(key)
	if err != nil {
		return err
	}
	return store.Delete(ctx, key)
}

func (s *RoutedMediaAssetStorage) Health(ctx context.Context) error {
	if s == nil || ctx == nil || s.shared == nil && s.legacy == nil {
		return errors.New("routed media asset storage is unavailable")
	}
	var sharedErr, legacyErr error
	if s.shared != nil {
		sharedErr = s.shared.Health(ctx)
	}
	if s.legacy != nil {
		legacyErr = s.legacy.Health(ctx)
	}
	return errors.Join(sharedErr, legacyErr)
}

func (s *RoutedMediaAssetStorage) store(key string) (storage_interfaces.MediaAssetStore, error) {
	if s == nil {
		return nil, errors.New("routed media asset storage is unavailable")
	}
	if strings.HasPrefix(key, "media-assets/") {
		if s.shared == nil {
			return nil, errors.New("shared media asset storage is unavailable")
		}
		return s.shared, nil
	}
	if strings.HasPrefix(key, "campaign-media/") {
		if s.legacy == nil {
			return nil, errors.New("legacy campaign media storage is unavailable")
		}
		return campaignStoreAdapter{s.legacy}, nil
	}
	return nil, errors.New("unsupported media asset object namespace")
}

type campaignStoreAdapter struct {
	storage_interfaces.CampaignMediaStore
}
