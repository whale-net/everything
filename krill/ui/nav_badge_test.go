package main

// Coverage for the Needs-attention nav item's escalated-count badge (FR
// 0879bc30): the figure it shows, the two figures it is required to agree
// with, and the two absences -- nothing escalated, nothing readable --
// which are both rendered as no badge rather than as a 0.
//
// The chrome is exercised through the seam a shell page uses
// (shellNavTargets -> workspaceShellData -> components.Shell) and rendered
// through this package's own fragment seam, so a test can pin the badge
// against one specific store fixture rather than whatever a mux resolves.

import (
	"bytes"
	"context"
	"errors"
	"html"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/a-h/templ"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/components"
)

// badgeRow is one escalated task in the fixture below. It is expressed as a
// row rather than as a fixed figure because the badge's narrowing is what
// decides which rows it counts: a read that dropped the product filter, or
// added a milestone one, has to answer a different number.
type badgeRow struct {
	id        uuid.UUID
	product   uuid.UUID
	milestone uuid.UUID
}

// badgeScope is the deployment's sole scope, the one the badge's read is
// resolved under. Its own id rather than a shared constant, so this file's
// fixture is independent of the ops console's test harness.
var badgeScope = store.Scope{ID: uuid.MustParse("11111111-2222-3333-4444-555555555555")}

// fakeBadgeScopes is the scope resolution a working deployment answers.
// It embeds the interface and implements GetSole, the only method the
// badge's read calls.
type fakeBadgeScopes struct {
	store.ScopeStore
}

func (fakeBadgeScopes) GetSole(context.Context) (store.Scope, error) { return badgeScope, nil }

// badgeFixtureTasks is the store all three figures are read through. Every
// read derives its answer from one predicate over one row set, and
// ListEscalatedTasks honours PageParams, so a consumer that counted a page
// rather than the queue is caught rather than flattered.
type badgeFixtureTasks struct {
	store.TaskStore
	scopeID uuid.UUID
	rows    []badgeRow

	// countErr stands in for a store outage: the read the badge itself
	// makes, not the list read beside it.
	countErr error

	gotCount []store.ListEscalatedTasksParams
}

// matching applies one escalated-queue narrowing to the fixture's rows.
func (f *badgeFixtureTasks) matching(p store.ListEscalatedTasksParams) []badgeRow {
	if p.ScopeID != f.scopeID {
		return nil
	}
	var out []badgeRow
	for _, row := range f.rows {
		if p.ProductID != nil && *p.ProductID != row.product {
			continue
		}
		if p.MilestoneID != nil && *p.MilestoneID != row.milestone {
			continue
		}
		out = append(out, row)
	}
	return out
}

func (f *badgeFixtureTasks) CountEscalatedTasks(_ context.Context, p store.ListEscalatedTasksParams) (int, error) {
	f.gotCount = append(f.gotCount, p)
	if f.countErr != nil {
		return 0, f.countErr
	}
	return len(f.matching(p)), nil
}

func (f *badgeFixtureTasks) ListEscalatedTasks(_ context.Context, p store.ListEscalatedTasksParams) (store.Page[store.EscalatedTaskRow], error) {
	rows := f.matching(p)
	size := store.ResolvePageSize(p.Page.PageSize)
	page := store.Page[store.EscalatedTaskRow]{}
	for _, row := range rows {
		if len(page.Items) >= size {
			page.NextToken = "more"
			break
		}
		page.Items = append(page.Items, store.EscalatedTaskRow{TaskID: row.id})
	}
	return page, nil
}

// CountConsoleOverview fills only the escalated figure and its sub-line.
// The other three tiles are zero here, which says nothing about the badge:
// no Overview page has shipped to render them, and this test is about the
// escalated figure agreeing with two other reads of the same rows.
func (f *badgeFixtureTasks) CountConsoleOverview(_ context.Context, p store.ConsoleOverviewParams) (store.ConsoleOverviewCounts, error) {
	escalated := len(f.matching(p.Escalated))
	return store.ConsoleOverviewCounts{Escalated: escalated, EscalatedRecently: escalated}, nil
}

// badgeFixture is one product's escalation queue plus another product's, so
// the product narrowing is load-bearing rather than decorative.
type badgeFixture struct {
	product          uuid.UUID
	otherProduct     uuid.UUID
	emptyProduct     uuid.UUID
	tasks            *badgeFixtureTasks
	productEscalated int
	otherEscalated   int
}

// wantProductEscalated and wantOtherEscalated are the fixture's figures as
// literals, so a test that derived its expectation from the same rows it
// reads would not be satisfied by a wrong count.
const (
	wantProductEscalated = store.DefaultConsolePageSize + 6
	wantOtherEscalated   = 7
)

func newBadgeFixture() badgeFixture {
	firstMilestone, secondMilestone := uuid.New(), uuid.New()
	f := badgeFixture{
		product:      uuid.New(),
		otherProduct: uuid.New(),
		emptyProduct: uuid.New(),
		tasks:        &badgeFixtureTasks{scopeID: badgeScope.ID},
	}

	// More escalated tasks than one default page holds, and spread over two
	// milestones: a badge counted from a page of the list answers 25 where
	// the queue holds 31, and one narrowed to a milestone answers 30.
	for range wantProductEscalated - 1 {
		f.tasks.rows = append(f.tasks.rows, badgeRow{
			id: uuid.New(), product: f.product, milestone: firstMilestone,
		})
	}
	f.tasks.rows = append(f.tasks.rows, badgeRow{
		id: uuid.New(), product: f.product, milestone: secondMilestone,
	})
	for range wantOtherEscalated {
		f.tasks.rows = append(f.tasks.rows, badgeRow{
			id: uuid.New(), product: f.otherProduct, milestone: uuid.New(),
		})
	}

	f.productEscalated = wantProductEscalated
	f.otherEscalated = wantOtherEscalated
	return f
}

// badgeApp is the app the badge reads through: the deployment's sole scope
// plus the fixture store, with nothing else the seam touches.
func (f badgeFixture) badgeApp() *App {
	// spec answers the scope read the sidebar's Product switcher makes;
	// an empty one leaves the switcher off, which is a state the chrome
	// renders and these tests are not about.
	return &App{scopes: fakeBadgeScopes{}, tasks: f.tasks, spec: emptyScopeSpecReader{}}
}

// escalatingParams is the P0 narrowing the badge, the Overview tile and the
// Escalated tab all share: the sole scope, the one product, and no
// milestone.
func escalatingParams(productID uuid.UUID) store.ListEscalatedTasksParams {
	return store.ListEscalatedTasksParams{
		ScopeID:       badgeScope.ID,
		ConsoleFilter: store.ConsoleFilter{ProductID: &productID},
	}
}

// TestNeedsAttentionBadgeEqualsTheOverviewTileAndTheEscalatedTab is the
// agreement half of the requirement: the badge, the Overview Escalated tile
// and the unfiltered Escalated tab count are three reads of one fixture, so
// a narrowing that moved one of them away from the others fails here.
//
// The figure is above one default page on purpose. A badge built from a
// page length would answer 25 and quietly cap the badge on every queue
// larger than a page -- which is why the badge reads the queue's count
// rather than a page of its rows.
func TestNeedsAttentionBadgeEqualsTheOverviewTileAndTheEscalatedTab(t *testing.T) {
	f := newBadgeFixture()
	app := f.badgeApp()
	ctx := context.Background()

	badge := app.needsAttentionBadge(ctx, f.product)

	// The narrowing itself, asserted rather than inferred from the numbers:
	// the badge reads the deployment's sole scope, this product, and no
	// milestone -- a milestone filter would scope the badge to one
	// container and hide escalations elsewhere in the product. Taken before
	// this test makes its own reads, so it is the badge's params alone.
	require.Len(t, f.tasks.gotCount, 1, "one read per request")
	assert.Equal(t, escalatingParams(f.product), f.tasks.gotCount[0], "the badge's own read params")

	tile, err := app.tasks.CountConsoleOverview(ctx, store.ConsoleOverviewParams{
		Escalated: escalatingParams(f.product),
	})
	require.NoError(t, err)
	tab, err := app.tasks.CountEscalatedTasks(ctx, escalatingParams(f.product))
	require.NoError(t, err)

	assert.Equal(t, f.productEscalated, badge.count, "badge")
	assert.Equal(t, badge.count, tile.Escalated, "badge vs the Overview Escalated tile")
	assert.Equal(t, badge.count, tab, "badge vs the unfiltered Escalated tab count")

	// Why the count read and not the list: one page of this queue is
	// shorter than the queue, so a page-derived badge would have answered
	// the default page size above.
	page, err := app.tasks.ListEscalatedTasks(ctx, escalatingParams(f.product))
	require.NoError(t, err)
	assert.Len(t, page.Items, store.DefaultConsolePageSize, "one page of the queue")
	assert.Less(t, len(page.Items), badge.count, "a page length would cap the badge")

	// A different product's escalations are a different number, so the
	// agreement above is not satisfied by a read that ignored the product.
	other := app.needsAttentionBadge(ctx, f.otherProduct)
	assert.Equal(t, f.otherEscalated, other.count, "another product's badge")
	assert.NotEqual(t, badge.count, other.count)

	// A product with nothing escalated reads 0, which renders as no badge.
	assert.Equal(t, 0, app.needsAttentionBadge(ctx, f.emptyProduct).count, "an idle product")
	assert.True(t, app.needsAttentionBadge(ctx, f.emptyProduct).readable, "an idle product is readable")
}

// TestNeedsAttentionBadgeRendersNoBadgeElementWithoutACount is the absence
// half: a genuine zero and an unreadable count both render no badge element
// at all, and neither ever renders the character 0.
//
// The zero and the unreadable read are the same rendered absence on
// purpose -- the FR asks for it -- but they are not the same value, so the
// test asserts the underlying tri-state separately: an unreadable read
// leaves the count undefined, while a zero is a figure that was read.
func TestNeedsAttentionBadgeRendersNoBadgeElementWithoutACount(t *testing.T) {
	t.Run("nothing is escalated", func(t *testing.T) {
		f := newBadgeFixture()
		app := f.badgeApp()

		badge := app.needsAttentionBadge(context.Background(), f.emptyProduct)
		assert.Equal(t, countedBadge(0), badge, "a genuine zero is a figure that was read")

		body := renderWorkspaceShell(t, app, f.emptyProduct)
		assertNoBadge(t, body)
	})

	t.Run("the count could not be read", func(t *testing.T) {
		f := newBadgeFixture()
		f.tasks.countErr = errors.New("connection refused")
		app := f.badgeApp()

		badge := app.needsAttentionBadge(context.Background(), f.product)
		// The tri-state, not just the markup: an unreadable read must not
		// have been folded into a zero the sidebar could render.
		assert.Equal(t, unreadableBadge, badge)

		assertNoBadge(t, renderWorkspaceShell(t, app, f.product))
	})
}

// assertNoBadge is the shared assertion of the two absence cases: no badge
// element is in the rendered sidebar, and the sidebar itself is otherwise
// there. An absent badge is an omission, not a page that failed to build
// its chrome.
func assertNoBadge(t *testing.T, body string) {
	t.Helper()
	assert.NotContains(t, body, navCountAttr, "no badge element at all")
	assert.Empty(t, navCountBadge(body), "no badge figure at all")
	assert.NotContains(t, body, ">0<", "a zero is never rendered as a badge")
	assert.Contains(t, body, `data-krill="workspace-shell"`, "the chrome still rendered")
	assert.Contains(t, body, "Needs attention", "the item still rendered")
}

// TestNeedsAttentionBadgeOmitsTheBadgeWhenTheReadFails drives the whole seam
// with a failing read client: the chrome still renders at 200 with no badge
// element, and the failure is logged at WARNING -- the system adjusted and
// served the page, so it completed, but not exactly as expected.
func TestNeedsAttentionBadgeOmitsTheBadgeWhenTheReadFails(t *testing.T) {
	f := newBadgeFixture()
	f.tasks.countErr = errors.New("connection refused")
	app := f.badgeApp()

	logs := captureWarnings(t)
	rec := renderWorkspaceShellTo(app, f.product)

	assert.Equal(t, http.StatusOK, rec.Code, "a failed badge read must not fail the page")

	body := rec.Body.String()
	assert.NotContains(t, body, `data-krill="nav-count"`, "an unreadable count renders no badge")
	assert.NotContains(t, body, "badge-error", "no red 0 and no badge at all")
	// The chrome around it is unaffected: the sidebar, its groups, and the
	// Needs-attention link itself all rendered.
	assert.Contains(t, body, `data-krill="workspace-shell"`)
	assert.Contains(t, body, `data-krill="primary-nav"`)
	assert.Contains(t, body, "Needs attention")
	assert.Contains(t, body, "Tasks")

	logged := logs.String()
	assert.Contains(t, logged, "level=WARN", "the failed read is logged at WARNING: %s", logged)
	assert.Contains(t, logged, "needs-attention badge", "the log names the badge: %s", logged)
	assert.NotContains(t, logged, "level=ERROR", "the page still rendered: %s", logged)
}

// TestNeedsAttentionBadgeRendersTheCountBesideTheLabel is the positive
// render case: the figure appears on the Needs-attention item, and on no
// other item, so a badge cannot wander onto the wrong link.
func TestNeedsAttentionBadgeRendersTheCountBesideTheLabel(t *testing.T) {
	f := newBadgeFixture()
	app := f.badgeApp()
	product := f.product

	body := renderWorkspaceShell(t, app, product)

	assert.Equal(t, strconv.Itoa(f.productEscalated), navCountBadge(body))
	assert.Equal(t, 1, strings.Count(body, `data-krill="nav-count"`), "only the Needs-attention item carries a badge")
	assert.Contains(t, body, `class="badge badge-error badge-sm"`, "the wireframe's badge classes")

	// The count sits inside the Needs-attention anchor rather than beside
	// it, so the label and the figure are one target to activate.
	item := navItemByLabel(t, workspaceNav(app.shellNavTargets(context.Background(), product, uuid.Nil), opsPath), "Needs attention")
	assert.Equal(t, strconv.Itoa(f.productEscalated), item.Count)
}

// failingBadgeScopes is the other way the badge's read cannot complete:
// the deployment's scope itself is unresolvable.
type failingBadgeScopes struct {
	store.ScopeStore
}

func (failingBadgeScopes) GetSole(context.Context) (store.Scope, error) {
	return store.Scope{}, errors.New("scope lookup unavailable")
}

// TestNeedsAttentionBadgeOmitsTheBadgeWhenTheScopeIsUnresolvable covers the
// read's one other failure mode, and holds it to the same contract as the
// store outage: no badge, page renders, WARNING.
func TestNeedsAttentionBadgeOmitsTheBadgeWhenTheScopeIsUnresolvable(t *testing.T) {
	f := newBadgeFixture()
	app := f.badgeApp()
	app.scopes = failingBadgeScopes{}

	logs := captureWarnings(t)
	rec := renderWorkspaceShellTo(app, f.product)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), `data-krill="nav-count"`)
	assert.Contains(t, logs.String(), "level=WARN", "an unresolvable scope is logged at WARNING")
	assert.Empty(t, f.tasks.gotCount, "an unresolvable scope must not reach the count read")
}

// TestNeedsAttentionBadgeNeedsNoProduct asserts the one case where the
// badge is a readable zero rather than an unreadable figure: with no product
// in scope there is no product-wide queue to count, and an empty scope is
// not a deployment whose escalation queue needs watching.
func TestNeedsAttentionBadgeNeedsNoProduct(t *testing.T) {
	f := newBadgeFixture()
	app := f.badgeApp()

	badge := app.needsAttentionBadge(context.Background(), uuid.Nil)

	assert.Equal(t, countedBadge(0), badge, "no product is a readable zero")
	assert.Empty(t, f.tasks.gotCount, "no product means no count read")
	assertNoBadge(t, renderWorkspaceShell(t, app, uuid.Nil))
}

// ── the seam, and the chrome it renders ─────────────────────────────────────

// workspaceShellPage is what a shell route serves once the cutover task
// mounts the chrome: the per-request nav data composed with the shell.
// Nothing registers it on a mux here.
func workspaceShellPage(app *App, productID uuid.UUID, activePath string) templ.Component {
	data := workspaceShellData(
		app.shellNavTargets(context.Background(), productID, uuid.Nil),
		activePath, "Overview", "developer",
		app.productSwitcherData(httptest.NewRequest(http.MethodGet, "/products/"+productID.String()+"/overview", nil)),
	)
	return components.Shell(data)
}

// renderWorkspaceShellTo renders that page through this package's own
// fragment seam and returns the recorder, so a test can assert the status
// as well as the markup.
func renderWorkspaceShellTo(app *App, productID uuid.UUID) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	renderFragment(rec, httptest.NewRequest(http.MethodGet, "/", nil),
		workspaceShellPage(app, productID, opsPath))
	return rec
}

func renderWorkspaceShell(t *testing.T, app *App, productID uuid.UUID) string {
	t.Helper()
	return renderWorkspaceShellTo(app, productID).Body.String()
}

// navCountAttr is the hook the badge renders under.
const navCountAttr = `data-krill="nav-count"`

// navCountBadge returns the figure the sidebar rendered, or "" when it
// rendered no badge element at all -- the distinction the FR turns on, and
// the reason the scan looks for the element rather than for a number.
func navCountBadge(body string) string {
	i := strings.Index(body, navCountAttr)
	if i < 0 {
		return ""
	}
	rest := strings.TrimPrefix(body[i+len(navCountAttr):], ">")
	end := strings.Index(rest, "</span>")
	if end < 0 {
		return ""
	}
	return html.UnescapeString(strings.TrimSpace(rest[:end]))
}

// captureWarnings redirects this package's logger into a buffer for the
// duration of the test and returns it, so a test can assert on what an
// operator's log would have said.
func captureWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := logger
	logger = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	t.Cleanup(func() { logger = prev })
	return &buf
}
