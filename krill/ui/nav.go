// The workspace shell's nav: the sidebar every signed-in page renders
// (design/wireframes/_shell.html) as one unheaded Overview link followed by
// six headed groups, and the rule for deciding which one the page being
// rendered belongs to.
//
// The item set is build-time constant; only the hrefs, the badges, and
// the active marking vary per request, because a link has to carry the
// operator's current product.
package main

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/components"
)

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

	// AltPath is a second path this item also owns, for a page that has
	// two URLs. Overview is one: "/" and the product's own overview both
	// render it, and the operator must see where they are on either.
	AltPath string

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
// one figure it shows. Product is the operator's current product.
//
// There is no milestone id here any more. It existed only so Tasks and
// Board could build per-milestone hrefs, and while neither page needs one
// it was a field the chrome could only sometimes fill: a page reached
// without a container in its URL had no milestone to pass, and the two
// items fell back to the delivery page for it. Now that both views are
// product-wide, every href is a function of the product alone, so a
// request cannot produce a sidebar whose links disagree with the page it
// is on.
type navTargets struct {
	Product uuid.UUID

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

// itemPaths is every path an item owns: its own, plus the alternative a
// two-URL item carries.
func (i navItem) itemPaths() []string {
	if i.AltPath == "" {
		return []string{i.itemPath()}
	}
	return []string{i.itemPath(), i.AltPath}
}

// navItemIsActive reports whether the page being rendered belongs to this
// item. Matched at path-segment boundaries rather than by raw prefix: an
// item's path is a prefix of its sub-pages ("/ops" of "/ops/claimed"), but
// not of an unrelated sibling that merely starts with the same characters
// ("/opsarchive"). A "*" segment matches exactly one segment.
func navItemIsActive(item navItem, activePath string) bool {
	for _, pattern := range item.itemPaths() {
		if pathOwns(pattern, activePath, item.Exact) {
			return true
		}
	}
	return false
}

// pathOwns reports whether the page at activePath is the pattern itself or
// sits under it. An Exact pattern owns its path alone; the Spec group's
// tabs are siblings, not a chain, and prefix matching would leave
// Capabilities lit while an operator reads Decisions.
func pathOwns(pattern, activePath string, exact bool) bool {
	patternSegments := pathSegments(pattern)
	segments := pathSegments(activePath)
	if len(patternSegments) > len(segments) {
		return false
	}
	for i, seg := range patternSegments {
		if seg != "*" && seg != segments[i] {
			return false
		}
	}
	return len(segments) == len(patternSegments) || !exact
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
// first and matches its own two URLs alone, so it is never active while
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
//
// switcher is the sidebar's Product select, which this function does not
// build: it reads the scope, and a seam that stayed pure is one a route
// cannot accidentally turn into a second, differently-filtered read.
func workspaceShellData(t navTargets, activePath, title, userLabel string, switcher *components.ProductSwitcherData) components.ShellData {
	return components.ShellData{
		LayoutData: components.LayoutData{
			Title:     title,
			UserLabel: userLabel,
		},
		NavGroups: workspaceNav(t, activePath),
		Switcher:  switcher,
	}
}

// escalationBadgeKey carries an escalated count this request already read,
// so a page whose own body shows the same figure -- the Overview's primary
// action -- and the sidebar's Needs-attention badge are one read rather
// than two that could disagree.
type escalationBadgeKey struct{}

// withEscalationBadge returns a request carrying an already-read badge.
func withEscalationBadge(r *http.Request, badge navBadge) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), escalationBadgeKey{}, badge))
}

// escalationBadgeFrom returns the badge this request already read, if any.
func escalationBadgeFrom(ctx context.Context) (navBadge, bool) {
	badge, ok := ctx.Value(escalationBadgeKey{}).(navBadge)
	return badge, ok
}

// shellNavTargets is the per-request half of the seam: it takes the product
// a route already resolved and adds the one figure the chrome must read for
// itself, so a route building a shell page never has to remember to fetch
// it.
func (app *App) shellNavTargets(ctx context.Context, productID uuid.UUID) navTargets {
	badge, ok := escalationBadgeFrom(ctx)
	if !ok {
		badge = app.needsAttentionBadge(ctx, productID)
	}
	return navTargets{
		Product:   productID,
		Escalated: badge,
	}
}

// navGroupTable is the sidebar's fixed shape, in render order. The item
// set is build-time constant; only the hrefs and the active marking vary
// per request.
func navGroupTable(t navTargets) []navGroup {
	product := productPath(t.Product)
	delivery := product + "/delivery"
	overview := productHref(t.Product, overviewSuffix)

	// Tasks and Board are the two product-wide views of one scope, so both
	// hrefs are the product's own pages and neither needs a milestone id
	// from the chrome. They were the delivery page and the in-flight
	// milestone's board only while the product-wide routes did not exist;
	// linking an operator to a page listing the milestones rather than to
	// the tasks they came to see is a fallback that has nothing left to
	// fall back from.
	//
	// Each still OWNS the pre-redesign per-milestone subtree (its
	// AltPath), because that URL is a task view however it is reached: an
	// operator who followed a bookmarked /milestones/{mid}/tasks link is
	// on a Tasks page and must see the sidebar say so. The wildcard is
	// what lets one item own a subtree rooted at an id the chrome does
	// not hold.
	//
	// The href does not follow: it is the product-wide page either way.
	// If those URLs are later cut over to redirect (FR f41a352d's legacy
	// rule), the redirect's destination IS the product-wide page, which
	// Path already owns -- so this AltPath goes quietly inert rather than
	// stale, and an operator arriving by the redirect still lands on a
	// page whose sidebar marks Tasks.
	tasks := productHref(t.Product, tasksSuffix)
	board := productHref(t.Product, boardSuffix)

	return []navGroup{
		{Title: "", Items: []navItem{
			// The home page is the Overview, and so is the product's own
			// overview URL. Both light this item, neither as a prefix, so
			// no other area's page does.
			{Label: "Overview", Href: overview, Path: overview, AltPath: "/", Exact: true},
		}},
		{Title: "Work", Items: []navItem{
			{Label: "Needs attention", Href: opsPath, Path: opsPath, Badge: t.Escalated},
			{Label: "Tasks", Href: tasks, Path: tasks, AltPath: product + "/milestones/*/tasks"},
			{Label: "Board", Href: board, Path: board, AltPath: product + "/milestones/*/board"},
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
