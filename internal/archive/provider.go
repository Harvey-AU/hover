package archive

import (
	"context"
	"io"
)

type UploadOptions struct {
	ContentType     string
	ContentEncoding string
	Metadata        map[string]string
}

// ColdStorageProvider abstracts an S3-compatible object store.
type ColdStorageProvider interface {
	// Call at startup to catch bad credentials/endpoints early.
	Ping(ctx context.Context, bucket string) error
	Upload(ctx context.Context, bucket, key string, data io.Reader, opts UploadOptions) error
	Download(ctx context.Context, bucket, key string) (io.ReadCloser, error)
	Exists(ctx context.Context, bucket, key string) (bool, error)
	// Returns one of: "r2", "s3", "b2".
	Provider() string
}
