package schedule

import (
	"bytes"

	"github.com/yuin/goldmark"
)

// renderScriptMarkdown converts a video script's raw markdown body to
// sanitized HTML for FR14's rendered-preview mode. Backed by goldmark
// with its default (Unsafe: false) HTML renderer -- goldmark's own
// package doc guarantees raw HTML in the source (block or inline, e.g. a
// literal "<script>" tag) is NOT passed through: it is replaced with an
// HTML comment placeholder rather than rendered or escaped inline, so a
// script body containing "<script>alert(1)</script>" can never execute
// in the rendered preview (FR14's sanitization requirement). This is the
// one markdown->HTML conversion path for both the create form's preview
// (create.go's HandleCreateScript, when Implementation wires the
// preview toggle) and the per-script detail page's preview
// (HandleScriptDetail) -- never a second, parallel conversion.
//
// goldmark was chosen over adding a second HTML-sanitization dependency
// (e.g. bluemonday) because its default-safe renderer already satisfies
// FR14's "must not execute" requirement without needing an additional
// allow-list pass; there is no other markdown library already vendored
// in this repo (checked MODULE.bazel's existing go_deps.from_file(go.mod)
// set before adding this one -- see go.mod/go.sum and MODULE.bazel's
// use_repo list for the new "com_github_yuin_goldmark" entry, added via
// `go get` + `bazel run //:gazelle` per this task's Scaffold scope).
//
// Returns the rendered HTML as a plain string; the caller (templ view,
// added in Implementation) is responsible for marking it trusted via
// templ.Raw at the render call site -- renderScriptMarkdown itself does
// not decide how the caller injects it.
func renderScriptMarkdown(source string) (string, error) {
	var buf bytes.Buffer
	if err := goldmark.Convert([]byte(source), &buf); err != nil {
		return "", err
	}
	return buf.String(), nil
}
