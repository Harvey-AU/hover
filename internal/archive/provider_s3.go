package archive

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// S3Provider implements ColdStorageProvider for any S3-compatible service
// (Cloudflare R2, Backblaze B2, AWS S3, MinIO, etc.).
type S3Provider struct {
	client   *s3.Client
	provider string // short label for DB column
}

// NewS3Provider creates a provider from explicit credentials.
func NewS3Provider(endpoint, accessKeyID, secretAccessKey, region, providerName string) (*S3Provider, error) {
	if endpoint == "" || accessKeyID == "" || secretAccessKey == "" {
		return nil, fmt.Errorf("archive: endpoint, access key, and secret key are required")
	}
	if region == "" {
		region = "auto" // R2 uses "auto"
	}
	if providerName == "" {
		providerName = "s3"
	}

	cfg, err := config.LoadDefaultConfig(context.Background(),
		config.WithRegion(region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			accessKeyID, secretAccessKey, "",
		)),
		// R2 supports PutObject, but the AWS SDK's default "when supported"
		// checksum mode adds extra checksum headers on uploads. Those headers are
		// not universally implemented across S3-compatible providers and can
		// cause signature mismatches even when HeadBucket succeeds.
		config.WithRequestChecksumCalculation(aws.RequestChecksumCalculationWhenRequired),
		config.WithResponseChecksumValidation(aws.ResponseChecksumValidationWhenRequired),
	)
	if err != nil {
		return nil, fmt.Errorf("archive: failed to load AWS config: %w", err)
	}

	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})

	return &S3Provider{client: client, provider: providerName}, nil
}

// ProviderFromEnv builds a ColdStorageProvider from ARCHIVE_* env vars.
// Returns (nil, nil) if ARCHIVE_PROVIDER is unset.
func ProviderFromEnv() (ColdStorageProvider, error) {
	name := os.Getenv("ARCHIVE_PROVIDER")
	if name == "" {
		return nil, nil
	}

	return NewS3Provider(
		os.Getenv("ARCHIVE_ENDPOINT"),
		os.Getenv("ARCHIVE_ACCESS_KEY_ID"),
		os.Getenv("ARCHIVE_SECRET_ACCESS_KEY"),
		os.Getenv("ARCHIVE_REGION"),
		name,
	)
}

func (p *S3Provider) Ping(ctx context.Context, bucket string) error {
	_, err := p.client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(bucket),
	})
	if err != nil {
		return fmt.Errorf("archive: cannot reach bucket %q: %w", bucket, err)
	}
	return nil
}

func (p *S3Provider) Upload(ctx context.Context, bucket, key string, data io.Reader, opts UploadOptions) error {
	var (
		bodyReader    io.Reader
		contentLength int64
	)

	if seeker, ok := data.(io.ReadSeeker); ok {
		start, err := seeker.Seek(0, io.SeekCurrent)
		if err != nil {
			return fmt.Errorf("archive: determine upload offset for %s/%s: %w", bucket, key, err)
		}
		end, err := seeker.Seek(0, io.SeekEnd)
		if err != nil {
			return fmt.Errorf("archive: determine upload length for %s/%s: %w", bucket, key, err)
		}
		if _, err := seeker.Seek(start, io.SeekStart); err != nil {
			return fmt.Errorf("archive: rewind upload reader for %s/%s: %w", bucket, key, err)
		}
		bodyReader = seeker
		contentLength = end - start
	} else {
		// Read non-seekable readers upfront so we can set ContentLength explicitly.
		// R2 rejects chunked/unsigned-payload signatures when content length is unknown.
		body, err := io.ReadAll(data)
		if err != nil {
			return fmt.Errorf("archive: read upload body for %s/%s: %w", bucket, key, err)
		}
		bodyReader = bytes.NewReader(body)
		contentLength = int64(len(body))
	}

	input := &s3.PutObjectInput{
		Bucket:        aws.String(bucket),
		Key:           aws.String(key),
		Body:          bodyReader,
		ContentLength: aws.Int64(contentLength),
	}
	if opts.ContentType != "" {
		input.ContentType = aws.String(opts.ContentType)
	}
	if opts.ContentEncoding != "" {
		input.ContentEncoding = aws.String(opts.ContentEncoding)
	}
	if len(opts.Metadata) > 0 {
		input.Metadata = opts.Metadata
	}

	// Use UNSIGNED-PAYLOAD to avoid R2's chunked signature validation issues.
	_, err := p.client.PutObject(ctx, input, s3.WithAPIOptions(
		v4.SwapComputePayloadSHA256ForUnsignedPayloadMiddleware,
	))
	if err != nil {
		return fmt.Errorf("archive: upload %s/%s failed: %w", bucket, key, err)
	}
	return nil
}

func (p *S3Provider) Download(ctx context.Context, bucket, key string) (io.ReadCloser, error) {
	output, err := p.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("archive: download %s/%s failed: %w", bucket, key, err)
	}
	return output.Body, nil
}

func (p *S3Provider) Exists(ctx context.Context, bucket, key string) (bool, error) {
	_, err := p.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		var nsk *types.NotFound
		if errors.As(err, &nsk) {
			return false, nil
		}
		// R2 may return NoSuchKey instead of NotFound
		var nske *types.NoSuchKey
		if errors.As(err, &nske) {
			return false, nil
		}
		return false, fmt.Errorf("archive: head %s/%s failed: %w", bucket, key, err)
	}
	return true, nil
}

func (p *S3Provider) Delete(ctx context.Context, bucket, key string) error {
	_, err := p.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("archive: delete %s/%s failed: %w", bucket, key, err)
	}
	return nil
}

func (p *S3Provider) Provider() string {
	return p.provider
}
