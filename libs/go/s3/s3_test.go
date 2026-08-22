package s3

import "testing"

// TestPublicURL covers issue #979/#983's PublicURL contract: an unsigned,
// path-style URL built from the client's configured public endpoint +
// bucket + key, with no network call and no signing. Constructed directly
// via a struct literal (an internal test, same package) rather than
// NewClient, since NewClient loads real AWS SDK config and would make this
// a network-dependent test for a method that itself makes no network call.
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
			want:           "https://s3.example.com/release-tools-bucket/release_helper_go/v1.2.3/release_helper_go-linux-amd64",
		},
		{
			name:           "no trailing slash on endpoint",
			publicEndpoint: "https://s3.example.com",
			bucket:         "release-tools-bucket",
			key:            "release_helper_go/v1.2.3/checksums.txt",
			want:           "https://s3.example.com/release-tools-bucket/release_helper_go/v1.2.3/checksums.txt",
		},
		{
			name:           "key with multiple slashes is passed through unmodified",
			publicEndpoint: "https://s3.example.com",
			bucket:         "bucket",
			key:            "a/b/c/d.txt",
			want:           "https://s3.example.com/bucket/a/b/c/d.txt",
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
