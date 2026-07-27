package config

import (
	"errors"
	"strings"
)

// ValidateMediaAssetBucketIsolation prevents the private media domain from
// sharing the legacy bucket whose compatibility adapter may grant public read.
func ValidateMediaAssetBucketIsolation(mediaAssetBucket, legacyBucket string) error {
	mediaAssetBucket = strings.TrimSpace(mediaAssetBucket)
	legacyBucket = strings.TrimSpace(legacyBucket)
	if mediaAssetBucket == "" {
		return errors.New("private media asset bucket is required")
	}
	if legacyBucket != "" && mediaAssetBucket == legacyBucket {
		return errors.New("private media asset bucket must differ from the legacy media bucket")
	}
	return nil
}

func (c *Config) ValidateMediaAssetBucketIsolation() error {
	if c == nil {
		return errors.New("configuration is required")
	}
	return ValidateMediaAssetBucketIsolation(c.MediaAssetBucket, c.MinioBucket)
}

func (c *Config) ValidatePrivateBucketIsolation(bucket string) error {
	if c == nil {
		return errors.New("configuration is required")
	}
	return ValidateMediaAssetBucketIsolation(bucket, c.MinioBucket)
}
