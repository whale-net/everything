package render_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/render"
	"github.com/whale-net/everything/krill/slice"
)

type stampedSource struct {
	*fakeSource
	at time.Time
}

func (s stampedSource) ProductSourceTime(context.Context, uuid.UUID) (time.Time, error) {
	return s.at, nil
}

func renderAt(t *testing.T, doc slice.Document, at time.Time) render.Files {
	t.Helper()
	src := stampedSource{&fakeSource{Doc: doc}, at}
	files, err := render.Render(context.Background(), src, uuid.New(), uuid.New())
	require.NoError(t, err)
	return files
}

func readFrom(m map[string]string) render.ReadFunc {
	return func(rel string) (string, bool) { c, ok := m[rel]; return c, ok }
}

func testDoc() slice.Document {
	return slice.Document{Product: &slice.ProductEntity{EntityRef: newRef(), Name: "demo", Vision: "v"}}
}

func TestStamp_EveryFileCarriesSourceStamp(t *testing.T) {
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	files := renderAt(t, testDoc(), at)
	for rel, c := range files.FileMap() {
		st, ok := render.ParseStamp(c)
		require.True(t, ok, rel)
		assert.True(t, st.Time.Equal(at), rel)
		assert.Equal(t, files.Stamp.Revision, st.Revision)
	}
}

func TestStale_IdenticalIgnoresRenderTimestamp(t *testing.T) {
	doc := testDoc()
	at := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	files := renderAt(t, doc, at)
	committed := map[string]string{}
	for rel, c := range files.FileMap() {
		committed[rel] = strings.Replace(c, time.Now().UTC().Format("2006-01-02T"), "1999-01-01T", 1)
	}
	assert.Empty(t, render.Stale(files, readFrom(committed)))
}

func TestStale_DifferingAndMissingNamed(t *testing.T) {
	files := renderAt(t, testDoc(), time.Now())
	committed := files.FileMap()
	committed["PRODUCT.md"] += "hand edit\n"
	delete(committed, "product/03-roadmap.md")
	assert.Equal(t, []string{"PRODUCT.md", "product/03-roadmap.md"}, render.Stale(files, readFrom(committed)))
}

func TestCheckNotNewer(t *testing.T) {
	doc := testDoc()
	newer := renderAt(t, doc, time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)).FileMap()
	older := renderAt(t, doc, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))

	// Committed newer than live data: refused.
	err := render.CheckNotNewer(older, readFrom(newer))
	require.ErrorIs(t, err, render.ErrNewerCommittedSource)

	// Committed older or equal: allowed. Nothing committed: allowed.
	assert.NoError(t, render.CheckNotNewer(renderAt(t, doc, time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)), readFrom(newer)))
	assert.NoError(t, render.CheckNotNewer(older, readFrom(older.FileMap())))
	assert.NoError(t, render.CheckNotNewer(older, readFrom(nil)))
}
