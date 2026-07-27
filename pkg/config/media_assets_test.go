package config

import "testing"

func TestValidateMediaAssetBucketIsolation(t *testing.T) {
	for _, test := range []struct {
		name, media, legacy string
		wantError           bool
	}{
		{name: "dedicated", media: "private-media", legacy: "legacy-public"},
		{name: "legacy disabled", media: "private-media"},
		{name: "missing private", media: " ", legacy: "legacy-public", wantError: true},
		{name: "shared bucket", media: "same", legacy: "same", wantError: true},
		{name: "trimmed shared bucket", media: " same ", legacy: "same", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateMediaAssetBucketIsolation(test.media, test.legacy)
			if (err != nil) != test.wantError {
				t.Fatalf("error = %v, wantError = %t", err, test.wantError)
			}
		})
	}
}
