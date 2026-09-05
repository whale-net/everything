package tools

// Pure-Go coverage for toResearchNoteOutput's Cited derivation.
// source_url's rejection/NULL-coercion rule moved to package store as of
// FR12 (issue #1897) -- its table-driven coverage lives in store's
// SaveNote tests now, not here. No Postgres or MCP
// transport needed here -- that end-to-end coverage (save_research_note,
// create_idea, list_research_notes/list_ideas against a real database and
// a real in-process MCP client) is research_integration_test.go's job
// (build tag "integration").
import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/audience_score_system/store"
)

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
