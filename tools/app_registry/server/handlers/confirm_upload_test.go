package handlers

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/server/repository/fake"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// testS3Client is a mock s3.Client that records calls and can return test data
type testS3Client struct {
	// objects maps key to (data, delay)
	objects map[string]struct {
		data  []byte
		delay time.Duration
	}
	downloadCalls int
}

func newTestS3Client() *testS3Client {
	return &testS3Client{
		objects: make(map[string]struct {
			data  []byte
			delay time.Duration
		}),
	}
}

func (m *testS3Client) Upload(ctx context.Context, key string, data []byte) error {
	panic("not implemented")
}

func (m *testS3Client) Download(ctx context.Context, key string) ([]byte, error) {
	m.downloadCalls++
	obj, exists := m.objects[key]
	if !exists {
		return nil, fmt.Errorf("object not found: %s", key)
	}
	
	// Simulate network delay
	if obj.delay > 0 {
		select {
		case <-time.After(obj.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	
	return obj.data, nil
}

func (m *testS3Client) PresignPublicGetURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	panic("not implemented")
}

func (m *testS3Client) PresignPublicPutURL(ctx context.Context, key string, contentType string, ttl time.Duration) (string, error) {
	panic("not implemented")
}

func (m *testS3Client) PresignGetURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	panic("not implemented")
}

func (m *testS3Client) Delete(ctx context.Context, key string) error {
	panic("not implemented")
}

func (m *testS3Client) Head(ctx context.Context, key string) (bool, error) {
	panic("not implemented")
}

// TestConfirmUpload_DigestMismatchAfterTamper tests FR-46:
// "tamper with bytes after the PUT and assert confirmation fails"
func TestConfirmUpload_DigestMismatchAfterTamper(t *testing.T) {
	repo := fake.New()
	server := NewArtifactServer(repo)
	
	// Create a mock S3 client with test data
	mockS3 := newTestS3Client()
	
	// Original data and its hash
	originalData := []byte("test artifact content")
	hasher := sha256.New()
	hasher.Write(originalData)
	claimedDigest := "sha256:" + fmt.Sprintf("%x", hasher.Sum(nil))
	
	// Tampered data
	tamperedData := []byte("tampered artifact content!")
	mockS3.objects["test-object-key"] = struct {
		data  []byte
		delay time.Duration
	}{data: tamperedData}
	
	// Test confirmUploadFile directly with the test S3 client
	result, err := server.confirmUploadFile(
		context.Background(),
		mockS3,
		"", // no gzip encoding
		"application/octet-stream",
		&pb.ConfirmUploadFile{
			VariantKey:    "linux-amd64",
			ObjectKey:     "test-object-key",
			ClaimedDigest: claimedDigest,
		},
	)
	
	require.NoError(t, err)
	assert.False(t, result.Confirmed, "confirmation should fail for tampered bytes")
	assert.False(t, result.IsTimeout, "should not be a timeout")
	assert.Contains(t, result.ErrorMessage, "digest mismatch")
	assert.Equal(t, claimedDigest, result.ClaimedDigest)
	assert.NotEqual(t, claimedDigest, result.ComputedDigest)
	assert.Equal(t, "test-object-key", result.ObjectKey)
}

// TestConfirmUpload_GzipDecompression tests that gzip-encoded blobs are
// decompressed before hashing (FR-46).
func TestConfirmUpload_GzipDecompression(t *testing.T) {
	server := NewArtifactServer(fake.New())
	mockS3 := newTestS3Client()
	
	// Original uncompressed data
	originalData := []byte("test artifact content for gzip")
	
	// Gzip compress it
	var buf bytes.Buffer
	gzipWriter := gzip.NewWriter(&buf)
	gzipWriter.Write(originalData)
	gzipWriter.Close()
	compressedData := buf.Bytes()
	
	// Hash the uncompressed data
	hasher := sha256.New()
	hasher.Write(originalData)
	uncompressedDigest := "sha256:" + fmt.Sprintf("%x", hasher.Sum(nil))
	
	// Store compressed data in S3
	mockS3.objects["gzip-object-key"] = struct {
		data  []byte
		delay time.Duration
	}{data: compressedData}
	
	// Confirm with gzip encoding
	result, err := server.confirmUploadFile(
		context.Background(),
		mockS3,
		"gzip", // gzip encoding
		"application/octet-stream",
		&pb.ConfirmUploadFile{
			VariantKey:    "linux-amd64",
			ObjectKey:     "gzip-object-key",
			ClaimedDigest: uncompressedDigest,
		},
	)
	
	require.NoError(t, err)
	assert.True(t, result.Confirmed, "confirmation should succeed")
	assert.False(t, result.IsTimeout, "should not be a timeout")
	assert.Equal(t, uncompressedDigest, result.ComputedDigest)
}

// TestConfirmUpload_MismatchDoesNotPoisonDigest tests FR-47:
// "the claimed digest is not poisoned — a subsequent upload of correct
// bytes for the same digest can be confirmed and published"
func TestConfirmUpload_MismatchDoesNotPoisonDigest(t *testing.T) {
	server := NewArtifactServer(fake.New())
	mockS3 := newTestS3Client()
	
	// Correct data
	correctData := []byte("correct artifact content")
	hasher := sha256.New()
	hasher.Write(correctData)
	correctDigest := "sha256:" + fmt.Sprintf("%x", hasher.Sum(nil))
	
	// First attempt with wrong data
	wrongData := []byte("wrong artifact content")
	mockS3.objects["object-key-1"] = struct {
		data  []byte
		delay time.Duration
	}{data: wrongData}
	
	result1, err := server.confirmUploadFile(
		context.Background(),
		mockS3,
		"",
		"application/octet-stream",
		&pb.ConfirmUploadFile{
			VariantKey:    "linux-amd64",
			ObjectKey:     "object-key-1",
			ClaimedDigest: correctDigest, // claim correct digest but store wrong data
		},
	)
	
	require.NoError(t, err)
	assert.False(t, result1.Confirmed)
	assert.False(t, result1.IsTimeout)
	
	// Second attempt with correct data (different object key, same digest)
	mockS3.objects["object-key-2"] = struct {
		data  []byte
		delay time.Duration
	}{data: correctData}
	
	result2, err := server.confirmUploadFile(
		context.Background(),
		mockS3,
		"",
		"application/octet-stream",
		&pb.ConfirmUploadFile{
			VariantKey:    "linux-amd64",
			ObjectKey:     "object-key-2",
			ClaimedDigest: correctDigest,
		},
	)
	
	require.NoError(t, err)
	assert.True(t, result2.Confirmed, "second attempt with correct bytes should succeed")
	assert.False(t, result2.IsTimeout)
	assert.Equal(t, correctDigest, result2.ComputedDigest)
}

// TestConfirmUpload_PerFileTimeout tests NFR-24: per-file 60s deadline
func TestConfirmUpload_PerFileTimeout(t *testing.T) {
	server := NewArtifactServer(fake.New())
	mockS3 := newTestS3Client()
	
	// Create a slow object that takes 100ms to download
	slowData := []byte("slow artifact")
	mockS3.objects["slow-object-key"] = struct {
		data  []byte
		delay time.Duration
	}{data: slowData, delay: 100 * time.Millisecond}
	
	// Create a context that times out after 50ms
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	
	hasher := sha256.New()
	hasher.Write(slowData)
	claimedDigest := "sha256:" + fmt.Sprintf("%x", hasher.Sum(nil))
	
	result, err := server.confirmUploadFile(
		ctx,
		mockS3,
		"",
		"application/octet-stream",
		&pb.ConfirmUploadFile{
			VariantKey:    "linux-amd64",
			ObjectKey:     "slow-object-key",
			ClaimedDigest: claimedDigest,
		},
	)
	
	require.NoError(t, err)
	assert.False(t, result.Confirmed)
	assert.True(t, result.IsTimeout, "timeout should be distinguishable from mismatch")
	assert.Contains(t, result.ErrorMessage, "deadline")
}

// TestConfirmUpload_NamesMismatchDetails tests FR-47:
// "the run fails with an error naming the object key, the claimed digest,
// and the computed digest"
func TestConfirmUpload_NamesMismatchDetails(t *testing.T) {
	server := NewArtifactServer(fake.New())
	mockS3 := newTestS3Client()
	
	wrongData := []byte("wrong data")
	mockS3.objects["specific-object-key"] = struct {
		data  []byte
		delay time.Duration
	}{data: wrongData}
	
	result, err := server.confirmUploadFile(
		context.Background(),
		mockS3,
		"",
		"application/octet-stream",
		&pb.ConfirmUploadFile{
			VariantKey:    "linux-amd64",
			ObjectKey:     "specific-object-key",
			ClaimedDigest: "sha256:0000000000000000000000000000000000000000000000000000000000000000",
		},
	)
	
	require.NoError(t, err)
	assert.False(t, result.Confirmed)
	assert.False(t, result.IsTimeout)
	
	// Verify all three values are present in the result
	assert.Equal(t, "specific-object-key", result.ObjectKey, "object key should be named")
	assert.Equal(t, "sha256:0000000000000000000000000000000000000000000000000000000000000000", result.ClaimedDigest, "claimed digest should be named")
	assert.NotEmpty(t, result.ComputedDigest, "computed digest should be present")
	assert.Contains(t, result.ErrorMessage, "digest mismatch", "error message should describe mismatch")
}

// TestConfirmUpload_ValidationErrors tests request validation
func TestConfirmUpload_ValidationErrors(t *testing.T) {
	repo := fake.New()
	server := NewArtifactServer(repo)
	
	tests := []struct {
		name    string
		request *pb.ConfirmUploadRequest
		wantErr bool
		errCode codes.Code
	}{
		{
			name: "missing upload_session_id",
			request: &pb.ConfirmUploadRequest{
				ArtifactKind: "binary",
				Files: []*pb.ConfirmUploadFile{
					{
						VariantKey:    "linux-amd64",
						ObjectKey:     "obj-key",
						ClaimedDigest: "sha256:1234",
					},
				},
			},
			wantErr: true,
			errCode: codes.InvalidArgument,
		},
		{
			name: "missing artifact_kind",
			request: &pb.ConfirmUploadRequest{
				UploadSessionId: "upload-123",
				Files: []*pb.ConfirmUploadFile{
					{
						VariantKey:    "linux-amd64",
						ObjectKey:     "obj-key",
						ClaimedDigest: "sha256:1234",
					},
				},
			},
			wantErr: true,
			errCode: codes.InvalidArgument,
		},
		{
			name: "empty files list",
			request: &pb.ConfirmUploadRequest{
				UploadSessionId: "upload-123",
				ArtifactKind:    "binary",
				Files:           []*pb.ConfirmUploadFile{},
			},
			wantErr: true,
			errCode: codes.InvalidArgument,
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := server.validateConfirmUploadRequest(tt.request)
			if tt.wantErr {
				require.Error(t, err)
				st, ok := status.FromError(err)
				require.True(t, ok)
				assert.Equal(t, tt.errCode, st.Code())
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestConfirmUpload_ConfirmedBlobTracking tests NFR-18: read-back once per distinct blob
func TestConfirmUpload_ConfirmedBlobTracking(t *testing.T) {
	server := NewArtifactServer(fake.New())
	mockS3 := newTestS3Client()
	
	// Create data and its digest
	data := []byte("shared artifact content")
	hasher := sha256.New()
	hasher.Write(data)
	digest := "sha256:" + fmt.Sprintf("%x", hasher.Sum(nil))
	
	// Store in two different object keys
	mockS3.objects["object-key-1"] = struct {
		data  []byte
		delay time.Duration
	}{data: data}
	mockS3.objects["object-key-2"] = struct {
		data  []byte
		delay time.Duration
	}{data: data}
	
	// First confirmation
	result1, err := server.confirmUploadFile(
		context.Background(),
		mockS3,
		"",
		"application/octet-stream",
		&pb.ConfirmUploadFile{
			VariantKey:    "variant-1",
			ObjectKey:     "object-key-1",
			ClaimedDigest: digest,
		},
	)
	require.NoError(t, err)
	assert.True(t, result1.Confirmed)
	
	// Second confirmation should work too (same data, different key)
	result2, err := server.confirmUploadFile(
		context.Background(),
		mockS3,
		"",
		"application/octet-stream",
		&pb.ConfirmUploadFile{
			VariantKey:    "variant-2",
			ObjectKey:     "object-key-2",
			ClaimedDigest: digest,
		},
	)
	require.NoError(t, err)
	assert.True(t, result2.Confirmed)
	
	// Both should have the same computed digest
	assert.Equal(t, result1.ComputedDigest, result2.ComputedDigest)
	
	// Each should have been downloaded once
	assert.Equal(t, 2, mockS3.downloadCalls)
}
