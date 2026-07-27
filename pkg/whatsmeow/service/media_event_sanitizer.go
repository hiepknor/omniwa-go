package whatsmeow_service

import "strings"

var providerDescriptorKeys = map[string]struct{}{
	"url":                 {},
	"directpath":          {},
	"mediakey":            {},
	"filesha256":          {},
	"fileencsha256":       {},
	"thumbnaildirectpath": {},
	"thumbnailsha256":     {},
	"thumbnailencsha256":  {},
	"jpegthumbnail":       {},
	"mediakeytimestamp":   {},
}

func attachLegacyMediaAssetID(data map[string]interface{}, assetID string) {
	if data == nil || assetID == "" {
		return
	}
	data["mediaAssetId"] = assetID
	message, ok := data["Message"].(map[string]interface{})
	if !ok {
		message = make(map[string]interface{})
	}
	message["mediaAssetId"] = assetID
	data["Message"] = message
}

func sanitizeProviderMediaDescriptors(value interface{}) {
	switch current := value.(type) {
	case map[string]interface{}:
		for _, child := range current {
			sanitizeProviderMediaDescriptors(child)
		}
		if !looksLikeProviderMediaDescriptor(current) {
			return
		}
		for key := range current {
			if _, sensitive := providerDescriptorKeys[strings.ToLower(key)]; sensitive {
				delete(current, key)
			}
		}
	case []interface{}:
		for _, child := range current {
			sanitizeProviderMediaDescriptors(child)
		}
	}
}

func looksLikeProviderMediaDescriptor(value map[string]interface{}) bool {
	for key := range value {
		switch strings.ToLower(key) {
		case "directpath", "mediakey", "filesha256", "fileencsha256", "thumbnaildirectpath":
			return true
		}
	}
	return false
}
