package s3

import "testing"

// TestPublicURL covers issue #979/#983/#1101's PublicURL contract: an
// unsigned, virtual-hosted-style URL built from the client's configured
// public endpoint + bucket + key, with no network call and no signing.
// Constructed directly via a struct literal (an internal test, same
// package) rather than NewClient, since NewClient loads real AWS SDK
// config and would make this a network-dependent test for a method that
// itself makes no network call.
//
// Virtual-hosted-style (<bucket>.<host>/<key>), not path-style
// (<host>/<bucket>/<key>): OVH's public endpoint rejects path-style
// requests with HTTP 400 (issue #1101 -- prod v0.10.0 release-tools
// download broke because of this).
func TestPublicURL(t *testing.T) {
	cases := []struct {
		name           string
		publicEndpoint string
		bucket         string
		key            string
		want           string
	}{
		{
			name:           "trailing slash on endpoint is normalized",
			publicEndpoint: "https://s3.example.com/",
			bucket:         "release-tools-bucket",
			key:            "release_helper_go/v1.2.3/release_helper_go-linux-amd64",
			want:           "https://release-tools-bucket.s3.example.com/release_helper_go/v1.2.3/release_helper_go-linux-amd64",
		},
		{
			name:           "no trailing slash on endpoint",
			publicEndpoint: "https://s3.example.com",
			bucket:         "release-tools-bucket",
			key:            "release_helper_go/v1.2.3/checksums.txt",
			want:           "https://release-tools-bucket.s3.example.com/release_helper_go/v1.2.3/checksums.txt",
		},
		{
			name:           "key with multiple slashes is passed through unmodified",
			publicEndpoint: "https://s3.example.com",
			bucket:         "bucket",
			key:            "a/b/c/d.txt",
			want:           "https://bucket.s3.example.com/a/b/c/d.txt",
		},
		{
			name:           "endpoint with no scheme defaults to https",
			publicEndpoint: "s3.example.com",
			bucket:         "bucket",
			key:            "a/b.txt",
			want:           "https://bucket.s3.example.com/a/b.txt",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Client{bucket: tc.bucket, publicEndpoint: tc.publicEndpoint}
			got := c.PublicURL(tc.key)
			if got != tc.want {
				t.Errorf("PublicURL(%q) = %q, want %q", tc.key, got, tc.want)
			}
		})
	}
}
