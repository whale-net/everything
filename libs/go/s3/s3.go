package s3

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Client wraps the AWS S3 client for ManMan operations
type Client struct {
	s3Client *s3.Client
	bucket   string
}

// Config holds S3 client configuration
type Config struct {
	Bucket   string
	Region   string
	Endpoint string // Optional: Custom S3 endpoint (e.g., for OVH, MinIO, DigitalOcean Spaces)
}

// NewClient creates a new S3 client
// Supports both AWS S3 and S3-compatible storage (OVH, MinIO, DigitalOcean Spaces, etc.)
func NewClient(ctx context.Context, cfg Config) (*Client, error) {
	awsCfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(cfg.Region))
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

	return &Client{
		s3Client: s3.NewFromConfig(awsCfg, s3Opts...),
		bucket:   cfg.Bucket,
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
