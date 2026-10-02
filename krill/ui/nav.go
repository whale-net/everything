package main

import (
	"context"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
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

	// Badge is the count this item carries. Its zero value renders no
	// badge, so an item that has no figure to show needs no special case
	// -- and neither does a read that failed, which is the same absence
	// by design.
	Badge navBadge
}

// navGroup is one headed section of the sidebar: a title and its items.
// An empty Title renders its items unheaded, which is how Overview sits
// above the groups rather than inside one.
type navGroup struct {
	Title string
	Items []navItem
}

// navBadge is a nav item's count in the three states a read can leave it:
// a positive figure to render, a genuine zero, and a read that failed.
//
// The third state is why this is a type rather than an int. A zero badge
// tells an operator nothing needs attention; an unreadable count says the
// same thing for the wrong reason, so a red 0 is a false alarm and a
// silent 0 is worse than either. unreadableBadge is a distinct value that
// renders as no badge at all.
type navBadge struct {
	// count is the figure, meaningful only when readable.
	count int

	// readable is false when the read failed, leaving count undefined.
	readable bool
}

// countedBadge is a figure the read returned, zero included.
func countedBadge(count int) navBadge {
	return navBadge{count: count, readable: true}
}

// unreadableBadge is a count this request could not obtain. It is a
// package-level value so no caller can construct it wrongly.
var unreadableBadge = navBadge{}

// label is the badge's rendered text, or "" for no badge -- which covers
// both a zero count and an unreadable one.
func (b navBadge) label() string {
	if !b.readable || b.count <= 0 {
		return ""
	}
	return strconv.Itoa(b.count)
}

// needsAttentionBadge reads how many tasks are escalated in one product
// across all its milestones: the same figure the Overview Escalated tile
// and the unfiltered Escalated tab show, so the three cannot drift.
//
// The read is CountEscalatedTasks through the P0 console narrowing -- the
// store's dedicated count read, which returns a failed count as its error
// rather than as a 0 and does not cap at a page size. Only the product is
// narrowed; a milestone filter would scope the badge to one container and
// hide escalations elsewhere in the product.
//
// A failure is not a zero. It omits the badge and logs at WARNING: the
// page rendered fine without it, but the operator is looking at a
// sidebar that cannot tell them whether anything is stuck.
func (app *App) needsAttentionBadge(ctx context.Context, productID uuid.UUID) navBadge {
	if productID == uuid.Nil {
		// No product means no product-wide count to read; an empty scope
		// is not a deployment whose escalation queue needs watching.
		return countedBadge(0)
	}
	scopeID, err := app.soleScopeID(ctx)
	if err != nil {
		logger.Warn("needs-attention badge: could not resolve scope", "error", err)
		return unreadableBadge
	}
	count, err := app.tasks.CountEscalatedTasks(ctx, store.ListEscalatedTasksParams{
		ScopeID:       scopeID,
		ConsoleFilter: store.ConsoleFilter{ProductID: &productID},
	})
	if err != nil {
		logger.Warn("needs-attention badge: escalated count unreadable, omitting the badge",
			"product", productID, "error", err)
		return unreadableBadge
	}
	return countedBadge(count)
}

// navTargets carries the ids the sidebar's hrefs are built from, plus the
// one figure it shows. Product is the operator's current product;
// Milestone is the milestone a milestone-scoped page is showing, or
// uuid.Nil on a page that is not under one -- a milestone id the chrome
// cannot know is why the Tasks and Board items fall back to the delivery
// page.
type navTargets struct {
	Product   uuid.UUID
	Milestone uuid.UUID

	// Escalated is the Needs-attention item's badge. It is a value the
	// caller resolved per request, not something the sidebar reads: the
	// chrome renders, the route reads.
	Escalated navBadge
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
				Count:  item.Badge.label(),
			})
		}
		out = append(out, components.NavGroup{Title: g.Title, Items: items})
	}
	return out
}

// workspaceShellData is the one seam a route building a shell page goes
// through: it takes the ids the caller resolved and returns the chrome
// with every href already built from them.
//
// The product id is the caller's, never one this file resolves: a route
// that forgot to supply one would silently render a sidebar pointing at
// uuid.Nil's paths, which resolve to nothing, so passing it explicitly
// keeps that failure a compile error rather than a dead link.
func workspaceShellData(t navTargets, activePath, title, userLabel string) components.ShellData {
	return components.ShellData{
		LayoutData: components.LayoutData{
			Title:     title,
			UserLabel: userLabel,
			Nav:       navLinks(activePath),
		},
		NavGroups: workspaceNav(t, activePath),
	}
}

// shellNavTargets is the per-request half of the seam: it takes the ids a
// route already resolved and adds the one figure the chrome must read for
// itself, so a route building a shell page never has to remember to fetch
// it.
//
// It returns targets rather than rendering, because the cutover task owns
// the seam that mounts the chrome; this only fills the data that seam
// will render.
func (app *App) shellNavTargets(ctx context.Context, productID, milestoneID uuid.UUID) navTargets {
	return navTargets{
		Product:   productID,
		Milestone: milestoneID,
		Escalated: app.needsAttentionBadge(ctx, productID),
	}
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
			{Label: "Needs attention", Href: opsPath, Path: opsPath, Badge: t.Escalated},
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
