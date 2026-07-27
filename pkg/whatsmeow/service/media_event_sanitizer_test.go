package whatsmeow_service

import "testing"

func TestSanitizeProviderMediaDescriptors(t *testing.T) {
	data := map[string]interface{}{
		"Message": map[string]interface{}{
			"imageMessage": map[string]interface{}{
				"URL": "https://provider.invalid/bearer", "DirectPath": "/secret", "MediaKey": "key",
				"FileSHA256": "plain", "FileEncSHA256": "encrypted", "caption": "keep me", "mimetype": "image/jpeg",
			},
			"contextInfo": map[string]interface{}{
				"quotedMessage": map[string]interface{}{
					"imageMessage": map[string]interface{}{"directPath": "/quoted", "mediaKey": "quoted-key", "caption": "quoted"},
				},
			},
			"extendedTextMessage": map[string]interface{}{"URL": "https://example.com/article", "text": "keep link"},
		},
	}

	sanitizeProviderMediaDescriptors(data)
	message := data["Message"].(map[string]interface{})
	image := message["imageMessage"].(map[string]interface{})
	for _, key := range []string{"URL", "DirectPath", "MediaKey", "FileSHA256", "FileEncSHA256"} {
		if _, exists := image[key]; exists {
			t.Fatalf("provider descriptor key %q survived sanitization", key)
		}
	}
	if image["caption"] != "keep me" || image["mimetype"] != "image/jpeg" {
		t.Fatal("safe image metadata was removed")
	}
	link := message["extendedTextMessage"].(map[string]interface{})
	if link["URL"] != "https://example.com/article" {
		t.Fatal("ordinary link URL was removed")
	}
	quoted := message["contextInfo"].(map[string]interface{})["quotedMessage"].(map[string]interface{})["imageMessage"].(map[string]interface{})
	if _, exists := quoted["directPath"]; exists || quoted["caption"] != "quoted" {
		t.Fatal("nested provider descriptor was not safely sanitized")
	}
}

func TestAttachLegacyMediaAssetID(t *testing.T) {
	data := map[string]interface{}{"Message": map[string]interface{}{"conversation": "hello"}}
	attachLegacyMediaAssetID(data, "asset-1")
	if data["mediaAssetId"] != "asset-1" || data["Message"].(map[string]interface{})["mediaAssetId"] != "asset-1" {
		t.Fatal("media asset id was not attached to the compatibility event")
	}
}
