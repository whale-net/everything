package main

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFaviconIsActuallyEmbedded guards the //go:embed directive that
// populates faviconIco.
//
// The directive went missing once already: the variable and the
// FaviconHandler route were both present, so nothing failed to compile
// and no test failed. What shipped was a handler serving a zero-byte
// "image/x-icon" with a 200 -- a worse failure than a 404, because the
// browser sees a valid response and stops asking.
//
// So this asserts the bytes are there, not merely that the route answers.
func TestFaviconIsActuallyEmbedded(t *testing.T) {
	require.NotEmpty(t, faviconIco,
		"favicon.ico must be embedded; without the //go:embed directive this is nil and /favicon.ico serves an empty 200")

	// An ICO header: reserved 0, type 1 (icon), at least one image.
	require.GreaterOrEqual(t, len(faviconIco), 6, "too short to be an .ico at all")
	assert.Equal(t, []byte{0, 0}, faviconIco[0:2], "ICO reserved field must be zero")
	assert.Equal(t, byte(1), faviconIco[2], "ICO type field must be 1 (icon)")
	assert.Equal(t, byte(1), faviconIco[4], "an ICO must contain at least one image")
}

// TestFaviconRouteServesTheEmbeddedBytes is the other half: the route
// exists, is not behind the sign-in gate (a static asset behind auth
// would redirect a browser icon request), and serves the real bytes.
func TestFaviconRouteServesTheEmbeddedBytes(t *testing.T) {
	mux := http.NewServeMux()
	mountStaticRoutes(mux)
	rec := fetch(t, mux, "/favicon.ico")

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "image/x-icon", rec.Header().Get("Content-Type"))
	assert.Equal(t, len(faviconIco), rec.Body.Len(), "the route must serve the embedded bytes, not an empty body")
}
