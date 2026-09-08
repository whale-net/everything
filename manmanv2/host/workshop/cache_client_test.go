package workshop

import (
	"archive/tar"
	"bytes"
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	pb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeCacheWorkshopClient is a minimal stand-in for control-api's
// WorkshopServiceClient, scoped to exactly the two RPCs CacheClient.TryFetch
// calls (GetCacheDownloadURL, ReportCacheRead). Embedding the nil interface
// means any other method panics loudly if a scenario's call graph ever
// grows, rather than silently returning a zero value (same pattern as
// manmanv2/ui/handlers_sgc_test.go's fakeWorkshopServiceClient).
type fakeCacheWorkshopClient struct {
	pb.WorkshopServiceClient

	cacheResp *pb.GetCacheDownloadURLResponse
	cacheErr  error

	reportCalls []*pb.ReportCacheReadRequest
	reportErr   error
}

func (f *fakeCacheWorkshopClient) GetCacheDownloadURL(ctx context.Context, in *pb.GetCacheDownloadURLRequest, opts ...grpc.CallOption) (*pb.GetCacheDownloadURLResponse, error) {
	if f.cacheErr != nil {
		return nil, f.cacheErr
	}
	return f.cacheResp, nil
}

func (f *fakeCacheWorkshopClient) ReportCacheRead(ctx context.Context, in *pb.ReportCacheReadRequest, opts ...grpc.CallOption) (*pb.ReportCacheReadResponse, error) {
	f.reportCalls = append(f.reportCalls, in)
	if f.reportErr != nil {
		return nil, f.reportErr
	}
	return &pb.ReportCacheReadResponse{}, nil
}

// buildTar produces an uncompressed tar archive (the wire format cacheObjectFormat
// documents) containing the given files, so tests can exercise fetchAndExtract's
// real extraction path rather than asserting against a raw byte comparison.
func buildTar(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, content := range files {
		hdr := &tar.Header{
			Name: name,
			Mode: 0644,
			Size: int64(len(content)),
		}
		require.NoError(t, tw.WriteHeader(hdr))
		_, err := tw.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	return buf.Bytes()
}

// captureLogs redirects the package-level slog default to a buffer for the
// duration of the calling test, restoring the previous default on cleanup.
// Used by NFR6 assertions (never log the presigned URL or its query string).
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(original) })
	return buf
}

func TestTryFetch_CacheHit(t *testing.T) {
	const secret = "X-Amz-Signature=super-secret-signature-value"
	fileContents := map[string]string{
		"addon.vpk":       "pretend addon content",
		"nested/data.txt": "nested file content",
	}
	tarBytes := buildTar(t, fileContents)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(tarBytes)
	}))
	defer srv.Close()

	logs := captureLogs(t)

	fake := &fakeCacheWorkshopClient{
		cacheResp: &pb.GetCacheDownloadURLResponse{
			CacheHit:     true,
			CacheEntryId: 42,
			PresignedUrl: srv.URL + "?" + secret,
			SizeBytes:    int64(len(tarBytes)),
		},
	}

	tmpDir := t.TempDir()
	destDir := filepath.Join(tmpDir, "sgc-1", "addon-123")

	client := NewCacheClient(fake, 1, srv.Client())
	hit, err := client.TryFetch(context.Background(), "123456789", "17000000", destDir)

	require.NoError(t, err)
	assert.True(t, hit)

	for name, content := range fileContents {
		got, readErr := os.ReadFile(filepath.Join(destDir, name))
		require.NoError(t, readErr)
		assert.Equal(t, content, string(got))
	}

	// FR10: presence must be reported after the object actually landed, using the
	// cache_entry_id from the GetCacheDownloadURL response.
	require.Len(t, fake.reportCalls, 1)
	assert.Equal(t, int64(42), fake.reportCalls[0].CacheEntryId)
	assert.Equal(t, int64(1), fake.reportCalls[0].ServerId)

	// NFR6: the presigned URL (and its signed query string) must never reach logs.
	assert.NotContains(t, logs.String(), secret)
	assert.NotContains(t, logs.String(), srv.URL)
}

func TestTryFetch_CacheMiss(t *testing.T) {
	fake := &fakeCacheWorkshopClient{
		cacheResp: &pb.GetCacheDownloadURLResponse{CacheHit: false},
	}

	tmpDir := t.TempDir()
	destDir := filepath.Join(tmpDir, "sgc-1", "addon-123")

	client := NewCacheClient(fake, 1, http.DefaultClient)
	hit, err := client.TryFetch(context.Background(), "123456789", "17000000", destDir)

	// An ordinary miss (e.g. first-ever download) must not be treated as an error.
	assert.NoError(t, err)
	assert.False(t, hit)

	_, statErr := os.Stat(destDir)
	assert.True(t, os.IsNotExist(statErr), "destDir must not be created on a cache miss")
	assert.Empty(t, fake.reportCalls, "a miss must never report a cache read")
}

func TestTryFetch_S3Forbidden(t *testing.T) {
	const secret = "X-Amz-Signature=super-secret-signature-value"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("expired"))
	}))
	defer srv.Close()

	logs := captureLogs(t)

	fake := &fakeCacheWorkshopClient{
		cacheResp: &pb.GetCacheDownloadURLResponse{
			CacheHit:     true,
			CacheEntryId: 7,
			PresignedUrl: srv.URL + "?" + secret,
		},
	}

	tmpDir := t.TempDir()
	destDir := filepath.Join(tmpDir, "sgc-1", "addon-123")

	client := NewCacheClient(fake, 1, srv.Client())
	hit, err := client.TryFetch(context.Background(), "123456789", "17000000", destDir)

	// A stale/expired presigned URL must degrade to the SteamCMD fallback, not fail
	// the install outright: hit == false with a non-nil error the caller can log and
	// swallow. Assert the error names the actual HTTP status (rather than merely any
	// error, e.g. a downstream tar-parse failure on the non-tar 403 body) so this test
	// pins down the non-2xx status check specifically, not just "something failed".
	require.Error(t, err)
	assert.Contains(t, err.Error(), "403", "error should identify the non-2xx S3 response, not a downstream failure")
	assert.False(t, hit)

	_, statErr := os.Stat(destDir)
	assert.True(t, os.IsNotExist(statErr), "destDir must be untouched on a non-2xx S3 response")
	assert.Empty(t, fake.reportCalls, "a failed fetch must never report a cache read")

	assert.NotContains(t, logs.String(), secret)
	assert.NotContains(t, logs.String(), srv.URL)
}

// hijackTruncatingHandler simulates a transfer interrupted mid-body: it promises a
// Content-Length far larger than what it actually sends, then closes the connection.
// This forces io.Copy on the client side to fail with an unexpected-EOF-style error,
// exercising fetchAndExtract's atomic-rename guarantee: nothing must be visible at
// destDir until the whole transfer and extraction succeed.
func hijackTruncatingHandler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		require.True(t, ok, "test server must support hijacking to simulate a truncated transfer")
		conn, bufrw, err := hj.Hijack()
		require.NoError(t, err)
		defer conn.Close()

		_, _ = bufrw.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 1000000\r\n\r\n")
		_, _ = bufrw.WriteString("short body, connection closes before the promised length")
		_ = bufrw.Flush()
		// Falling off the end of the handler (via the deferred conn.Close() above)
		// closes the TCP connection before the promised 1,000,000 bytes are sent.
	}
}

func TestTryFetch_InterruptedTransfer(t *testing.T) {
	srv := httptest.NewServer(hijackTruncatingHandler(t))
	defer srv.Close()

	fake := &fakeCacheWorkshopClient{
		cacheResp: &pb.GetCacheDownloadURLResponse{
			CacheHit:     true,
			CacheEntryId: 9,
			PresignedUrl: srv.URL,
		},
	}

	tmpDir := t.TempDir()
	destDir := filepath.Join(tmpDir, "sgc-1", "addon-123")

	client := NewCacheClient(fake, 1, srv.Client())
	hit, err := client.TryFetch(context.Background(), "123456789", "17000000", destDir)

	require.Error(t, err)
	assert.False(t, hit)

	_, statErr := os.Stat(destDir)
	assert.True(t, os.IsNotExist(statErr), "an interrupted transfer must leave no content at destDir")

	// No sibling temp/staging artifacts should survive either -- fetchAndExtract's
	// defers clean them up even on failure.
	parent := filepath.Dir(destDir)
	entries, readErr := os.ReadDir(parent)
	require.NoError(t, readErr)
	assert.Empty(t, entries, "no partial temp/staging artifacts should remain after a failed fetch")

	assert.Empty(t, fake.reportCalls)
}

func TestTryFetch_ControlAPIUnimplemented(t *testing.T) {
	fake := &fakeCacheWorkshopClient{
		cacheErr: status.Error(codes.Unimplemented, "GetCacheDownloadURL not supported"),
	}

	tmpDir := t.TempDir()
	destDir := filepath.Join(tmpDir, "sgc-1", "addon-123")

	client := NewCacheClient(fake, 1, http.DefaultClient)

	assert.NotPanics(t, func() {
		hit, err := client.TryFetch(context.Background(), "123456789", "17000000", destDir)
		// Older control-api (or an unavailable relay) must degrade to the SteamCMD
		// fallback: hit == false with the RPC error surfaced so the caller can log it
		// as an expected deployment-skew case, never a crash.
		assert.False(t, hit)
		assert.Error(t, err)
	})

	_, statErr := os.Stat(destDir)
	assert.True(t, os.IsNotExist(statErr))
	assert.Empty(t, fake.reportCalls)
}

// TestTryFetch_NFR6_ScrubsPresignedURLOnConnectionFailure exercises scrubURL directly:
// http.Client errors (e.g. connection refused) embed the request URL verbatim in their
// Error() string, so fetchAndExtract must strip it before the error is logged or
// returned -- NFR6 forbids the presigned URL (and its signed query string) from ever
// reaching host-manager logs, including inside wrapped error messages.
func TestTryFetch_NFR6_ScrubsPresignedURLOnConnectionFailure(t *testing.T) {
	const secret = "X-Amz-Signature=super-secret-signature-value"

	// Bind a loopback listener and close it immediately: connecting to the now-unused
	// port reliably and quickly fails with "connection refused" (unlike an unroutable
	// address, which can hang until a client timeout fires under some sandboxed network
	// policies).
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	closedAddr := l.Addr().String()
	require.NoError(t, l.Close())
	unreachableURL := "http://" + closedAddr + "/object?" + secret

	logs := captureLogs(t)

	fake := &fakeCacheWorkshopClient{
		cacheResp: &pb.GetCacheDownloadURLResponse{
			CacheHit:     true,
			CacheEntryId: 5,
			PresignedUrl: unreachableURL,
		},
	}

	tmpDir := t.TempDir()
	destDir := filepath.Join(tmpDir, "sgc-1", "addon-123")

	fastFailClient := &http.Client{Timeout: 5 * time.Second}
	client := NewCacheClient(fake, 1, fastFailClient)
	hit, err := client.TryFetch(context.Background(), "123456789", "17000000", destDir)

	require.Error(t, err)
	assert.False(t, hit)
	assert.NotContains(t, err.Error(), secret, "the returned error must not embed the presigned URL's query string")
	assert.NotContains(t, err.Error(), unreachableURL)
	assert.NotContains(t, logs.String(), secret, "logs must never contain the presigned URL's query string")
	assert.NotContains(t, logs.String(), unreachableURL)
}
