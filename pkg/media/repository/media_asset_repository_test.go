package media_repository

import (
	"context"
	"strings"
	"testing"
	"time"

	media_model "github.com/evolution-foundation/evolution-go/pkg/media/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func TestValidationRejectsCrossNamespaceVariant(t *testing.T) {
	repository := &repository{db: &gorm.DB{}, now: time.Now}
	variant := media_model.AssetVariant{
		MediaAssetID: uuid.NewString(), InstanceID: uuid.NewString(), Kind: media_model.VariantCanonical,
		ObjectKey: "campaign-media/wrong", MIMEType: "image/jpeg", SizeBytes: 1, Width: 1, Height: 1, SHA256: strings.Repeat("a", 64),
	}
	if err := validateVariant(repository, context.Background(), &variant); err == nil {
		t.Fatal("cross-namespace media variant was accepted")
	}
}

func TestValidationAcceptsExactTenantVariantKey(t *testing.T) {
	instanceID, assetID := uuid.NewString(), uuid.NewString()
	repository := &repository{db: &gorm.DB{}, now: time.Now}
	variant := media_model.AssetVariant{
		MediaAssetID: assetID, InstanceID: instanceID, Kind: media_model.VariantProviderOriginal,
		ObjectKey: "media-assets/" + instanceID + "/" + assetID + "/provider_original",
		MIMEType:  "image/png", SizeBytes: 1, Width: 1, Height: 1, SHA256: strings.Repeat("b", 64),
	}
	if err := validateVariant(repository, context.Background(), &variant); err != nil {
		t.Fatalf("exact tenant variant key rejected: %v", err)
	}
}
