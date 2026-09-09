package s3

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// TestPresignPublicGetURL covers issue #979/#983/#1101's PresignPublicGetURL
// contract: a signed, virtual-hosted-style GET URL built from the client's
// public endpoint + bucket + key.
//
// Virtual-hosted-style (<bucket>.<host>/<key>), not path-style
// (<host>/<bucket>/<key>): OVH's public endpoint rejects path-style
// requests with HTTP 400 (issue #1101 -- prod v0.10.0 release-tools
// download broke because of this, back when this used an unsigned
// PublicURL built by hand rather than the SDK's own presigner).
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

// TestPresignPublicPutURL covers manmanv2 issue #2213's PresignPublicPutURL
// contract, mirroring TestPresignPublicGetURL's structure: a signed,
// virtual-hosted-style PUT URL built from the client's public endpoint +
// bucket + key, addressed via the same presignPublic client
// PresignPublicGetURL already uses (so it inherits the same
// virtual-hosted-style / OVH-400-avoidance rationale documented there).
// Host-manager (manmanv2/README-HOST.md, bare metal) is exactly the
// external, credential-less consumer this exists for -- the finding this
// closes is that GetCacheUploadURL had no public-endpoint PUT presign
// available at all before this method existed.
func TestPresignPublicPutURL(t *testing.T) {
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
			bucket:         "workshop-cache-bucket",
			key:            "workshop-cache/123456/789.tar",
			wantHost:       "workshop-cache-bucket.s3.example.com",
			wantPath:       "/workshop-cache/123456/789.tar",
		},
		{
			name:           "no trailing slash on endpoint",
			publicEndpoint: "https://s3.example.com",
			bucket:         "workshop-cache-bucket",
			key:            "workshop-cache/555/3.tar",
			wantHost:       "workshop-cache-bucket.s3.example.com",
			wantPath:       "/workshop-cache/555/3.tar",
		},
		{
			name:           "key with multiple slashes is passed through unmodified",
			publicEndpoint: "https://s3.example.com",
			bucket:         "bucket",
			key:            "a/b/c/d.tar",
			wantHost:       "bucket.s3.example.com",
			wantPath:       "/a/b/c/d.tar",
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

			got, err := c.PresignPublicPutURL(context.Background(), tc.key, 15*time.Minute)
			if err != nil {
				t.Fatalf("PresignPublicPutURL: %v", err)
			}

			u, err := url.Parse(got)
			if err != nil {
				t.Fatalf("PresignPublicPutURL returned unparseable URL %q: %v", got, err)
			}
			if u.Scheme != "https" {
				t.Errorf("scheme = %q, want %q", u.Scheme, "https")
			}
			if u.Host != tc.wantHost {
				t.Errorf("host = %q, want %q (public endpoint, not the internal one)", u.Host, tc.wantHost)
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

// TestPresignPublicPutURL_NoPublicEndpointConfigured mirrors
// TestPresignPublicGetURL_NoPublicEndpointConfigured: a Client built without
// Config.PublicEndpoint must fail loudly (a real, checkable error) rather
// than nil-pointer panicking on the unset presignPublic client.
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
	if _, err := c.PresignPublicPutURL(context.Background(), "key", time.Minute); err == nil {
		t.Error("expected an error with no public endpoint configured, got nil")
	}
}

// TestIsNoSuchKey covers every detection branch IsNoSuchKey's doc comment
// claims (manmanv2 #2187, FR12's "object already absent is a success"
// eviction convergence): the two typed AWS SDK errors, the string-matching
// fallback for S3-compatible endpoints that don't return one, an unrelated
// error, and nil. cache_test.go's fakePresigner only ever drives the
// string-fallback branch (it fakes the narrow cachePresigner interface, not
// a real AWS SDK error), so the typed branches -- what a real S3 endpoint
// actually returns -- have no coverage without this.
//
// Note the "typed" cases don't actually isolate the errors.As branch from
// the string-fallback one below it: types.NotFound{}.Error() and
// types.NoSuchKey{}.Error() both happen to start with their own type name
// ("NotFound: ", "NoSuchKey: "), so the string fallback would also catch
// them -- confirmed by deliberately deleting the errors.As checks and
// observing these two cases stay green while the string-fallback-only cases
// below go red. They're kept anyway to pin the real SDK types' actual
// behavior (including through Delete's %w wrapping, which the string-only
// cases don't exercise); the string-fallback cases are what carry the
// red/green proof for this test.
//
// Also checks both typed cases survive being wrapped with %w the way
// Delete's own fmt.Errorf("failed to delete from S3: %w", err) wraps them --
// EvictCacheEntry's NoSuchKey convergence calls IsNoSuchKey on exactly that
// wrapped error, never the raw SDK error, so a wrapping regression that
// silently dropped %w down to %v would break real eviction convergence
// without failing anywhere else in this repo.
func TestIsNoSuchKey(t *testing.T) {
	deleteWrap := func(err error) error {
		return fmt.Errorf("failed to delete from S3: %w", err)
	}

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"typed types.NotFound", &types.NotFound{}, true},
		{"typed types.NoSuchKey", &types.NoSuchKey{}, true},
		{"typed types.NotFound wrapped via Delete's %w", deleteWrap(&types.NotFound{}), true},
		{"typed types.NoSuchKey wrapped via Delete's %w", deleteWrap(&types.NoSuchKey{}), true},
		{"string-fallback NoSuchKey", errors.New("simulated: NoSuchKey: the specified key does not exist"), true},
		{"string-fallback NotFound", errors.New("simulated: NotFound"), true},
		{"string-fallback 404 status", errors.New("simulated: StatusCode: 404, RequestID: abc123"), true},
		{"unrelated error", errors.New("simulated: connection refused"), false},
		{"unrelated error wrapped via Delete's %w", deleteWrap(errors.New("simulated: access denied")), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsNoSuchKey(tc.err); got != tc.want {
				t.Errorf("IsNoSuchKey(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
