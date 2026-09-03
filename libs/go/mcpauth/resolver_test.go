package mcpauth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestCallerResolverFunc_RoundTripsIdentityAndOK proves CallerResolverFunc
// is a plain pass-through adapter: whatever the wrapped function returns is
// exactly what ResolveCaller reports, for both the resolved and
// not-resolved cases.
func TestCallerResolverFunc_RoundTripsIdentityAndOK(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	t.Run("resolved", func(t *testing.T) {
		f := CallerResolverFunc(func(r *http.Request) (string, bool) {
			assert.Same(t, req, r, "the exact *http.Request passed to ResolveCaller must reach the wrapped func")
			return "person-123", true
		})

		var resolver CallerResolver = f
		identity, ok := resolver.ResolveCaller(req)
		assert.True(t, ok)
		assert.Equal(t, "person-123", identity)
	})

	t.Run("not resolved", func(t *testing.T) {
		f := CallerResolverFunc(func(*http.Request) (string, bool) {
			return "", false
		})

		var resolver CallerResolver = f
		identity, ok := resolver.ResolveCaller(req)
		assert.False(t, ok)
		assert.Equal(t, "", identity)
	})
}
