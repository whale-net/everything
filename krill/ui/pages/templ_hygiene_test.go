package pages

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// templFiles reads every .templ source in this package, keyed by file name.
func templFiles(t *testing.T) map[string]string {
	t.Helper()
	paths, err := filepath.Glob("*.templ")
	if err != nil || len(paths) == 0 {
		t.Fatalf("no .templ sources found: %v", err)
	}
	out := make(map[string]string, len(paths))
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		out[p] = string(b)
	}
	return out
}

// goFieldLine matches a Go struct field declaration ("Name Type"). Inside a
// templ body templ does not parse it as Go: it renders it as page text.
var goFieldLine = regexp.MustCompile(
	`^\s+[A-Z]\w*\s+(\[\]|\*|map\[[\w.]+\])*(string|int|int64|bool|float64|error|any|time\.Time|[A-Z]\w*(\.[A-Z]\w*)?)\s*$`)

// TestTemplBodiesCarryNoGoDeclarations guards against a struct field pasted
// into a templ body, which compiles cleanly and renders as literal text.
func TestTemplBodiesCarryNoGoDeclarations(t *testing.T) {
	for name, src := range templFiles(t) {
		inTempl := false
		for i, line := range strings.Split(src, "\n") {
			switch {
			case strings.HasPrefix(line, "templ "):
				inTempl = true
				continue
			case line == "}":
				inTempl = false
				continue
			}
			if inTempl && goFieldLine.MatchString(line) {
				t.Errorf("%s:%d renders a Go declaration as page text: %q", name, i+1, strings.TrimSpace(line))
			}
		}
	}
}

// popoverTag matches an opening tag carrying the bare popover attribute
// (not popovertarget).
var popoverTag = regexp.MustCompile(`(?s)<[a-z]+\s[^>]*?\bpopover(\s|>|=)[^>]*>`)

var classAttr = regexp.MustCompile(`class="([^"]*)"`)

// displayClasses set a display value, which beats the browser's own
// [popover]:not(:popover-open){display:none} and leaves the popover open.
var displayClasses = map[string]bool{
	"card": true, "flex": true, "inline-flex": true, "grid": true, "inline-grid": true,
	"block": true, "inline-block": true, "table": true, "alert": true, "menu": true,
}

// TestPopoversCarryNoDisplayClass guards the closed state of every native
// popover: a display class on the popover element itself shows it on load.
func TestPopoversCarryNoDisplayClass(t *testing.T) {
	found := 0
	for name, src := range templFiles(t) {
		for _, tag := range popoverTag.FindAllString(src, -1) {
			found++
			m := classAttr.FindStringSubmatch(tag)
			if m == nil {
				continue
			}
			for _, c := range strings.Fields(m[1]) {
				if displayClasses[c] {
					t.Errorf("%s: popover element carries display class %q, so it renders open: %s", name, c, tag)
				}
			}
		}
	}
	if found == 0 {
		t.Fatal("found no popover element; the scan pattern no longer matches the templates")
	}
}

// TestNoDivideColorUtilities guards against divide-<daisy color> classes.
// daisyUI's CDN stylesheet ships border-<color> utilities but not divide-*,
// and the Tailwind browser build does not know daisyUI's palette, so such a
// divider silently renders in the text colour. An arbitrary value,
// divide-[var(--color-base-300)], is generated and resolves correctly.
func TestNoDivideColorUtilities(t *testing.T) {
	divide := regexp.MustCompile(`\bdivide-(base|primary|secondary|accent|neutral|info|success|warning|error)\S*`)
	for name, src := range templFiles(t) {
		for _, m := range divide.FindAllString(src, -1) {
			t.Errorf("%s uses %q, which the CDN stylesheet does not define; use divide-[var(--color-<color>)] instead", name, m)
		}
	}
}
