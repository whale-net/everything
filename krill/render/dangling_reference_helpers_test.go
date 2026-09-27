package render_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/whale-net/everything/krill/render"
)

// The invariant this file guards: a rendered document must not send a
// reader to content it does not contain.
//
// The defect it exists for was found by measurement, not by a test. The
// whagent_net brief's three LoadBearingDecisions each end "See the
// mapping note on this Product", and note bd9197eb -- the renumbering
// mapping that sentence means -- reached no rendered output at all. The
// document was not silent about the gap; it pointed straight at it.
//
// So this is deliberately not a grep for one literal sentence. The same
// class of defect recurs every time an entity gains prose that
// cross-references another, in wording nobody predicted. What generalizes
// is the check below: find every cross-reference, then ask whether the
// document set actually contains something it could be pointing at.

// seeTheRe finds a cross-reference of the form "see the <phrase>",
// tolerating the trailing prepositional clause that names where the
// target lives ("... on this Product"). It stops at sentence and clause
// punctuation, so one body yields one reference rather than swallowing the
// rest of the paragraph.
var seeTheRe = regexp.MustCompile(`(?i)\bsee the ([^.;:\n]+?)(?:\s+(?:on|in|under|above|below)\b|[.;:\n]|$)`)

// headingRe finds a markdown heading's text, at any level.
var headingRe = regexp.MustCompile(`(?m)^#{1,6}\s+(.+)$`)

// noteLabelRe finds a rendered note's label line -- the bold id line
// renderNotesSection emits, e.g. "**`bd9197eb-...`** — comment — status:
// noted". A note is a referent a cross-reference can legitimately name,
// and its first body line is too, since that is where its subject is.
var noteLabelRe = regexp.MustCompile("(?m)^\\*\\*`[0-9a-f-]{36}`\\*\\*.*$")

// referenceStopWords are dropped before matching a phrase's content words.
// Without them every reference would "match" every referent, because
// "on this Product" shares "this" with nothing useful and the check would
// pass vacuously.
var referenceStopWords = map[string]bool{
	"the": true, "a": true, "an": true, "this": true, "that": true,
	"these": true, "those": true, "it": true, "its": true, "of": true,
	"for": true, "and": true, "or": true, "to": true, "in": true,
	"on": true, "at": true, "by": true, "with": true, "is": true,
	"are": true, "was": true, "here": true, "there": true,
}

// referent is one thing in a rendered file set a cross-reference could
// point at.
type referent struct {
	kind string // "heading" or "note"
	text string
}

// collectReferents returns everything in the file set a reader could be
// sent to: every heading, every rendered note's label, and every note's
// first body line (a note's subject is in its first line, and a reference
// naming that subject is resolving correctly).
func collectReferents(files render.Files) []referent {
	var out []referent
	for _, content := range files.FileMap() {
		out = append(out, referentsIn(content)...)
	}
	return out
}

func referentsIn(content string) []referent {
	var out []referent
	for _, m := range headingRe.FindAllStringSubmatch(content, -1) {
		out = append(out, referent{kind: "heading", text: m[1]})
	}

	// Each note's rendered extent runs from its label line to the next
	// label line, so its first body line can be paired with its label.
	labels := noteLabelRe.FindAllStringIndex(content, -1)
	for i, span := range labels {
		out = append(out, referent{kind: "note", text: content[span[0]:span[1]]})
		body := content[span[1]:]
		if i+1 < len(labels) {
			body = body[:labels[i+1][0]-span[1]]
		}
		if first := firstNonEmptyLine(body); first != "" {
			out = append(out, referent{kind: "note", text: first})
		}
	}
	return out
}

func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}

// danglingReferences returns every cross-reference phrase in the file
// set, with the file it was found in.
func danglingReferences(files render.Files) map[string]string {
	out := map[string]string{}
	for path, content := range files.FileMap() {
		for _, m := range seeTheRe.FindAllStringSubmatch(content, -1) {
			phrase := strings.TrimSpace(m[1])
			if phrase != "" {
				out[phrase] = path
			}
		}
	}
	return out
}

// contentWords is a phrase reduced to the words that actually identify a
// target, lowercased.
func contentWords(phrase string) []string {
	var out []string
	for _, w := range strings.Fields(strings.ToLower(phrase)) {
		w = strings.Trim(w, "()[`*_,:;\"'")
		if w != "" && !referenceStopWords[w] {
			out = append(out, w)
		}
	}
	return out
}

// referencesAMeaningfulTarget is the whole rule, in two parts.
//
// Part one, the note rule: a phrase naming a note (in any wording --
// "note", "notes", "mapping note") requires at least one note to be
// rendered. This is what catches the real defect directly, and it does so
// without knowing the mapping note's subject matter.
//
// Part two, the content rule: otherwise, at least one content word of
// the phrase must appear in some referent. This catches a reference to
// anything else krill holds but render does not surface -- a table, a
// survey, a section that was never wired up.
//
// Either part alone would be too weak. Part two alone misses "mapping
// note", because the mapping note's subject is "renumbering", not
// "mapping". Part one alone only ever catches notes, and would wave
// through "see the risk table" pointing at nothing.
func referencesAMeaningfulTarget(phrase string, referents []referent) (ok bool, reason string) {
	words := contentWords(phrase)
	if len(words) == 0 {
		// Nothing to check against (e.g. "see the"). Not a violation.
		return true, ""
	}

	noteCount := 0
	for _, r := range referents {
		if r.kind == "note" {
			noteCount++
		}
	}

	if namesANote(words) {
		if noteCount == 0 {
			return false, "the reference names a note, but the rendered file set contains no note"
		}
		return true, ""
	}

	lowered := make([]string, len(referents))
	for i, r := range referents {
		lowered[i] = strings.ToLower(r.text)
	}
	for _, w := range words {
		for _, text := range lowered {
			if strings.Contains(text, w) {
				return true, ""
			}
		}
	}
	return false, "no rendered heading or note mentions any of: " + strings.Join(words, ", ")
}

func namesANote(words []string) bool {
	for _, w := range words {
		if strings.TrimSuffix(w, "s") == "note" {
			return true
		}
	}
	return false
}

// assertNoDanglingReferences is the assertion both this file's tests and
// the integration test call.
func assertNoDanglingReferences(t *testing.T, files render.Files) {
	t.Helper()
	referents := collectReferents(files)
	refs := danglingReferences(files)
	for phrase, path := range refs {
		ok, reason := referencesAMeaningfulTarget(phrase, referents)
		if ok {
			continue
		}
		assert.Fail(t, "rendered output references content it does not contain",
			"%s says %q -- %s", path, "see the "+phrase, reason)
	}
}
