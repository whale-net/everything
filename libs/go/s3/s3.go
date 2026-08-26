package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// Client wraps the AWS S3 client for ManMan operations
type Client struct {
	s3Client       *s3.Client
	presign        *s3.PresignClient
	presignPublic  *s3.PresignClient // uses public endpoint for pre-signed URLs
	uploader       *manager.Uploader
	bucket         string
}

// Config holds S3 client configuration
type Config struct {
	Bucket         string
	Region         string
	Endpoint       string // Optional: Custom S3 endpoint (e.g., for OVH, MinIO, DigitalOcean Spaces)
	PublicEndpoint string // Optional: Public-facing endpoint for pre-signed URLs (if different from Endpoint)
	AccessKey      string // Optional: Static access key (for MinIO, etc.)
	SecretKey      string // Optional: Static secret key (for MinIO, etc.)
}

// NewClient creates a new S3 client
// Supports both AWS S3 and S3-compatible storage (OVH, MinIO, DigitalOcean Spaces, etc.)
func NewClient(ctx context.Context, cfg Config) (*Client, error) {
	configOpts := []func(*config.LoadOptions) error{
		config.WithRegion(cfg.Region),
		// Disable automatic CRC32 checksum injection (default since service/s3 v1.73.0).
		// OVH Object Storage does not support x-amz-sdk-checksum-algorithm or
		// STREAMING-UNSIGNED-PAYLOAD-TRAILER, causing requests to be rejected (404/501).
		config.WithRequestChecksumCalculation(aws.RequestChecksumCalculationWhenRequired),
		config.WithResponseChecksumValidation(aws.ResponseChecksumValidationWhenRequired),
	}

	// If static credentials are provided, use them instead of default credential chain
	if cfg.AccessKey != "" && cfg.SecretKey != "" {
		configOpts = append(configOpts, config.WithCredentialsProvider(
			aws.CredentialsProviderFunc(func(ctx context.Context) (aws.Credentials, error) {
				return aws.Credentials{
					AccessKeyID:     cfg.AccessKey,
					SecretAccessKey: cfg.SecretKey,
				}, nil
			}),
		))
	}

	awsCfg, err := config.LoadDefaultConfig(ctx, configOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	// Configure S3 client options
	s3Opts := []func(*s3.Options){}

	// If custom endpoint is provided (e.g., OVH, MinIO), use it
	if cfg.Endpoint != "" {
		s3Opts = append(s3Opts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
			// Use path-style addressing for S3-compatible services
			o.UsePathStyle = true
		})
	}

	s3c := s3.NewFromConfig(awsCfg, s3Opts...)

	// If a public endpoint is configured, create a separate presign client using it.
	// Public endpoints (e.g. OVH's cloud.ovh.us) require virtual-hosted style URLs, not
	// path-style, so we do not inherit s3Opts here and explicitly leave UsePathStyle false.
	var presignPublic *s3.PresignClient
	if cfg.PublicEndpoint != "" {
		presignPublic = s3.NewPresignClient(s3.NewFromConfig(awsCfg, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(cfg.PublicEndpoint)
		}))
	}

	return &Client{
		s3Client:      s3c,
		presign:       s3.NewPresignClient(s3c),
		presignPublic: presignPublic,
		uploader:      manager.NewUploader(s3c),
		bucket:        cfg.Bucket,
	}, nil
}

// UploadOptions holds optional parameters for upload operations
type UploadOptions struct {
	ContentType     string
	ContentEncoding string
	Metadata        map[string]string
}

// Upload uploads data to S3 and returns the S3 URL
func (c *Client) Upload(ctx context.Context, key string, data []byte, opts *UploadOptions) (string, error) {
	if opts == nil {
		opts = &UploadOptions{}
	}

	input := &s3.PutObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(data),
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

	_, err := c.s3Client.PutObject(ctx, input)
	if err != nil {
		return "", fmt.Errorf("failed to upload to S3: %w", err)
	}

	// Return S3 URL in the format: s3://bucket/key
	return fmt.Sprintf("s3://%s/%s", c.bucket, key), nil
}

// PresignPutURL generates a pre-signed PUT URL for the given key.
// Always uses the primary (internal) endpoint — presigned URL consumers
// (e.g. backup scheduler, backup config) are internal infrastructure that reach S3 directly,
// and the signature must match the endpoint that handles the request.
// For public-endpoint presigned PUTs with per-file content types, see PresignPublicPutURL.
func (c *Client) PresignPutURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	req, err := c.presign.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(c.bucket),
		Key:         aws.String(key),
		ContentType: aws.String("application/gzip"),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("failed to presign PUT URL: %w", err)
	}
	return req.URL, nil
}

// PresignPublicPutURL generates a pre-signed PUT URL for the given key against the
// client's public endpoint (Config.PublicEndpoint) using virtual-hosted-style addressing.
// Returns both the presigned URL and the exact set of headers the producer must send
// with the PUT request — a deviating PUT will fail the signature verification.
// Returns an error if no public endpoint is configured.
func (c *Client) PresignPublicPutURL(ctx context.Context, key string, contentType string, ttl time.Duration) (url string, headers map[string]string, err error) {
	if c.presignPublic == nil {
		return "", nil, errors.New("no public endpoint configured for presigned PUT URLs")
	}
	req, err := c.presignPublic.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(c.bucket),
		Key:         aws.String(key),
		ContentType: aws.String(contentType),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", nil, fmt.Errorf("failed to presign public PUT URL: %w", err)
	}

	// Convert SignedHeader to a map of strings for easier consumption.
	// SignedHeader is an http.Header which is map[string][]string.
	headers = make(map[string]string)
	for k, v := range req.SignedHeader {
		// For signed headers, typically only one value per key, but take the first if multiple.
		if len(v) > 0 {
			headers[k] = v[0]
		}
	}

	return req.URL, headers, nil
}

// noSeekReader wraps an io.Reader to prevent the AWS SDK from seeking it.
type noSeekReader struct{ r io.Reader }

func (n noSeekReader) Read(p []byte) (int, error) { return n.r.Read(p) }

// UploadStream uploads an io.Reader to S3 using PutObject (streaming, no seeking required).
func (c *Client) UploadStream(ctx context.Context, key string, r io.Reader, opts *UploadOptions) (string, error) {
	if opts == nil {
		opts = &UploadOptions{}
	}

	input := &s3.PutObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
		Body:   noSeekReader{r},
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

	if _, err := c.s3Client.PutObject(ctx, input, func(o *s3.Options) {
		o.RetryMaxAttempts = 1
	}); err != nil {
		return "", fmt.Errorf("failed to stream upload to S3: %w", err)
	}
	return fmt.Sprintf("s3://%s/%s", c.bucket, key), nil
}

// Download downloads data from S3 by key
func (c *Client) Download(ctx context.Context, key string) ([]byte, error) {
	result, err := c.s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to download from S3: %w", err)
	}
	defer result.Body.Close()

	data, err := io.ReadAll(result.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read S3 object: %w", err)
	}

	return data, nil
}

// Delete deletes an object from S3
func (c *Client) Delete(ctx context.Context, key string) error {
	_, err := c.s3Client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("failed to delete from S3: %w", err)
	}

	return nil
}

// GetBucket returns the configured bucket name
func (c *Client) GetBucket() string {
	return c.bucket
}

// PresignPublicGetURL generates a pre-signed GET URL for key, addressed via
// the client's public endpoint (Config.PublicEndpoint) using presignPublic
// -- virtual-hosted-style, per the comment on presignPublic in NewClient
// (OVH's public endpoint rejects path-style requests outright with HTTP
// 400). Returns an error if no public endpoint is configured.
//
// Unlike an unsigned "public URL" (issue #979/#983's original design), this
// does not require the bucket to grant anonymous/public reads: the
// signature carries the caller's own credentials, so any identity with
// read access to the object (e.g. the one that wrote it) can hand an
// external consumer -- e.g. a CI job resolving a CLI binary download, see
// tools/app_registry/server/handlers/artifact.go's ResolveBinaryURL -- a
// working link without giving that consumer S3 credentials of its own.
// OVH's release-tools bucket was never actually configured for anonymous
// reads (issue #1101), which is why this replaced the unsigned approach.
func (c *Client) PresignPublicGetURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	if c.presignPublic == nil {
		return "", errors.New("no public endpoint configured for presigned GET URLs")
	}
	req, err := c.presignPublic.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("failed to presign public GET URL: %w", err)
	}
	return req.URL, nil
}

// Exists checks if an object exists in S3
func (c *Client) Exists(ctx context.Context, key string) (bool, error) {
	_, err := c.s3Client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		// Check for NotFound error type
		var notFound *types.NotFound
		if errors.As(err, &notFound) {
			return false, nil
		}

		// Check for NoSuchKey error type
		var noSuchKey *types.NoSuchKey
		if errors.As(err, &noSuchKey) {
			return false, nil
		}

		// Check error string for 404 status code or NotFound/NoSuchKey messages
		errStr := err.Error()
		if strings.Contains(errStr, "StatusCode: 404") ||
			strings.Contains(errStr, "NotFound") ||
			strings.Contains(errStr, "NoSuchKey") {
			return false, nil
		}

		return false, fmt.Errorf("failed to check S3 object existence: %w", err)
	}
	return true, nil
}

// Append appends data to an existing S3 object
// If the object doesn't exist, it creates a new one with the provided data
// This is an expensive operation as it requires downloading the entire object,
// concatenating the new data, and re-uploading
func (c *Client) Append(ctx context.Context, key string, data []byte, opts *UploadOptions) error {
	// Check if object exists
	exists, err := c.Exists(ctx, key)
	if err != nil {
		return err
	}

	var finalData []byte
	if exists {
		// Download existing object
		existingData, err := c.Download(ctx, key)
		if err != nil {
			return fmt.Errorf("failed to download existing object for append: %w", err)
		}
		// Concatenate existing data with new data
		finalData = append(existingData, data...)
	} else {
		// Object doesn't exist, just use the new data
		finalData = data
	}

	// Upload the combined data
	_, err = c.Upload(ctx, key, finalData, opts)
	if err != nil {
		return fmt.Errorf("failed to upload appended data: %w", err)
	}

	return nil
}
