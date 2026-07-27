package storage_interfaces

import (
	"context"
	"io"
)

// MediaAssetStore is the private object boundary for immutable media variants.
// Implementations must not expose public URLs or mutate bucket policy.
type MediaAssetStore interface {
	Put(context.Context, string, io.Reader, int64, string) error
	Open(context.Context, string) (io.ReadCloser, error)
	Delete(context.Context, string) error
	Health(context.Context) error
}
