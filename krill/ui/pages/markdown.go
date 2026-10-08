package pages

// Markdown rendering for free-text prose fields. Fields pinned byte-for-byte
// against MCP responses (SummaryLine, open-question Text) stay unrendered.
// A plain .go file: a .templ with no component fails on its unused import.

import (
	"bytes"
	"html"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// prose enables GFM (tables, strikethrough, autolinks) on top of CommonMark.
var prose = goldmark.New(goldmark.WithExtensions(extension.GFM))

// renderMarkdown converts source to HTML for templ.Raw. goldmark's default
// renderer (Unsafe: false) replaces raw HTML, so authored prose cannot inject
// markup. A conversion failure falls back to the escaped source.
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
