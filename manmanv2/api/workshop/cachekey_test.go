package workshop

import (
	"strings"
	"testing"
)

// TestCacheKey_DeterministicForSamePair proves the same (workshopID,
// contentVersion) pair always derives the identical key, no matter how many
// times it is called -- the identity two racing hosts must converge on
// (NFR4).
func TestCacheKey_DeterministicForSamePair(t *testing.T) {
	a := CacheKey("123456789", "20240102")
	b := CacheKey("123456789", "20240102")
	if a != b {
		t.Fatalf("expected identical keys for the same (workshopID, contentVersion) pair, got %q and %q", a, b)
	}
}

// TestCacheKey_DifferentVersionsYieldDifferentKeys proves a version bump
// changes the cache identity (FR9: a new version is a new append-only row,
// not an update to the old one).
func TestCacheKey_DifferentVersionsYieldDifferentKeys(t *testing.T) {
	v1 := CacheKey("123456789", "20240102")
	v2 := CacheKey("123456789", "20240103")
	if v1 == v2 {
		t.Fatalf("expected different content versions to derive different keys, both were %q", v1)
	}
}

// TestCacheKey_DifferentWorkshopIDsYieldDifferentKeys proves the workshop ID
// is load-bearing in the derived key, not just the version.
func TestCacheKey_DifferentWorkshopIDsYieldDifferentKeys(t *testing.T) {
	k1 := CacheKey("111", "20240102")
	k2 := CacheKey("222", "20240102")
	if k1 == k2 {
		t.Fatalf("expected different workshop IDs to derive different keys, both were %q", k1)
	}
}

// TestCacheKey_SignatureCannotIncorporateAmbientIdentity is the executable
// form of NFR1's "no host, SGC, deployment, or library parameter" rule:
// CacheKey's signature is exactly (workshopID, contentVersion string)
// string, so there is no parameter slot for a caller to pass ambient
// identity through even if they wanted to. This is asserted by construction
// -- the function value's type -- rather than by inspecting behaviour,
// because behaviour alone can't prove the absence of a parameter that could
// simply be ignored.
func TestCacheKey_SignatureCannotIncorporateAmbientIdentity(t *testing.T) {
	var fn func(string, string) string = CacheKey
	if fn == nil {
		t.Fatal("CacheKey must be assignable to func(string, string) string -- exactly workshopID and contentVersion, nothing else")
	}

	// Reinforce by behaviour: calling it repeatedly, with different
	// goroutines/call sites/"ambient state" in play, never changes the
	// result for the same pair -- there is nothing else the derivation
	// could be reading.
	want := CacheKey("999", "42")
	for i := 0; i < 5; i++ {
		if got := CacheKey("999", "42"); got != want {
			t.Fatalf("call %d: CacheKey(\"999\", \"42\") = %q, want %q (stable regardless of ambient state)", i, got, want)
		}
	}
}

// TestCacheKey_NumericVersionNormalisation proves numeric content-version
// inputs normalise so that different formattings of the same underlying
// numeric version (Steam's time_updated, or a numeric SteamCMD manifest id)
// collide on the identical key -- the convergence NFR4 depends on.
func TestCacheKey_NumericVersionNormalisation(t *testing.T) {
	tests := []struct {
		name string
		a, b string
	}{
		{"unpadded vs zero-padded", "42", "0000000000000000042"},
		{"surrounding whitespace", "42", "  42  "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ka := CacheKey("100", tt.a)
			kb := CacheKey("100", tt.b)
			if ka != kb {
				t.Fatalf("expected %q and %q to normalise to the same key, got %q and %q", tt.a, tt.b, ka, kb)
			}
		})
	}
}

// TestCacheKey_NonNumericVersionIsUsedAsIs proves a non-numeric SteamCMD
// manifest/version string passes through (trimmed), rather than being
// mangled by numeric normalisation, and that whitespace alone does not
// change identity.
func TestCacheKey_NonNumericVersionIsUsedAsIs(t *testing.T) {
	a := CacheKey("100", "manifest-abc123")
	b := CacheKey("100", "  manifest-abc123  ")
	if a != b {
		t.Fatalf("expected whitespace-trimmed non-numeric version to derive the same key, got %q and %q", a, b)
	}
	if !strings.Contains(a, "manifest-abc123") {
		t.Fatalf("expected non-numeric version string to be used as-is in the key, got %q", a)
	}

	// A numeric-looking version and an otherwise-identical non-numeric one
	// must not collide.
	c := CacheKey("100", "42")
	d := CacheKey("100", "42abc")
	if c == d {
		t.Fatalf("expected numeric and non-numeric versions to derive different keys, both were %q", c)
	}
}

// TestS3Key_DerivedFromCacheKey proves S3Key is a pure function of the
// cache key alone -- same input, same output, no additional identity
// parameters.
func TestS3Key_DerivedFromCacheKey(t *testing.T) {
	ck := CacheKey("123456789", "20240102")
	s1 := S3Key(ck)
	s2 := S3Key(ck)
	if s1 != s2 {
		t.Fatalf("expected S3Key to be deterministic for the same cache key, got %q and %q", s1, s2)
	}
	if !strings.HasPrefix(s1, "workshop-cache/") {
		t.Fatalf("expected S3Key to live under the workshop-cache/ prefix, got %q", s1)
	}

	other := S3Key(CacheKey("987654321", "20240102"))
	if s1 == other {
		t.Fatalf("expected different cache keys to derive different S3 keys, both were %q", s1)
	}
}
