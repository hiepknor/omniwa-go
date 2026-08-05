package storage_interfaces

import "context"

// MediaStorage defines the contract for storing and retrieving media files
type MediaStorage interface {
	Health(ctx context.Context) error

	// Store saves media data and returns a temporary signed compatibility URL.
	Store(ctx context.Context, data []byte, fileName string, contentType string) (string, error)

	// Delete removes the stored media
	Delete(ctx context.Context, fileName string) error

	// GetURL returns a temporary signed compatibility URL for the media.
	GetURL(ctx context.Context, fileName string) (string, error)
}
