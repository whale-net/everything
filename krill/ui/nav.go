package main

import (
	"strings"

	"github.com/google/uuid"

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

// ── the workspace shell's grouped nav ───────────────────────────────────────
//
// The model below is the chrome the operator facelift's sidebar renders
// (design/wireframes/_shell.html): one unheaded Overview link followed by
// six headed groups. It sits beside the flat navArea table above rather
// than replacing it -- the pages renderShell still serves resolve their
// product server-side and have no product id to scope hrefs with, so they
// keep the flat table until the shell takes over routing.

// navItem is one link in the workspace shell's sidebar.
type navItem struct {
	// Label is the link's text.
	Label string

	// Href is where the link goes. It must resolve on this binary: an
	// item whose redesigned page has not shipped points at the existing
	// page for that area rather than at a route that would 404.
	Href string

	// Path is the route prefix that marks this item active. Usually Href,
	// but split out because an item can link at one page and own another
	// -- Tasks links at the delivery page while the pages it owns are the
	// milestone task subtree.
	//
	// A "*" segment matches any one segment, which is how an item owns a
	// subtree rooted at an id the chrome does not hold: the milestone task
	// and board pages differ by their own trailing segment, not by any
	// one milestone.
	Path string

	// Exact marks an item whose only active page is its own path, with
	// nothing under it. The Spec group's tabs are siblings hanging off
	// the product path, so without it Capabilities would stay lit while
	// an operator is reading Decisions.
	Exact bool
}

// navGroup is one headed section of the sidebar: a title and its items.
// An empty Title renders its items unheaded, which is how Overview sits
// above the groups rather than inside one.
type navGroup struct {
	Title string
	Items []navItem
}

// navTargets carries the ids the sidebar's hrefs are built from. Product
// is the operator's current product; Milestone is the milestone a
// milestone-scoped page is showing, or uuid.Nil on a page that is not
// under one -- a milestone id the chrome cannot know is why the Tasks and
// Board items fall back to the delivery page.
type navTargets struct {
	Product   uuid.UUID
	Milestone uuid.UUID
}

// itemPath is the prefix that marks an item active.
func (i navItem) itemPath() string {
	if i.Path != "" {
		return i.Path
	}
	return i.Href
}

// navItemIsActive reports whether the page being rendered belongs to this
// item. It generalises navIsActive's segment-boundary rule: a page belongs
// to an item when it is at the item's path or under it, unless the item is
// Exact (which owns its path alone). A "*" segment in the item's path
// matches exactly one segment.
func navItemIsActive(item navItem, activePath string) bool {
	pattern := pathSegments(item.itemPath())
	segments := pathSegments(activePath)
	if len(pattern) > len(segments) {
		return false
	}
	for i, seg := range pattern {
		if seg != "*" && seg != segments[i] {
			return false
		}
	}
	// A path the page sits under is only owned by a non-Exact item.
	return len(segments) == len(pattern) || !item.Exact
}

// pathSegments splits a URL path into its non-empty segments. The root is
// the empty path, so it has none -- which is what keeps the Overview item
// from matching every page as a prefix.
func pathSegments(path string) []string {
	path = strings.Trim(path, "/")
	if path == "" {
		return nil
	}
	return strings.Split(path, "/")
}

// workspaceNav builds the shell's grouped nav for a product, marking at
// most one item active.
//
// At most one, first match in render order: two items can share a path --
// Tasks and Board both link at the delivery page until their own pages
// ship -- and a sidebar showing two active items is worse than one showing
// the more specific Work item it currently points through. Overview sits
// first and matches the bare root only, so it is never active while
// another area is.
func workspaceNav(t navTargets, activePath string) []components.NavGroup {
	groups := navGroupTable(t)
	out := make([]components.NavGroup, 0, len(groups))
	marked := false
	for _, g := range groups {
		items := make([]components.NavLink, 0, len(g.Items))
		for _, item := range g.Items {
			active := !marked && navItemIsActive(item, activePath)
			marked = marked || active
			items = append(items, components.NavLink{
				Label:  item.Label,
				Href:   item.Href,
				Active: active,
			})
		}
		out = append(out, components.NavGroup{Title: g.Title, Items: items})
	}
	return out
}

// navGroupTable is the sidebar's fixed shape, in render order. The item
// set is build-time constant; only the hrefs and the active marking vary
// per request.
func navGroupTable(t navTargets) []navGroup {
	product := productPath(t.Product)
	delivery := product + "/delivery"

	// Tasks and Board own the milestone task subtree. With no milestone in
	// scope the chrome cannot build either page's href, so both link at
	// the product's delivery page -- the page listing the milestones whose
	// tasks and boards they are. Either way their active paths carry the
	// "*" wildcard, because the subtree is rooted at a milestone id that
	// varies per page.
	tasks, board := delivery, delivery
	if t.Milestone != uuid.Nil {
		milestone := product + "/milestones/" + t.Milestone.String()
		tasks, board = milestone+"/tasks", milestone+"/board"
	}

	return []navGroup{
		{Title: "", Items: []navItem{
			// The home page is the Overview. It matches the bare root
			// only, never a prefix, so no other area's page lights it.
			{Label: "Overview", Href: "/", Path: "/", Exact: true},
		}},
		{Title: "Work", Items: []navItem{
			{Label: "Needs attention", Href: opsPath, Path: opsPath},
			{Label: "Tasks", Href: tasks, Path: product + "/milestones/*/tasks"},
			{Label: "Board", Href: board, Path: product + "/milestones/*/board"},
		}},
		{Title: "Delivery", Items: []navItem{
			{Label: "Milestones", Href: delivery, Exact: true},
		}},
		{Title: "Design", Items: []navItem{
			{Label: "Design sessions", Href: designProductSessionsPath(t.Product)},
		}},
		{Title: "Spec", Items: []navItem{
			{Label: "Capabilities", Href: product, Exact: true},
			{Label: "Decisions", Href: product + "/decisions", Exact: true},
			{Label: "Personas", Href: product + "/personas", Exact: true},
			{Label: "Non-goals", Href: product + "/non-goals", Exact: true},
		}},
		{Title: "Admin", Items: []navItem{
			{Label: "Credentials", Href: credentialsPath, Exact: true},
		}},
	}
}
