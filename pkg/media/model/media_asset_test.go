package media_model

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAssetJSONDoesNotExposeStorageOrTenantInternals(t *testing.T) {
	asset := Asset{
		ID: "asset-id", InstanceID: "instance-id", MediaType: "image", Origin: AssetOriginDeviceUpload,
		Status: AssetStatusReady, CleanupClaimToken: stringPointer("claim"),
		Canonical: &AssetVariant{Kind: VariantCanonical, ObjectKey: "media-assets/private", MIMEType: "image/jpeg", SHA256: strings.Repeat("a", 64)},
	}
	encoded, err := json.Marshal(asset)
	if err != nil {
		t.Fatal(err)
	}
	value := string(encoded)
	for _, forbidden := range []string{"instance-id", "media-assets/private", "claim", "objectKey", "instanceId"} {
		if strings.Contains(value, forbidden) {
			t.Fatalf("asset JSON exposed %q: %s", forbidden, value)
		}
	}
	if !strings.Contains(value, `"mediaType":"image"`) || !strings.Contains(value, `"variant":"canonical"`) {
		t.Fatalf("asset JSON omitted safe metadata: %s", value)
	}
}

func stringPointer(value string) *string { return &value }
