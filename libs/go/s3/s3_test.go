package s3

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestPresignPublicGetURL covers issue #979/#983/#1101's PresignPublicGetURL
// contract: a signed, virtual-hosted-style GET URL built from the client's
// public endpoint + bucket + key.
//
// Virtual-hosted-style (<bucket>.<host>/<key>), not path-style
// (<host>/<bucket>/<key>): OVH's public endpoint rejects path-style
// requests with HTTP 400 (issue #1101 -- prod v0.10.0 release-tools
// download broke because of this, back when a manual unsigned URL builder
// was used instead of the SDK's own presigner).
//
// Signed rather than unsigned: OVH's release-tools bucket was never
// actually configured for anonymous/public reads, so an unsigned URL came
// back 403 regardless of addressing style (issue #1101, follow-up).
// Presigning with the caller's own (write) credentials works without any
// bucket ACL change -- any identity with read access to the object can
// hand an external consumer a working, time-limited link.
func TestPresignPublicGetURL(t *testing.T) {
	cases := []struct {
		name           string
		publicEndpoint string
		bucket         string
		key            string
		wantHost       string
		wantPath       string
	}{
		{
			name:           "trailing slash on endpoint is normalized",
			publicEndpoint: "https://s3.example.com/",
			bucket:         "release-tools-bucket",
			key:            "release_helper_go/v1.2.3/release_helper_go-linux-amd64",
			wantHost:       "release-tools-bucket.s3.example.com",
			wantPath:       "/release_helper_go/v1.2.3/release_helper_go-linux-amd64",
		},
		{
			name:           "no trailing slash on endpoint",
			publicEndpoint: "https://s3.example.com",
			bucket:         "release-tools-bucket",
			key:            "release_helper_go/v1.2.3/checksums.txt",
			wantHost:       "release-tools-bucket.s3.example.com",
			wantPath:       "/release_helper_go/v1.2.3/checksums.txt",
		},
		{
			name:           "key with multiple slashes is passed through unmodified",
			publicEndpoint: "https://s3.example.com",
			bucket:         "bucket",
			key:            "a/b/c/d.txt",
			wantHost:       "bucket.s3.example.com",
			wantPath:       "/a/b/c/d.txt",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := NewClient(context.Background(), Config{
				Bucket:         tc.bucket,
				Region:         "us-east-1",
				PublicEndpoint: tc.publicEndpoint,
				AccessKey:      "test-access-key",
				SecretKey:      "test-secret-key",
			})
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}

			got, err := c.PresignPublicGetURL(context.Background(), tc.key, 15*time.Minute)
			if err != nil {
				t.Fatalf("PresignPublicGetURL: %v", err)
			}

			u, err := url.Parse(got)
			if err != nil {
				t.Fatalf("PresignPublicGetURL returned unparseable URL %q: %v", got, err)
			}
			if u.Scheme != "https" {
				t.Errorf("scheme = %q, want %q", u.Scheme, "https")
			}
			if u.Host != tc.wantHost {
				t.Errorf("host = %q, want %q (virtual-hosted-style, not path-style)", u.Host, tc.wantHost)
			}
			if u.Path != tc.wantPath {
				t.Errorf("path = %q, want %q", u.Path, tc.wantPath)
			}

			q := u.Query()
			if sig := q.Get("X-Amz-Signature"); sig == "" {
				t.Error("missing X-Amz-Signature -- URL is not actually signed")
			}
			if alg := q.Get("X-Amz-Algorithm"); alg != "AWS4-HMAC-SHA256" {
				t.Errorf("X-Amz-Algorithm = %q, want AWS4-HMAC-SHA256", alg)
			}
		})
	}
}

// TestPresignPublicGetURL_NoPublicEndpointConfigured covers the case where
// a Client was built without Config.PublicEndpoint -- ResolveBinaryURL
// depends on this failing loudly rather than silently falling back to an
// internal-only signer that produces a URL an external CI job can't reach.
func TestPresignPublicGetURL_NoPublicEndpointConfigured(t *testing.T) {
	c, err := NewClient(context.Background(), Config{
		Bucket:    "bucket",
		Region:    "us-east-1",
		AccessKey: "test-access-key",
		SecretKey: "test-secret-key",
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := c.PresignPublicGetURL(context.Background(), "key", time.Minute); err == nil {
		t.Error("expected an error with no public endpoint configured, got nil")
	}
}

// TestAddressingStyleAgreement verifies that all URL producers (internal presign
// for PUT, public presign for PUT/GET) use the correct and mutually-agreed
// addressing style. This is the only unit-level coverage of addressing style
// across the plan (FR-51).
//
// - Internal presign (PresignPutURL, used by backup infrastructure) uses
//   path-style: https://endpoint/bucket/key
// - Public presign (PresignPublicPutURL, PresignPublicGetURL, used for
//   external distribution) uses virtual-hosted style: https://bucket.endpoint/key
//   because OVH's public endpoint rejects path-style with HTTP 400.
//
// This test is red/green: flipping either producer's addressing style will cause
// the test to fail, catching accidental breakage before production sees it.
func TestAddressingStyleAgreement(t *testing.T) {
	cases := []struct {
		name                    string
		internalEndpoint        string
		publicEndpoint          string
		bucket                  string
		key                     string
		wantInternalPathStyle   bool
		wantPublicVirtualHosted bool
	}{
		{
			name:                    "OVH-like endpoints",
			internalEndpoint:        "https://s3.ovh-internal.example.com",
			publicEndpoint:          "https://s3.ovh-public.example.com",
			bucket:                  "release-tools",
			key:                     "binary/v1.0.0/app",
			wantInternalPathStyle:   true,
			wantPublicVirtualHosted: true,
		},
		{
			name:                    "path-style and virtual-hosted endpoints",
			internalEndpoint:        "https://s3.example.com",
			publicEndpoint:          "https://s3.example.com",
			bucket:                  "my-bucket",
			key:                     "path/to/artifact.tar.gz",
			wantInternalPathStyle:   true,
			wantPublicVirtualHosted: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := NewClient(context.Background(), Config{
				Bucket:         tc.bucket,
				Region:         "us-east-1",
				Endpoint:       tc.internalEndpoint,
				PublicEndpoint: tc.publicEndpoint,
				AccessKey:      "test-access-key",
				SecretKey:      "test-secret-key",
			})
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}

			// Test internal presign (PresignPutURL) uses path-style.
			internalURL, err := c.PresignPutURL(context.Background(), tc.key, time.Hour)
			if err != nil {
				t.Fatalf("PresignPutURL: %v", err)
			}

			u, err := url.Parse(internalURL)
			if err != nil {
				t.Fatalf("failed to parse internal presign URL %q: %v", internalURL, err)
			}

			// Path-style: endpoint appears in Host, bucket in Path
			// Virtual-hosted: bucket appears in Host
			isPathStyle := strings.Contains(u.Path, tc.bucket)
			if tc.wantInternalPathStyle && !isPathStyle {
				t.Errorf("PresignPutURL should use path-style, got virtual-hosted URL: %s (host=%s, path=%s)",
					internalURL, u.Host, u.Path)
			}

			// Test public presign (PresignPublicPutURL) uses virtual-hosted style.
			publicURL, _, err := c.PresignPublicPutURL(context.Background(), tc.key, "application/octet-stream", time.Hour)
			if err != nil {
				t.Fatalf("PresignPublicPutURL: %v", err)
			}

			u, err = url.Parse(publicURL)
			if err != nil {
				t.Fatalf("failed to parse public presign URL %q: %v", publicURL, err)
			}

			// Virtual-hosted: bucket appears in the host as prefix
			hasVirtualHosted := strings.HasPrefix(u.Host, tc.bucket+".")
			if tc.wantPublicVirtualHosted && !hasVirtualHosted {
				t.Errorf("PresignPublicPutURL should use virtual-hosted style, got path-style URL: %s (host=%s, path=%s)",
					publicURL, u.Host, u.Path)
			}

			// Also test public presign GET uses the same virtual-hosted style.
			getURL, err := c.PresignPublicGetURL(context.Background(), tc.key, time.Hour)
			if err != nil {
				t.Fatalf("PresignPublicGetURL: %v", err)
			}

			u, err = url.Parse(getURL)
			if err != nil {
				t.Fatalf("failed to parse public GET presign URL %q: %v", getURL, err)
			}

			hasVirtualHostedGet := strings.HasPrefix(u.Host, tc.bucket+".")
			if tc.wantPublicVirtualHosted && !hasVirtualHostedGet {
				t.Errorf("PresignPublicGetURL should use virtual-hosted style, got path-style URL: %s (host=%s, path=%s)",
					getURL, u.Host, u.Path)
			}
		})
	}
}

// TestPresignPublicPutURL covers the public-endpoint presigned PUT (FR-5).
// Verifies that presigned URLs are generated with the correct addressing style
// (virtual-hosted, not path-style) and include signed headers that a producer
// must include when making the actual PUT request.
func TestPresignPublicPutURL(t *testing.T) {
	cases := []struct {
		name           string
		publicEndpoint string
		bucket         string
		key            string
		contentType    string
		wantHost       string
		wantPath       string
	}{
		{
			name:           "OVH public endpoint with content type",
			publicEndpoint: "https://s3.ovh.example.com",
			bucket:         "release-tools",
			key:            "helper/v1.0.0/app-linux",
			contentType:    "application/octet-stream",
			wantHost:       "release-tools.s3.ovh.example.com",
			wantPath:       "/helper/v1.0.0/app-linux",
		},
		{
			name:           "public endpoint with custom content type",
			publicEndpoint: "https://cdn.example.com",
			bucket:         "artifacts",
			key:            "release/v2.0.0/app.tar.gz",
			contentType:    "application/gzip",
			wantHost:       "artifacts.cdn.example.com",
			wantPath:       "/release/v2.0.0/app.tar.gz",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := NewClient(context.Background(), Config{
				Bucket:         tc.bucket,
				Region:         "us-east-1",
				PublicEndpoint: tc.publicEndpoint,
				AccessKey:      "test-access-key",
				SecretKey:      "test-secret-key",
			})
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}

			urlStr, headers, err := c.PresignPublicPutURL(context.Background(), tc.key, tc.contentType, time.Hour)
			if err != nil {
				t.Fatalf("PresignPublicPutURL: %v", err)
			}

			u, err := url.Parse(urlStr)
			if err != nil {
				t.Fatalf("PresignPublicPutURL returned unparseable URL %q: %v", urlStr, err)
			}

			if u.Host != tc.wantHost {
				t.Errorf("host = %q, want %q (virtual-hosted-style, not path-style)", u.Host, tc.wantHost)
			}
			if u.Path != tc.wantPath {
				t.Errorf("path = %q, want %q", u.Path, tc.wantPath)
			}

			// Verify headers are returned (the caller must include these in their PUT)
			if len(headers) == 0 {
				t.Error("PresignPublicPutURL should return signed headers, got empty map")
			}

			// Verify signature is present in URL
			q := u.Query()
			if sig := q.Get("X-Amz-Signature"); sig == "" {
				t.Error("missing X-Amz-Signature -- URL is not actually signed")
			}
		})
	}
}

// TestPresignPublicPutURL_NoPublicEndpointConfigured covers the case where
// a Client was built without Config.PublicEndpoint.
func TestPresignPublicPutURL_NoPublicEndpointConfigured(t *testing.T) {
	c, err := NewClient(context.Background(), Config{
		Bucket:    "bucket",
		Region:    "us-east-1",
		AccessKey: "test-access-key",
		SecretKey: "test-secret-key",
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, _, err := c.PresignPublicPutURL(context.Background(), "key", "text/plain", time.Minute); err == nil {
		t.Error("expected an error with no public endpoint configured, got nil")
	}
}
