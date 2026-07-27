package media_service

import (
	"context"
	"reflect"
	"testing"

	media_model "github.com/evolution-foundation/evolution-go/pkg/media/model"
	media_repository "github.com/evolution-foundation/evolution-go/pkg/media/repository"
	storage_interfaces "github.com/evolution-foundation/evolution-go/pkg/storage/interfaces"
	"github.com/google/uuid"
)

type instancePurgeRepositoryFake struct {
	plan *media_repository.InstancePurgePlan
	err  error
}

func (f *instancePurgeRepositoryFake) PlanInstancePurge(context.Context, string) (*media_repository.InstancePurgePlan, error) {
	return f.plan, f.err
}

func TestInstancePurgerDeletesEveryVariantAndReturnsFencedAssetSet(t *testing.T) {
	assetIDs := []string{uuid.NewString(), uuid.NewString()}
	repository := &instancePurgeRepositoryFake{plan: &media_repository.InstancePurgePlan{
		AssetIDs: assetIDs,
		Variants: []media_model.AssetVariant{{ObjectKey: "legacy"}, {ObjectKey: "canonical"}},
	}}
	store := &cleanupStoreFake{}
	factoryCalls := 0
	purger := NewInstancePurger(repository, func(_ context.Context, variants []media_model.AssetVariant) (storage_interfaces.MediaAssetStore, error) {
		factoryCalls++
		if !reflect.DeepEqual(variants, repository.plan.Variants) {
			t.Fatalf("factory variants=%v", variants)
		}
		return store, nil
	})

	result, err := purger.Purge(context.Background(), uuid.NewString())
	if err != nil || !reflect.DeepEqual(result, assetIDs) || !reflect.DeepEqual(store.deleted, []string{"legacy", "canonical"}) || factoryCalls != 1 {
		t.Fatalf("result=%v deleted=%v factoryCalls=%d err=%v", result, store.deleted, factoryCalls, err)
	}
}

func TestInstancePurgerDoesNotRequireStorageWithoutVariants(t *testing.T) {
	assetID := uuid.NewString()
	purger := NewInstancePurger(&instancePurgeRepositoryFake{plan: &media_repository.InstancePurgePlan{AssetIDs: []string{assetID}}}, nil)
	result, err := purger.Purge(context.Background(), uuid.NewString())
	if err != nil || !reflect.DeepEqual(result, []string{assetID}) {
		t.Fatalf("result=%v err=%v", result, err)
	}
}

func TestInstancePurgerFailsClosedOnObjectDeleteFailure(t *testing.T) {
	purger := NewInstancePurger(&instancePurgeRepositoryFake{plan: &media_repository.InstancePurgePlan{
		AssetIDs: []string{uuid.NewString()}, Variants: []media_model.AssetVariant{{ObjectKey: "blocked"}},
	}}, func(context.Context, []media_model.AssetVariant) (storage_interfaces.MediaAssetStore, error) {
		return &cleanupStoreFake{failKey: "blocked"}, nil
	})
	if _, err := purger.Purge(context.Background(), uuid.NewString()); err == nil {
		t.Fatalf("purge error=%v", err)
	}
}
