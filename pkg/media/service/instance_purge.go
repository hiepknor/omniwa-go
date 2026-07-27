package media_service

import (
	"context"
	"errors"
	"fmt"

	media_model "github.com/evolution-foundation/evolution-go/pkg/media/model"
	media_repository "github.com/evolution-foundation/evolution-go/pkg/media/repository"
	storage_interfaces "github.com/evolution-foundation/evolution-go/pkg/storage/interfaces"
	"github.com/google/uuid"
)

type instancePurgeRepository interface {
	PlanInstancePurge(context.Context, string) (*media_repository.InstancePurgePlan, error)
}

type InstancePurgeStoreFactory func(context.Context, []media_model.AssetVariant) (storage_interfaces.MediaAssetStore, error)

// InstancePurger removes all known private objects before the database deletes
// their metadata. The returned IDs fence the later instance-delete transaction:
// a concurrent asset that was not purged keeps the instance FK restricted.
type InstancePurger struct {
	repository instancePurgeRepository
	store      InstancePurgeStoreFactory
}

func NewInstancePurger(repository instancePurgeRepository, store InstancePurgeStoreFactory) *InstancePurger {
	return &InstancePurger{repository: repository, store: store}
}

func (p *InstancePurger) Purge(ctx context.Context, instanceID string) ([]string, error) {
	if p == nil || p.repository == nil || ctx == nil || uuid.Validate(instanceID) != nil {
		return nil, errors.New("instance media purger and identity are required")
	}
	plan, err := p.repository.PlanInstancePurge(ctx, instanceID)
	if err != nil {
		return nil, fmt.Errorf("plan instance media purge: %w", err)
	}
	if len(plan.Variants) == 0 {
		return plan.AssetIDs, nil
	}
	if p.store == nil {
		return nil, errors.New("private media storage is required to delete an instance with media")
	}
	store, err := p.store(ctx, plan.Variants)
	if err != nil {
		return nil, fmt.Errorf("open private media storage for instance purge: %w", err)
	}
	for _, variant := range plan.Variants {
		if err := store.Delete(ctx, variant.ObjectKey); err != nil {
			return nil, fmt.Errorf("delete private media object for instance purge: %w", err)
		}
	}
	return plan.AssetIDs, nil
}
