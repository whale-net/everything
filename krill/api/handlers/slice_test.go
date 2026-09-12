// External (black-box) test for Slice.Register (slice.go, issue #2491's
// Testing section: "Handler tests for the four endpoints"). Proves all
// four granularities' routes are actually mounted at their documented
// paths -- slice_internal_test.go separately proves the shared handle()
// adapter's id-parsing/error-mapping logic once, generically.
package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/slice"
)

// TestSliceRegister_MountsAllFourGranularities proves each of the four
// documented paths is wired to the handle() adapter, not left unmatched
// (which would 404 from the mux itself, indistinguishable at a glance from
// a mounted-but-broken route -- an invalid id path value disambiguates:
// only a mounted route reaches handle() and returns 400 for it).
// slice.NewQuerier(nil) is safe here because an invalid id is rejected
// before the querier's underlying store is ever touched (see slice.go's
// handle: uuid.Parse fails first).
func TestSliceRegister_MountsAllFourGranularities(t *testing.T) {
	mux := http.NewServeMux()
	handlers.NewSlice(slice.NewQuerier(nil)).Register(mux)

	paths := []string{
		"/slices/feature-sets/not-a-uuid",
		"/slices/features/not-a-uuid",
		"/slices/requirements/not-a-uuid",
		"/slices/products/not-a-uuid",
	}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusBadRequest, rec.Code, "expected %s to be mounted and reach handle()'s id validation", path)
		})
	}
}
