package storage_interfaces

import (
	"context"
	"io"
)

// CampaignMediaStore is a private object-key store for durable campaign media.
// Implementations must not return public or presigned URLs.
type CampaignMediaStore interface {
	Put(context.Context, string, io.Reader, int64, string) error
	Open(context.Context, string) (io.ReadCloser, error)
	Delete(context.Context, string) error
	Health(context.Context) error
}
