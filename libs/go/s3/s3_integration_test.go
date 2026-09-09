//go:build integration

// Follow-up to #2213 (which pointed presigned cache URLs at the public S3
// endpoint) and finding #2225, which confirmed the resulting
// virtual-hosted-style URLs are unusable against local dev/Tilt MinIO: the
// hostname doesn't resolve there, and even forced to resolve, MinIO with no
// MINIO_DOMAIN configured treats the first URL path segment as the bucket
// name, so a direct PUT against a vhost-style presigned URL fails with
// NoSuchBucket. #2225 also noted the RPC-level fix (#2213) was verified but
// the resulting presigned URL was never actually used to move real bytes --
// this file closes that gap with a real MinIO container and real HTTP
// PUT/GET against the presigned URLs.
//
// Same precedent as manmanv2/api/handlers's action_execution_integration_test:
// build-tagged `integration`, real container via testcontainers-go's core
// GenericContainer API (no MinIO module vendored), run explicitly:
//
//	bazel test //libs/go/s3:s3_integration_test --test_output=all
package s3

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	minioTestRootUser     = "minioadmin"
	minioTestRootPassword = "minioadmin"
)

// startMinIO starts a throwaway MinIO container matching
// tools/tilt/minio.tilt's local dev deployment exactly: same env, same
// server args, and -- the load-bearing part -- no MINIO_DOMAIN set, which is
// what makes MinIO treat the first URL path segment as the bucket name
// (path-style only, no virtual-hosted-style routing) and is the root cause
// #2225 traces this defect to. Returns the endpoint (http://host:port) the
// container's S3 API is reachable at.
func startMinIO(ctx context.Context, t *testing.T) string {
	t.Helper()
	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "minio/minio:latest",
			ExposedPorts: []string{"9000/tcp"},
			Env: map[string]string{
				"MINIO_ROOT_USER":     minioTestRootUser,
				"MINIO_ROOT_PASSWORD": minioTestRootPassword,
			},
			Cmd:        []string{"server", "/data", "--console-address", ":9001"},
			WaitingFor: wait.ForHTTP("/minio/health/live").WithPort("9000/tcp").WithStartupTimeout(60 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start minio container: %v", err)
	}
	t.Cleanup(func() {
		_ = ctr.Terminate(context.Background())
	})
	host, err := ctr.Host(ctx)
	if err != nil {
		t.Fatalf("minio host: %v", err)
	}
	port, err := ctr.MappedPort(ctx, "9000/tcp")
	if err != nil {
		t.Fatalf("minio port: %v", err)
	}
	return fmt.Sprintf("http://%s:%s", host, port.Port())
}

// TestPresignPublicRoundTrip_LocalMinIO is the integration test finding
// #2225 says was missing: a real PUT/GET against a presigned URL, not just
// an RPC-level check that PresignPublicPutURL/PresignPublicGetURL return a
// URL. Two sub-tests share one MinIO container:
//
//   - "path style" proves the fix: PublicUsePathStyle: true lets a direct PUT
//     and GET round-trip real bytes through the presigned URLs.
//   - "vhost style (regression guard)" proves the fix is load-bearing: the
//     old default (PublicUsePathStyle: false, i.e. omitted) reproduces
//     #2225's exact NoSuchBucket-shaped failure against the same container.
//     If PublicUsePathStyle is ever accidentally dropped or defaulted back
//     to false for this endpoint shape, this sub-test goes red.
func TestPresignPublicRoundTrip_LocalMinIO(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	endpoint := startMinIO(ctx, t)
	const bucket = "s3-integration-test-bucket"

	// Create the bucket via the internal (non-public) client, path-style --
	// this is how any real internal caller talks to MinIO, and matches
	// tools/tilt/minio.tilt's minio-bucket-setup step.
	internal, err := NewClient(ctx, Config{
		Bucket:    bucket,
		Region:    "us-east-1",
		Endpoint:  endpoint,
		AccessKey: minioTestRootUser,
		SecretKey: minioTestRootPassword,
	})
	if err != nil {
		t.Fatalf("NewClient (internal): %v", err)
	}
	if _, err := internal.s3Client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(bucket),
	}); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}

	body := []byte("s3 integration test payload -- " + time.Now().String())

	t.Run("path style", func(t *testing.T) {
		c, err := NewClient(ctx, Config{
			Bucket:             bucket,
			Region:             "us-east-1",
			Endpoint:           endpoint,
			PublicEndpoint:     endpoint,
			PublicUsePathStyle: true,
			AccessKey:          minioTestRootUser,
			SecretKey:          minioTestRootPassword,
		})
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}

		key := "path-style/object.bin"

		putURL, err := c.PresignPublicPutURL(ctx, key, 5*time.Minute)
		if err != nil {
			t.Fatalf("PresignPublicPutURL: %v", err)
		}
		putReq, err := http.NewRequestWithContext(ctx, http.MethodPut, putURL, bytes.NewReader(body))
		if err != nil {
			t.Fatalf("build PUT request: %v", err)
		}
		putReq.ContentLength = int64(len(body))
		putResp, err := http.DefaultClient.Do(putReq)
		if err != nil {
			t.Fatalf("PUT presigned URL: %v", err)
		}
		defer putResp.Body.Close()
		putRespBody, _ := io.ReadAll(putResp.Body)
		if putResp.StatusCode != http.StatusOK && putResp.StatusCode != http.StatusNoContent {
			t.Fatalf("PUT status = %d, want 200/204; body: %s", putResp.StatusCode, putRespBody)
		}

		getURL, err := c.PresignPublicGetURL(ctx, key, 5*time.Minute)
		if err != nil {
			t.Fatalf("PresignPublicGetURL: %v", err)
		}
		getResp, err := http.Get(getURL) //nolint:gosec,noctx
		if err != nil {
			t.Fatalf("GET presigned URL: %v", err)
		}
		defer getResp.Body.Close()
		got, err := io.ReadAll(getResp.Body)
		if err != nil {
			t.Fatalf("read GET body: %v", err)
		}
		if getResp.StatusCode != http.StatusOK {
			t.Fatalf("GET status = %d, want 200; body: %s", getResp.StatusCode, got)
		}
		if !bytes.Equal(got, body) {
			t.Fatalf("round-tripped bytes mismatch: got %q, want %q", got, body)
		}
	})

	t.Run("vhost style (regression guard)", func(t *testing.T) {
		c, err := NewClient(ctx, Config{
			Bucket:         bucket,
			Region:         "us-east-1",
			Endpoint:       endpoint,
			PublicEndpoint: endpoint,
			// PublicUsePathStyle deliberately omitted (false): this is
			// #2225's exact repro shape -- a bare host:port endpoint with no
			// MINIO_DOMAIN, addressed vhost-style.
			AccessKey: minioTestRootUser,
			SecretKey: minioTestRootPassword,
		})
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}

		key := "vhost-style/object.bin"
		putURL, err := c.PresignPublicPutURL(ctx, key, 5*time.Minute)
		if err != nil {
			t.Fatalf("PresignPublicPutURL: %v", err)
		}
		putReq, err := http.NewRequestWithContext(ctx, http.MethodPut, putURL, bytes.NewReader(body))
		if err != nil {
			t.Fatalf("build PUT request: %v", err)
		}
		putReq.ContentLength = int64(len(body))
		putResp, err := http.DefaultClient.Do(putReq)
		if err != nil {
			// A transport-level failure (e.g. TLS/DNS) also counts as "the
			// PUT did not succeed" for this regression guard.
			return
		}
		defer putResp.Body.Close()
		respBody, _ := io.ReadAll(putResp.Body)
		if putResp.StatusCode == http.StatusOK || putResp.StatusCode == http.StatusNoContent {
			t.Fatalf("expected vhost-style PUT against a bare host:port MinIO endpoint to fail (matching #2225's NoSuchBucket repro), got status %d; body: %s", putResp.StatusCode, respBody)
		}
	})
}
