// wireframe assembles a directory of static screen fragments into a single
// preview.html for design iteration. Styling comes from pinned CDN builds of
// daisyUI and the Tailwind browser runtime (same pattern as the real UI, which
// loads Tailwind from a CDN), so viewing the output requires network access.
//
// Input layout (see README.md):
//
//	<dir>/_shell.html      optional shared chrome; <!-- wf:screen --> marks
//	                       where each screen's body is injected
//	<dir>/screens/*.html   one fragment per screen, first line holds metadata:
//	                       <!-- wf: name="servers" title="Servers" -->
//
// Screens are ordered by filename; the first one is the default route.
// Links between screens use href="#/<name>".
package main

import (
	_ "embed"
	"flag"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"
)

//go:embed template.html
var pageTemplate string

//go:embed themes.css
var themesCSS string

type screen struct {
	Name   string
	Title  string
	Body   string
	Parent string // non-empty = rendered as a layer panel over the parent screen
	Depth  int
	Root   string // top-most base ancestor (for nav highlighting)
}

type pageData struct {
	Title     string
	ThemesCSS string
	Screens   []screen
}

var metaRe = regexp.MustCompile(`<!--\s*wf:(.*?)-->`)
var attrRe = regexp.MustCompile(`(\w+)="([^"]*)"`)

func main() {
	dir := flag.String("dir", "", "wireframe directory containing screens/ and optional _shell.html")
	out := flag.String("out", "", "output file (default <dir>/preview.html)")
	title := flag.String("title", "Wireframes", "page title suffix")
	flag.Parse()

	if *dir == "" {
		fmt.Fprintln(os.Stderr, "usage: wireframe --dir <path> [--out <file>] [--title <name>]")
		os.Exit(2)
	}
	// `bazel run` executes in the runfiles tree; resolve user-relative paths
	// against the invocation directory.
	if wd := os.Getenv("BUILD_WORKING_DIRECTORY"); wd != "" {
		if !filepath.IsAbs(*dir) {
			*dir = filepath.Join(wd, *dir)
		}
		if *out != "" && !filepath.IsAbs(*out) {
			*out = filepath.Join(wd, *out)
		}
	}
	if *out == "" {
		*out = filepath.Join(*dir, "preview.html")
	}

	if err := run(*dir, *out, *title); err != nil {
		fmt.Fprintln(os.Stderr, "wireframe:", err)
		os.Exit(1)
	}
}

func run(dir, out, title string) error {
	shell := "<!-- wf:screen -->"
	if b, err := os.ReadFile(filepath.Join(dir, "_shell.html")); err == nil {
		shell = string(b)
	}
	if !strings.Contains(shell, "<!-- wf:screen -->") {
		return fmt.Errorf("_shell.html must contain the marker <!-- wf:screen -->")
	}

	paths, err := filepath.Glob(filepath.Join(dir, "screens", "*.html"))
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return fmt.Errorf("no screen fragments found in %s/screens/", dir)
	}
	sort.Strings(paths)

	var screens []screen
	seen := map[string]string{}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		s, err := parseScreen(string(b))
		if err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(p), err)
		}
		if prev, dup := seen[s.Name]; dup {
			return fmt.Errorf("%s: screen name %q already used by %s", filepath.Base(p), s.Name, prev)
		}
		seen[s.Name] = filepath.Base(p)
		// Base screens get the app shell; layers render as panels over their
		// parent, so the shell (nav etc.) must not repeat inside them.
		if s.Parent == "" {
			s.Body = strings.Replace(shell, "<!-- wf:screen -->", s.Body, 1)
		}
		screens = append(screens, s)
	}
	if err := resolveLayers(screens); err != nil {
		return err
	}

	tmpl, err := template.New("page").Parse(pageTemplate)
	if err != nil {
		return err
	}
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()
	err = tmpl.Execute(f, pageData{
		Title:     title,
		ThemesCSS: themesCSS,
		Screens:   screens,
	})
	if err != nil {
		return err
	}
	fmt.Printf("wrote %s (%d screens)\n", out, len(screens))
	return nil
}

func parseScreen(src string) (screen, error) {
	m := metaRe.FindStringSubmatch(src)
	if m == nil {
		return screen{}, fmt.Errorf(`missing metadata comment: <!-- wf: name="..." title="..." -->`)
	}
	attrs := map[string]string{}
	for _, kv := range attrRe.FindAllStringSubmatch(m[1], -1) {
		attrs[kv[1]] = kv[2]
	}
	name := attrs["name"]
	if name == "" || !regexp.MustCompile(`^[a-z0-9-]+$`).MatchString(name) {
		return screen{}, fmt.Errorf("wf metadata needs name=%q matching [a-z0-9-]+", name)
	}
	title := attrs["title"]
	if title == "" {
		title = name
	}
	return screen{
		Name:   name,
		Title:  html.EscapeString(title),
		Parent: attrs["parent"],
		Body:   strings.TrimSpace(strings.Replace(src, m[0], "", 1)),
	}, nil
}

// resolveLayers validates parent references and computes each layer's depth
// and root base screen.
func resolveLayers(screens []screen) error {
	byName := map[string]*screen{}
	for i := range screens {
		byName[screens[i].Name] = &screens[i]
	}
	for i := range screens {
		s := &screens[i]
		depth, cur := 0, s
		for cur.Parent != "" {
			next, ok := byName[cur.Parent]
			if !ok {
				return fmt.Errorf("screen %q: parent %q does not exist", s.Name, cur.Parent)
			}
			depth++
			if depth > len(screens) {
				return fmt.Errorf("screen %q: parent cycle", s.Name)
			}
			cur = next
		}
		s.Depth = depth
		s.Root = cur.Name
	}
	return nil
}
