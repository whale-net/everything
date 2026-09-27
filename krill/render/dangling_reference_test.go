package render_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/render"
)

// TestRenderedOutput_ReferencesResolveToRenderedContent is the guard over
// the notes and Requirements work in this milepebble: the fixture carries
// whagent_net's exact "See the mapping note on this Product" sentence, and
// the render that resolves it must come back clean.
func TestRenderedOutput_ReferencesResolveToRenderedContent(t *testing.T) {
	src := mappingNoteSrc()
	files, err := render.Render(t.Context(), src, uuid.New(), src.Doc.Product.ID)
	require.NoError(t, err)

	assertNoDanglingReferences(t, files)
}

// TestRenderedOutput_GuardCatchesTheRealDefect is what makes the guard
// worth having. The same fixture, against a renderer that emits no notes,
// must FAIL -- otherwise the assertion above is passing vacuously and
// would not have caught the defect that motivated this task.
func TestRenderedOutput_GuardCatchesTheRealDefect(t *testing.T) {
	src := mappingNoteSrc()
	src.Notes = nil

	files, err := render.Render(t.Context(), src, uuid.New(), src.Doc.Product.ID)
	require.NoError(t, err)

	require.Contains(t, files.ProductMD, "See the mapping note on this Product",
		"the fixture must still carry the real sentence, or this proves nothing")

	referents := collectReferents(files)
	ok, reason := referencesAMeaningfulTarget("mapping note on this Product", referents)
	assert.False(t, ok, "a document with no notes must not pass the guard")
	assert.Contains(t, reason, "no note",
		"the failure must name the actual defect, not a generic mismatch")
}

// The guard must not be a grep for one sentence. Each case below is a
// differently-worded reference to something the document does or does not
// contain; a literal-phrase check would pass all the "missing" rows.
func TestRenderedOutput_GuardGeneralizesBeyondOneSentence(t *testing.T) {
	// A referent the content rule can match: a rendered note's subject.
	const noteSubject = "CAPABILITY RENUMBERING (2026-09-25 onboarding)."

	for _, tc := range []struct {
		name      string
		phrase    string
		referents []referent
		wantOK    bool
	}{
		{"names a note, one is rendered", "mapping note on this Product",
			[]referent{{kind: "note", text: noteSubject}}, true},
		{"names a note, none rendered", "mapping note on this Product",
			[]referent{{kind: "heading", text: "Vision"}}, false},
		{"names notes, none rendered", "see the notes below",
			[]referent{{kind: "heading", text: "Roadmap"}}, false},
		{"different wording, target present", "see the renumbering record",
			[]referent{{kind: "note", text: noteSubject}}, true},
		{"different wording, target absent", "see the renumbering record",
			[]referent{{kind: "heading", text: "Vision"}}, false},
		{"points at a rendered heading", "see the roadmap for delivery status",
			[]referent{{kind: "heading", text: "Roadmap"}}, true},
		{"points at a heading that is not emitted", "see the risk register",
			[]referent{{kind: "heading", text: "Vision"}}, false},
		{"plural heading matches on a content word", "see the capability map",
			[]referent{{kind: "heading", text: "Capability map"}}, true},
		{"a note rendered but empty of the subject", "see the deployment survey",
			[]referent{{kind: "note", text: "unrelated"}}, false},
		{"a phrase of only stopwords has nothing to check", "this that of the",
			[]referent{{kind: "heading", text: "Vision"}}, true},
		{"an empty file set satisfies nothing", "see the vision",
			nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ok, reason := referencesAMeaningfulTarget(tc.phrase, tc.referents)
			assert.Equal(t, tc.wantOK, ok, "reason given: %s", reason)
			if !tc.wantOK {
				assert.NotEmpty(t, reason, "a failure must explain itself")
			}
		})
	}
}

// A cross-reference living in a body, not a heading, is still a
// cross-reference -- the extractor reads the whole file set, so this
// cannot be evaded by where the sentence is put.
func TestRenderedOutput_GuardReadsReferencesFromBodiesNotJustHeadings(t *testing.T) {
	body := "Some prose. See the mapping note on this Product. More prose."
	files := render.Files{ProductMD: body}

	refs := danglingReferences(files)
	require.Contains(t, refs, "mapping note", "a reference inside a body must be found")

	ok, _ := referencesAMeaningfulTarget("mapping note", collectReferents(files))
	assert.False(t, ok, "and it must still be reported as dangling when no note is rendered")
}
