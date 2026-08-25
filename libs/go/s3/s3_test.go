package s3

import (
	"context"
	"net/url"
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
