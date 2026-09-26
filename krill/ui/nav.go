package main

import (
	"strings"

	"github.com/whale-net/everything/krill/ui/components"
)

// navArea is one top-level destination in the shell's persistent nav.
// The three areas the shell exists to expose -- ops console,
// design-session browser, spec+delivery browser -- are FR 85a8b33c's
// contract; the credential widget is the page that predates the shell and
// stays reachable from it.
//
// The chrome that renders these lives in krill/ui/components/layout.templ
// (a wrapper around //libs/go/htmxui's Shell); this file keeps only what
// is krill's own: the area table and the rule for deciding which one is
// active. htmxui.Shell hardcodes no nav of its own, by design.
type navArea struct {
	// Path is this area's own route prefix on this binary.
	Path string

	// Label is the nav link's text.
	Label string

	// Blurb is the one-line description the shell's home page shows
	// beside the link.
	Blurb string
}

// navAreas is the shell's nav, in render order. Kept as package-level
// state rather than per-request data: the set is fixed at build time and
// every page renders the same nav.
var navAreas = []navArea{
	{
		Path:  opsPath,
		Label: "Ops console",
		Blurb: "Claimed, escalated, and cancelled tasks, and open notes.",
	},
	{
		Path:  designPath,
		Label: "Design sessions",
		Blurb: "Browse design sessions, their revisions, and open questions.",
	},
	{
		Path:  specPath,
		Label: "Spec & delivery",
		Blurb: "The spec entities, decisions, and milestone delivery status.",
	},
	{
		Path:  credentialsPath,
		Label: "Credentials",
		Blurb: "Mint a static bearer token for an MCP client.",
	},
}

// navIsActive reports whether the page being rendered belongs to this nav
// area, so an operator can see where they are without reading the URL.
// Matched at path-segment boundaries rather than by raw prefix: an area
// root is a prefix of its sub-pages ("/ops" of "/ops/claimed"), but not
// of an unrelated sibling that merely starts with the same characters
// ("/opsarchive").
func navIsActive(area navArea, activePath string) bool {
	if activePath == area.Path {
		return true
	}
	return strings.HasPrefix(activePath, area.Path+"/")
}

// navLinks turns the area table into the chrome's nav slot, marking the
// area the page being rendered belongs to.
func navLinks(activePath string) []components.NavLink {
	links := make([]components.NavLink, 0, len(navAreas))
	for _, area := range navAreas {
		links = append(links, components.NavLink{
			Label:  area.Label,
			Href:   area.Path,
			Active: navIsActive(area, activePath),
		})
	}
	return links
}
