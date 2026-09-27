package pages

// Markdown rendering for the shell's free-text prose fields: a spec
// entity's body/description (imported from a markdown doc -- see
// krill/ARCHITECTURE/21-markdown-importer.md), a design session's opening
// submission, a task note's body, and a milestone/milepebble's outcome
// sentence. Every one of these is plain-language prose an operator or a
// producer/architect persona wrote in markdown; rendering it through
// templ's default Go-expression escaping drops every paragraph break,
// list, and emphasis the author actually used.
//
// Deliberately NOT applied to a revision event's SummaryLine, an open
// question's Text, or the design-session list row's OpeningSubmission --
// design_page_test.go pins each of those as an exact byte-for-byte match
// against the MCP tool's own wire response, and rendering markdown into any
// of them would silently break that UI/MCP parity contract.
//
// A plain .go file, not a .templ one, despite this package's other types
// living in .templ (BUILD.bazel's go_srcs is otherwise hand-maintained
// empty, per htmxui ARCHITECTURE §12): a generated *_templ.go always
// imports "github.com/a-h/templ" for its own runtime helpers, whether or
// not the source file declares a `templ` component, so a .templ file with
// no component of its own fails to compile as an unused import. This file
// has no `templ` component -- only a caller's own `@templ.Raw(...)` uses
// that package -- so it stays a normal Go file, added to go_srcs by hand.

import (
	"bytes"
	"html"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// prose enables GFM (tables, strikethrough, autolinks) on top of goldmark's
// CommonMark-only default -- mirrors whagent_net/ui/components/session.templ's
// chatMarkdown and audience_score_system/web/schedule's scriptMarkdown, the
// two existing goldmark uses in this repo.
var prose = goldmark.New(goldmark.WithExtensions(extension.GFM))

// renderMarkdown converts source to sanitized HTML, backed by goldmark's
// default (Unsafe: false) HTML renderer -- goldmark's own package doc
// guarantees raw HTML in the source (e.g. a literal "<script>" tag) is
// replaced with an HTML comment placeholder rather than rendered or escaped
// inline, so operator- or agent-authored prose can never inject markup.
// Mirrors audience_score_system/web/schedule's renderScriptMarkdown and
// whagent_net/ui's renderMarkdown -- the same "goldmark over a second
// sanitization dependency" choice, not a third parallel implementation of
// the reasoning. Callers pass the result to templ.Raw, the same escape
// hatch credentials.templ's injected script const uses.
//
// A conversion failure (goldmark's Convert essentially never errors on
// valid UTF-8 input) falls back to the HTML-escaped source rather than
// dropping the field.
func renderMarkdown(source string) string {
	if source == "" {
		return ""
	}
	var buf bytes.Buffer
	if err := prose.Convert([]byte(source), &buf); err != nil {
		return html.EscapeString(source)
	}
	return buf.String()
}
