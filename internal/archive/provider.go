package archive

import (
	"context"
	"io"
)

// UploadOptions controls metadata attached to a cold-storage object.
type UploadOptions struct {
	ContentType     string
	ContentEncoding string
	Metadata        map[string]string
}

// ColdStorageProvider abstracts an S3-compatible object store.
type ColdStorageProvider interface {
	// Upload writes data to the given bucket/key.
	Upload(ctx context.Context, bucket, key string, data io.Reader, opts UploadOptions) error
	// Download retrieves an object by bucket/key.
	Download(ctx context.Context, bucket, key string) (io.ReadCloser, error)
	// Exists returns true if the object exists and is readable.
	Exists(ctx context.Context, bucket, key string) (bool, error)
	// Provider returns the short name of the backend ("r2", "s3", "b2").
	Provider() string
}
