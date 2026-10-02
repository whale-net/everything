// The Milestones-in-flight panel's acceptance, one row at a time
// (FR 8ef82053) -- plus the honesty of the shared test stores the panel's
// read runs through on every full-page render.
//
// overview_page_test.go covers the panel's classification, its two count
// figures, and its three sentence-or-bar branches. What it asserts is
// present, not what each row is *made of*: a row could render its href from
// one element, its name from another, and no badge at all, and every
// assertion there would still hold. The tests below read one rendered row
// and check the three things the FR puts in it -- the name as the link to
// Milestone detail, the status as a badge, and the progress -- so a row
// that lost or moved one of them fails.
//
// The store fakes matter here for the same reason. The panel's read runs
// through five shared fakes that each answer it with no containers, so a
// panel that rendered its empty state without consulting the read would
// pass in five other targets' tests and here. The last test in this file
// pins each of them as reached.
package main

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
)

// ---------------------------------------------------------------------------
// one rendered row at a time
// ---------------------------------------------------------------------------

// panelRows splits a rendered panel into one string per row, so an
// assertion is about a row rather than about the panel as a whole. Without
// it a panel rendering three identical figures passes whichever row lost
// its own.
func panelRows(t *testing.T, panel string) []string {
	t.Helper()
	const marker = `<li data-krill="overview-in-flight-row">`
	parts := strings.Split(panel, marker)
	if len(parts) < 2 {
		t.Fatalf("panel rendered no rows:\n%s", collapsed(panel))
	}
	rows := parts[1:]
	// Cut each row at its own close so a row cannot be satisfied by text
	// belonging to the row after it.
	for i, row := range rows {
		if end := strings.Index(row, "</li>"); end >= 0 {
			rows[i] = row[:end]
		}
	}
	return rows
}

// rowNamed returns the one rendered row carrying name, failing when there
// is not exactly one -- a duplicate is as wrong as a miss, and a search
// that took the first would hide it.
func rowNamed(t *testing.T, panel, name string) string {
	t.Helper()
	var found []string
	for _, row := range panelRows(t, panel) {
		if strings.Contains(collapsed(row), name) {
			found = append(found, row)
		}
	}
	switch len(found) {
	case 1:
		return found[0]
	case 0:
		t.Fatalf("no panel row names %q:\n%s", name, collapsed(panel))
	default:
		t.Fatalf("%d panel rows name %q, want 1:\n%s", len(found), name, collapsed(panel))
	}
	return ""
}

// anchorTextRe captures the visible text of a link carrying href, so a test
// can tell a row whose *name* is the link from a row that merely contains
// that href somewhere.
var anchorTextRe = regexp.MustCompile(`<a href="([^"]*)"[^>]*>(.*?)</a>`)

// badgeTextRe captures the label of a badge span. htmxui.Badge renders
// <span class="badge ...">label</span> with no hook of its own, so the class
// is what identifies one.
var badgeTextRe = regexp.MustCompile(`<span class="badge[^"]*"[^>]*>([^<]*)</span>`)

// TestInFlightPanelRowLinksItsNameToMilestoneDetail is the first of a
// row's three parts: the name is itself the link, and the link is the
// container's Milestone detail page under this product. The existing href
// assertion proves the URL is somewhere on the page; this proves it is
// wrapped around the name an operator reads, so a row whose anchor lost its
// text -- rendering the name as a bare label beside an empty link -- fails.
func TestInFlightPanelRowLinksItsNameToMilestoneDetail(t *testing.T) {
	mux := overviewMux(t, slice.DeliveryListing{}, nil, withProgress(panelContainers()...))

	for _, path := range overviewPaths() {
		panel := panelRegion(t, fetch(t, mux, path).Body.String())

		for _, tc := range []struct{ name, id string }{
			{"Executing milestone", panelMilestone},
			{"Designing milestone", panelDesign},
			// A milepebble's row links to the cut's own page, not its
			// parent's: milestone_ref holds both, so the same path serves
			// it and the id is the milepebble's own.
			{"Executing milepebble", panelMilepebble},
		} {
			row := rowNamed(t, panel, tc.name)
			want := "/products/" + overviewProduct.String() + "/milestones/" + tc.id

			var nameIsLink bool
			for _, m := range anchorTextRe.FindAllStringSubmatch(row, -1) {
				if strings.TrimSpace(m[2]) != tc.name {
					continue
				}
				if m[1] != want {
					t.Errorf("GET %s: row %q links to %q, want the Milestone detail %q", path, tc.name, m[1], want)
				}
				nameIsLink = true
			}
			if !nameIsLink {
				t.Errorf("GET %s: the name %q is not the link; row was:\n%s", path, tc.name, collapsed(row))
			}
		}
	}
}

// TestInFlightPanelRowBadgesItsStatus is the second part, and the one no
// other assertion in this package covers: that the row carries a status
// badge at all, and that the badge reads the container's own status.
//
// The badge is rendered through the same statusBadge the delivery page
// uses, so the class is pinned too -- a row that rendered the status as
// plain text, or hand-rolled a span, would satisfy "the status is on the
// row" while losing the one thing a badge is for.
func TestInFlightPanelRowBadgesItsStatus(t *testing.T) {
	mux := overviewMux(t, slice.DeliveryListing{}, nil, withProgress(panelContainers()...))

	for _, path := range overviewPaths() {
		panel := panelRegion(t, fetch(t, mux, path).Body.String())

		for _, tc := range []struct {
			name, status, badgeClass string
		}{
			// components.MilestoneStatusStyle: in progress is the warning
			// (work happening now), in design the soft info tint (a spec
			// being written). Both are the shell's shared mapping.
			{"Executing milestone", "in progress", "badge-warning"},
			{"Designing milestone", "in design", "badge-info"},
			// The milepebble's own status, not its parent's: this row's
			// parent is designed, so a badge reading the parent would say
			// "designed" on a container that is in flight.
			{"Executing milepebble", "in progress", "badge-warning"},
		} {
			row := rowNamed(t, panel, tc.name)

			var badged bool
			for _, label := range badgeTextRe.FindAllStringSubmatch(row, -1) {
				if strings.TrimSpace(label[1]) != tc.status {
					continue
				}
				badged = true
			}
			if !badged {
				t.Errorf("GET %s: row %q carries no badge reading %q:\n%s", path, tc.name, tc.status, collapsed(row))
			}
			if !strings.Contains(row, tc.badgeClass) {
				t.Errorf("GET %s: row %q does not use the shared %s badge class:\n%s", path, tc.name, tc.badgeClass, collapsed(row))
			}
		}
	}
}

// TestInFlightPanelProgressCountsTasksBeyondDone pins the third part
// against the one figure a re-derivation gets wrong: a container whose
// tasks are all still open.
//
// The panel asks the read for Done() and Total(), so this row reads
// "0 of 3". A view that derived either number from the Done lane, or that
// treated a zero Done as a container with no tasks, reads "0 of 0" or "No
// tasks yet" -- and both would be true statements about the board that an
// operator cannot act on, since the container plainly holds three tasks
// waiting to be done.
func TestInFlightPanelProgressCountsTasksBeyondDone(t *testing.T) {
	row := milestoneProgress(uuid.MustParse(panelMilestone), "Open milestone",
		store.MilestoneStatusInProgress, store.TaskLaneCounts{Implementation: 2, Testing: 1})

	mux := overviewMux(t, slice.DeliveryListing{}, nil, withProgress(row))
	panel := panelRegion(t, fetch(t, mux, "/products/"+overviewProduct.String()+"/overview").Body.String())

	markup := rowNamed(t, panel, "Open milestone")
	if want := "0 of 3 tasks done"; !strings.Contains(collapsed(markup), want) {
		t.Errorf("a container with nothing done reads %q, want %q:\n%s", collapsed(markup), want, collapsed(markup))
	}
	tag := elementTag(markup, "overview-in-flight-progress")
	if !strings.Contains(tag, `value="0"`) || !strings.Contains(tag, `max="3"`) {
		t.Errorf("progress bar tag %q does not carry value=0 max=3", tag)
	}
	if strings.Contains(markup, "No tasks yet") {
		t.Errorf("a container whose tasks are all still open says it has none:\n%s", collapsed(markup))
	}
}

// TestInFlightPanelRendersInDesignOverItsProgress settles the one overlap
// between the two sentence branches. An in-design container is the one
// status that can hold tasks without them meaning anything -- a cut can be
// opened before its spec is signed off -- and the FR says the signoff
// sentence wins, because "0 of 2 tasks done" beside an empty bar tells an
// operator the work started and stalled when the spec is simply not
// written. Pinned on a fixture with tasks so the precedence is decided
// rather than incidental.
func TestInFlightPanelRendersInDesignOverItsProgress(t *testing.T) {
	row := milestoneProgress(uuid.MustParse(panelDesign), "Open spec",
		store.MilestoneStatusInDesign, store.TaskLaneCounts{Implementation: 2})

	mux := overviewMux(t, slice.DeliveryListing{}, nil, withProgress(row))
	panel := panelRegion(t, fetch(t, mux, "/products/"+overviewProduct.String()+"/overview").Body.String())

	markup := rowNamed(t, panel, "Open spec")
	if want := "Spec not yet signed off."; !strings.Contains(collapsed(markup), want) {
		t.Errorf("an in-design container with tasks does not say %q:\n%s", want, collapsed(markup))
	}
	if strings.Contains(markup, `data-krill="overview-in-flight-progress"`) {
		t.Errorf("an in-design container renders a progress bar:\n%s", collapsed(markup))
	}
}

// ---------------------------------------------------------------------------
// the shared store fakes
// ---------------------------------------------------------------------------

// progressProbe records the params the panel's read arrives with and
// answers with a fixture's containers, wrapping whichever shared fake the
// case is about so every other read the chrome makes still reaches that
// fake rather than this probe.
type progressProbe struct {
	store.TaskStore
	calls  *[]store.ProductTaskProgressParams
	answer []store.ContainerTaskProgress
}

func (p *progressProbe) SummarizeProductTaskProgress(_ context.Context, params store.ProductTaskProgressParams) (store.ProductTaskProgress, error) {
	*p.calls = append(*p.calls, params)
	return store.ProductTaskProgress{ProductID: params.ProductID, Containers: p.answer}, nil
}

// sharedFake is one of the fakes the panel's read runs through in some
// other target. Each exists because every go_test target in this package
// renders a full shell page, and each answers the panel's read with no
// containers -- which is the shape that would let a panel consulting
// nothing at all still render its empty state.
type sharedFake struct {
	name string
	tasks store.TaskStore
}

// shellFakes are all five of them, named as they are declared, so a new
// shared fake added for a sixth target has to be added here too.
func shellFakes() []sharedFake {
	return []sharedFake{
		{"chromeTaskCounter", chromeTaskCounter{}},
		{"navTasks", &navTasks{milestoneID: navMilestoneID}},
		{"productScopeTasks", productScopeTasks{}},
		{"fakeTaskLister", &fakeTaskLister{tasks: map[uuid.UUID][]store.TaskSummary{}}},
		{"fakeDetailStore", &fakeDetailStore{tasks: map[uuid.UUID]store.Task{}}},
	}
}

// probeMux mounts the shell's own routes with the given task store in
// place, so the panel renders through the same registrations a real page
// does rather than through a bare handler.
func probeMux(t *testing.T, tasks store.TaskStore) *http.ServeMux {
	t.Helper()
	app := newTestApp(t)
	app.tasks = tasks
	app.spec = &fakeSpecReader{products: []store.Product{{ID: overviewProduct, Name: "krill"}}}
	app.credentials = &fakeCredentials{}
	app.designSessions = navStubDesignSessions{}
	app.revisionEvents = navStubRevisionEvents{}
	mux := http.NewServeMux()
	app.mountShellRoutes(mux)
	return mux
}

// TestThePanelReadReachesEverySharedChromeFake is the honesty check on the
// fakes the panel's read runs through.
//
// Each answers with no containers whatever it is asked, so a panel that
// rendered its empty state without calling the read would satisfy every
// test in this file and every target that uses one of them. So each is
// wrapped in a probe that records the call, and both halves are asserted:
// the read is reached (once, with this product), and its answer is what
// the panel renders -- a fixture container shows up as a row rather than
// being quietly dropped in favour of the empty state the fake would have
// given anyway.
func TestThePanelReadReachesEverySharedChromeFake(t *testing.T) {
	for _, fake := range shellFakes() {
		t.Run(fake.name, func(t *testing.T) {
			// An in-flight container, so the probe's answer and the fake's
			// own empty answer cannot both render the same panel.
			container := milestoneProgress(uuid.MustParse(panelMilestone), "Probe milestone",
				store.MilestoneStatusInProgress, store.TaskLaneCounts{Done: 1, Implementation: 1})

			var calls []store.ProductTaskProgressParams
			probe := &progressProbe{
				TaskStore: fake.tasks,
				calls:     &calls,
				answer:    []store.ContainerTaskProgress{container},
			}
			panel := panelRegion(t, fetch(t, probeMux(t, probe), "/products/"+overviewProduct.String()+"/overview").Body.String())

			if len(calls) != 1 {
				t.Fatalf("the panel called the progress read %d times behind this fake, want 1", len(calls))
			}
			if calls[0].ProductID != overviewProduct {
				t.Errorf("the read was for product %s, want %s", calls[0].ProductID, overviewProduct)
			}
			// The read was made and its answer rendered: without this the
			// recording above would prove only that something called out.
			if !strings.Contains(collapsed(panel), "Probe milestone") {
				t.Errorf("the panel did not render the container the read returned:\n%s", collapsed(panel))
			}
			if strings.Contains(panel, `data-krill="overview-in-flight-panel-empty"`) {
				t.Errorf("the panel rendered its empty state despite the read returning a container:\n%s", collapsed(panel))
			}

			// And the same fake with no containers is the empty answer, so
			// the case above is the read being honoured rather than the
			// panel always rendering rows.
			var quiet []store.ProductTaskProgressParams
			empty := panelRegion(t, fetch(t, probeMux(t, &progressProbe{
				TaskStore: fake.tasks,
				calls:     &quiet,
			}), "/products/"+overviewProduct.String()+"/overview").Body.String())
			if !strings.Contains(empty, `data-krill="overview-in-flight-panel-empty"`) {
				t.Errorf("an empty read did not render the empty state:\n%s", collapsed(empty))
			}
		})
	}
}