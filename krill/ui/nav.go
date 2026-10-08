// Workspace shell sidebar: an unheaded Overview link, six headed groups, and
// the rule for which item the current page belongs to. Hrefs vary per request
// because links carry the operator's current product.
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

type navItem struct {
	Label string

	// Href must resolve on this binary; never point at a route that would 404.
	Href string

	// Path is the route prefix that marks this item active when it differs from
	// Href. A "*" segment matches any one segment, so an item can own a subtree
	// rooted at an id the chrome does not hold.
	Path string

	// AltPath is a second path this item owns, for a page with two URLs or a
	// legacy wildcard subtree.
	AltPath string

	// Exact limits activity to the path itself; the Spec tabs are siblings under
	// the product path and would otherwise all stay lit.
	Exact bool

	// Badge is the item's count; the zero value renders no badge.
	Badge navBadge
}

// navGroup is one sidebar section. An empty Title renders its items unheaded.
type navGroup struct {
	Title string
	Items []navItem
}

// navBadge is a nav count that distinguishes a failed read from zero, so an
// unreadable count renders no badge rather than a misleading 0.
type navBadge struct {
	count int

	readable bool
}

func countedBadge(count int) navBadge {
	return navBadge{count: count, readable: true}
}

// unreadableBadge is a count this request could not obtain.
var unreadableBadge = navBadge{}

// label is the badge text, or "" for zero or unreadable.
func (b navBadge) label() string {
	if !b.readable || b.count <= 0 {
		return ""
	}
	return strconv.Itoa(b.count)
}

// needsAttentionBadge counts escalated tasks across the whole product, the same
// figure the Overview tile and Escalated tab show. A failed read logs a warning
// and omits the badge rather than showing 0.
func (app *App) needsAttentionBadge(ctx context.Context, productID uuid.UUID) navBadge {
	if productID == uuid.Nil {
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

// navTargets carries the ids sidebar hrefs are built from, plus the badge.
// Every href depends only on the product.
type navTargets struct {
	Product uuid.UUID

	// Escalated is resolved by the route per request; the sidebar only renders it.
	Escalated navBadge
}

func (i navItem) itemPath() string {
	if i.Path != "" {
		return i.Path
	}
	return i.Href
}

func (i navItem) itemPaths() []string {
	if i.AltPath == "" {
		return []string{i.itemPath()}
	}
	return []string{i.itemPath(), i.AltPath}
}

// navItemIsActive reports whether the current page belongs to item, matching
// at path-segment boundaries so "/ops" owns "/ops/claimed" but not "/opsarchive".
func navItemIsActive(item navItem, activePath string) bool {
	for _, pattern := range item.itemPaths() {
		if pathOwns(pattern, activePath, item.Exact) {
			return true
		}
	}
	return false
}

// pathOwns reports whether activePath is pattern or sits under it; an exact
// pattern owns only itself.
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

// pathSegments splits a path into non-empty segments. The root has none, which
// keeps Overview from matching every page as a prefix.
func pathSegments(path string) []string {
	path = strings.Trim(path, "/")
	if path == "" {
		return nil
	}
	return strings.Split(path, "/")
}

// workspaceNav builds the grouped nav, marking at most one item active: the
// first match in render order.
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

// workspaceShellData builds the shell chrome from caller-resolved ids. It stays
// pure: the switcher is passed in so a route cannot add a second scope read.
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

// escalationBadgeKey carries an escalated count already read this request, so
// a page showing the same figure and the sidebar badge share one read.
type escalationBadgeKey struct{}

func withEscalationBadge(r *http.Request, badge navBadge) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), escalationBadgeKey{}, badge))
}

func escalationBadgeFrom(ctx context.Context) (navBadge, bool) {
	badge, ok := ctx.Value(escalationBadgeKey{}).(navBadge)
	return badge, ok
}

// shellNavTargets adds the escalation badge to a route-resolved product,
// reusing a count already on ctx.
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

// navGroupTable is the sidebar's fixed shape, in render order.
func navGroupTable(t navTargets) []navGroup {
	product := productPath(t.Product)
	overview := productHref(t.Product, overviewSuffix)
	// AltPath keeps legacy /delivery bookmarks marking Milestones active.
	milestones := productHref(t.Product, milestonesSuffix)

	// Tasks and Board are product-wide. AltPath keeps legacy per-milestone URLs
	// marked correctly; the wildcard owns a subtree rooted at an unknown id.
	tasks := productHref(t.Product, tasksSuffix)
	board := productHref(t.Product, boardSuffix)

	// AltPath keeps the legacy /ops root marking Needs attention active.
	needsAttention := productHref(t.Product, needsAttentionSuffix)

	return []navGroup{
		{Title: "", Items: []navItem{
			// Overview owns both "/" and the product overview, exactly, so no other
			// area's page lights it.
			{Label: "Overview", Href: overview, Path: overview, AltPath: "/", Exact: true},
		}},
		{Title: "Work", Items: []navItem{
			{Label: "Needs attention", Href: needsAttention, Path: needsAttention, AltPath: opsPath, Badge: t.Escalated},
			{Label: "Tasks", Href: tasks, Path: tasks, AltPath: product + "/milestones/*/tasks"},
			{Label: "Board", Href: board, Path: board, AltPath: product + "/milestones/*/board"},
		}},
		{Title: "Delivery", Items: []navItem{
			{Label: "Milestones", Href: milestones, Path: milestones, AltPath: product + "/delivery"},
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
