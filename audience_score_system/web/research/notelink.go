package research

import (
	"regexp"

	"github.com/google/uuid"
)

// noteRefPattern matches FR2's recognized syntax: note:<uuid>, case-
// insensitive. The uuid group is deliberately loose on hex-digit case
// (regexp's (?i) flag) so "NOTE:<UPPERCASE-UUID>" matches identically to
// "note:<lowercase-uuid>" -- uuid.Parse below normalizes whatever case was
// typed before it is ever compared against notesOnChannel or rendered into
// an href.
var noteRefPattern = regexp.MustCompile(`(?i)note:([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})`)

// noteTextSegment is one piece of a note's Text after linkifyNoteRefs has
// split it into literal and reference segments (FR1). HRef == "" means
// render Text as plain, unlinked text -- this is both the ordinary
// non-reference-text case AND every FR3 safe-fallback case (unresolvable,
// cross-Channel, or malformed reference); a non-empty HRef means render
// Text as a hyperlink to it. Callers MUST render Text through a templ text
// node, never by string-concatenating into raw HTML, so note text stays
// HTML-escaped regardless of which case applies (FR3's escaping
// requirement).
type noteTextSegment struct {
	Text string
	HRef string
}

// linkifyNoteRefs splits text into literal and reference segments (FR1),
// resolving each recognized note:<uuid> reference (FR2, case-insensitive)
// against notesOnChannel -- the set of notes known to be on the Channel
// currently being viewed, NOT a global id space. This is FR3's
// cross-Channel leak guard: a reference must be resolved against a
// Channel-scoped set the caller already loaded (e.g. from
// store.ResearchStore.ListByChannel/ListFiltered), never a global
// GetByID -- a uuid that is well-formed and belongs to a real note, but on
// a different Channel than the one being viewed, is indistinguishable here
// from one that does not exist at all: both fall through to the plain-text
// case below. anchorFor derives the href for a resolved id (e.g.
// "#note-<uuid>", or a full page path plus that fragment when the
// reference and the referenced note render on different pages) --
// linkifyNoteRefs owns only the parse-and-resolve step; callers own the
// anchor/URL scheme.
//
// FR3's three safe-fallback cases -- a malformed uuid after "note:", a
// well-formed uuid absent from notesOnChannel (unresolvable), and a
// well-formed uuid present in some OTHER Channel's set but not this one
// (cross-Channel, indistinguishable from unresolvable at this layer since
// notesOnChannel is already scoped) -- all come back as a single
// literal-text segment (HRef == "") rather than a link, never a broken or
// leaking href.
func linkifyNoteRefs(text string, notesOnChannel map[uuid.UUID]struct{}, anchorFor func(uuid.UUID) string) []noteTextSegment {
	matches := noteRefPattern.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return []noteTextSegment{{Text: text}}
	}

	segments := make([]noteTextSegment, 0, len(matches)*2+1)
	last := 0
	for _, m := range matches {
		start, end := m[0], m[1]
		idStart, idEnd := m[2], m[3]

		if start > last {
			segments = append(segments, noteTextSegment{Text: text[last:start]})
		}

		id, err := uuid.Parse(text[idStart:idEnd])
		if err != nil {
			// The regex's character class already constrains this to
			// hex-digit-and-hyphen shape, so this should not happen in
			// practice -- fall back to literal text defensively (FR3),
			// never panic on a note's own saved Text.
			segments = append(segments, noteTextSegment{Text: text[start:end]})
			last = end
			continue
		}

		if _, onChannel := notesOnChannel[id]; !onChannel {
			// Unresolvable or off-Channel (FR3) -- literal text, never a
			// broken link or a cross-Channel leak.
			segments = append(segments, noteTextSegment{Text: text[start:end]})
			last = end
			continue
		}

		segments = append(segments, noteTextSegment{Text: text[start:end], HRef: anchorFor(id)})
		last = end
	}
	if last < len(text) {
		segments = append(segments, noteTextSegment{Text: text[last:]})
	}
	return segments
}

// noteRefSegments is views.templ's noteBody's single call into
// linkifyNoteRefs (FR1): it builds notesOnChannel and anchorFor from
// targets -- research.go's resolveNoteRefTargets result, id -> IdeaID
// (nil meaning unattached) for every note:<uuid> reference this page
// could resolve to a note on channelID -- so noteBody itself never
// touches notesOnChannel/anchorFor construction directly. anchorFor
// points at wherever the target note renders as itself (FR1): its Idea's
// detail page when it has one, else the Channel index's unattached
// section -- both keyed off the SAME "note-<uuid>" anchor id views.templ
// emits on noteBody's outer div, regardless of which page the reference
// itself appears on.
func noteRefSegments(text string, channelID uuid.UUID, targets map[uuid.UUID]*uuid.UUID) []noteTextSegment {
	onChannel := make(map[uuid.UUID]struct{}, len(targets))
	for id := range targets {
		onChannel[id] = struct{}{}
	}
	anchorFor := func(id uuid.UUID) string {
		base := "/channels/" + channelID.String()
		if ideaID := targets[id]; ideaID != nil {
			return base + "/research/ideas/" + ideaID.String() + "#note-" + id.String()
		}
		return base + "/research#note-" + id.String()
	}
	return linkifyNoteRefs(text, onChannel, anchorFor)
}
