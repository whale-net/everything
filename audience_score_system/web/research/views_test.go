package research

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/audience_score_system/web/components"
)

func TestIdeaDetail_ResponsiveLayoutAndFloatingBox(t *testing.T) {
	chID := uuid.New()
	ideaID := uuid.New()
	authorID := uuid.New()
	noteID := uuid.New()
	sourceURL := "https://www.youtube.com/watch?v=1234567890abcdefghijklmnopqrstuvwxyz"

	data := components.LayoutData{
		Title: "Idea Detail Test",
	}
	ch := store.Channel{
		ID:    chID,
		Title: "Test Channel",
	}
	idea := store.Idea{
		ID:        ideaID,
		Title:     "Test Idea with Very Long Title",
		ChannelID: chID,
	}
	notes := []store.ResearchNoteWithAuthor{
		{
			ResearchNote: store.ResearchNote{
				ID:        noteID,
				IdeaID:    &ideaID,
				ThreadID:  uuid.New(),
				Text:      "Here is a note with very long text that must wrap nicely without overflow.",
				SourceURL: &sourceURL,
				CreatedAt: time.Now(),
			},
			AuthorDisplayName: "Test Author",
		},
	}
	relationsByNote := map[uuid.UUID][]store.NoteRelation{}
	noteRefTargets := map[uuid.UUID]*uuid.UUID{}
	threads := []store.ThreadSummary{}
	current := &store.Verdict{
		ID:             uuid.New(),
		IdeaID:         ideaID,
		Version:        1,
		Verdict:        store.VerdictViable,
		Reasoning:      "Strong premise and audience demand.",
		AuthorPersonID: authorID,
		Source:         store.VerdictSourceHuman,
		CreatedAt:      time.Now(),
	}
	authorNames := map[uuid.UUID]string{authorID: "Test Analyst"}
	citedNotes := map[uuid.UUID]store.ResearchNote{}
	retiredNotes := map[uuid.UUID][]store.RelationType{}
	canWrite := true
	form := noteFormData{IdempotencyKey: "key-1", IdeaID: ideaID.String()}
	verdictForm := verdictFormData{IdempotencyKey: "key-2"}
	strategies := []store.StrategyDetail{}
	proposeForm := proposeFormData{}

	component := IdeaDetail(
		data,
		ch,
		idea,
		notes,
		relationsByNote,
		noteRefTargets,
		false,
		threads,
		current,
		authorNames,
		citedNotes,
		retiredNotes,
		canWrite,
		form,
		verdictForm,
		strategies,
		proposeForm,
	)

	var buf bytes.Buffer
	err := component.Render(context.Background(), &buf)
	require.NoError(t, err)
	html := buf.String()

	// 1. Widescreen container class must be present to fill more of the page
	assert.Contains(t, html, "xl:max-w-7xl", "layout container should expand on widescreen")
	assert.Contains(t, html, "2xl:max-w-[1600px]", "layout container should expand on 2xl widescreen")

	// 2. Widescreen flex layout container
	assert.Contains(t, html, "verdict-layout-container", "should have responsive layout container")
	assert.Contains(t, html, "xl:flex", "should use flex row on widescreen")

	// 3. Floating verdict panel for portrait/mobile (including 1080x1920)
	assert.Contains(t, html, "verdict-floating-panel", "verdict panel should have floating panel class")
	assert.Contains(t, html, "id=\"verdict-panel\"", "verdict panel element must exist")

	// 4. Floating toggle button for mobile/portrait
	assert.Contains(t, html, "verdict-floating-btn", "floating toggle button should exist")
	assert.Contains(t, html, "id=\"verdict-floating-btn\"", "verdict toggle button element must exist")
	assert.Contains(t, html, "data-toggle-verdict-box", "toggle attribute should be present")

	// 5. Minimize button inside panel
	assert.Contains(t, html, "verdict-minimize-btn", "minimize button should exist inside panel")

	// 6. Text overflow prevention in research notes
	assert.Contains(t, html, "min-w-0 flex-1", "note content should have min-w-0 flex-1 to prevent flex overflow")
	assert.Contains(t, html, "whitespace-pre-wrap break-words", "note text should wrap and break words")
	assert.Contains(t, html, "break-all", "source URL should break-all to prevent horizontal scroll")

	// 7. Full-width button on save verdict form
	assert.Contains(t, html, "btn btn-primary btn-sm w-full mt-3", "save verdict button should be full width in the sidebar")

	// 8. Responsive style tag defining portrait 1080x1920 floating box and landscape widescreen
	assert.Contains(t, html, "@media (orientation: portrait), (max-width: 1279px)", "responsive style for portrait/narrow monitors must be present")
	assert.Contains(t, html, "@media (min-width: 1280px) and (orientation: landscape)", "responsive style for widescreen monitors must be present")
}
