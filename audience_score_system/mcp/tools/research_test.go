package tools

// Pure-Go, table-driven coverage for validateSourceURL (FR10's
// rejection/NULL-coercion rule) and toResearchNoteOutput's Cited
// derivation. No Postgres or MCP transport needed here -- that end-to-end
// coverage (save_research_note, create_idea, list_research_notes/
// list_ideas against a real database and a real in-process MCP client) is
// research_integration_test.go's job (build tag "integration").
import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/audience_score_system/store"
)

func TestValidateSourceURL_AbsentOrBlankIsNilNeverEmptyString(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"empty string", ""},
		{"whitespace only", "   "},
		{"tabs and newlines", "\t\n  \t"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := validateSourceURL(tc.raw)
			require.NoError(t, err)
			assert.Nil(t, got, "an absent/blank source_url must persist as SQL NULL (nil), never a pointer to an empty string (FR10)")
		})
	}
}

func TestValidateSourceURL_WellFormedAbsoluteHTTPOrHTTPSIsAccepted(t *testing.T) {
	for _, raw := range []string{
		"http://example.com/video",
		"https://example.com/video?x=1",
		"  https://example.com/video  ", // surrounding whitespace trimmed
	} {
		t.Run(raw, func(t *testing.T) {
			got, err := validateSourceURL(raw)
			require.NoError(t, err)
			require.NotNil(t, got)
			assert.NotEmpty(t, *got)
		})
	}
}

func TestValidateSourceURL_RejectsMalformedOrNonHTTPSchemes(t *testing.T) {
	for _, raw := range []string{
		"not a url",
		"javascript:alert(1)",
		"ftp://example.com/file",
		"example.com/no-scheme",
		"http://", // absolute scheme, no host
	} {
		t.Run(raw, func(t *testing.T) {
			got, err := validateSourceURL(raw)
			assert.Error(t, err, "must reject %q rather than silently storing it as a cited source", raw)
			assert.Nil(t, got)
		})
	}
}

func TestToResearchNoteOutput_CitedIsDerivedFromSourceURLNilness(t *testing.T) {
	base := store.ResearchNote{
		ID:             uuid.New(),
		ChannelID:      uuid.New(),
		Text:           "some text",
		AuthorPersonID: uuid.New(),
		CreatedAt:      time.Now(),
	}

	uncited := base
	uncited.SourceURL = nil
	out := toResearchNoteOutput(uncited, "Author Name")
	assert.False(t, out.Cited, "a nil SourceURL must render cited=false")
	assert.Nil(t, out.SourceURL)

	url := "https://example.com/source"
	cited := base
	cited.SourceURL = &url
	out = toResearchNoteOutput(cited, "Author Name")
	assert.True(t, out.Cited, "a non-nil SourceURL must render cited=true")
	require.NotNil(t, out.SourceURL)
	assert.Equal(t, url, *out.SourceURL)
}
