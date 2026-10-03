// NFR 7b497d92's timestamp rule, asserted on every task view rather than
// on the one that happened to get it right first:
//
//	Every timestamp is a time element whose datetime is the RFC3339
//	instant, whose title is the exact UTC instant and whose text is the
//	absolute instant; a client enhancer rewrites the visible text to
//	relative.
//
// The Product-wide Board already carried these assertions
// (product_board_card_states_test.go); the other three task views render
// the same value and were the defect. One case per view, all four driven
// through their real handler, so a case that passes is a statement about
// the markup an operator receives.
//
// Each view is asked twice: once for a task holding a lease, and once for
// a task whose claim was never taken. The second is not the smaller half
// -- a view that renders an instant unconditionally would satisfy every
// attribute assertion below while naming a lease nobody holds.

package main

import (
	"net/http"
	"regexp"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
)

// leaseTimeTagRE is a rendered <time> element: its attributes and its own
// text. The capture of the content is the point -- it is what an operator
// reads with scripting off, so it is the assertion's subject rather than
// the attributes beside it.
var leaseTimeTagRE = regexp.MustCompile(`<time ([^>]*)>([^<]*)</time>`)

// leaseAttrRE reads the attributes out of an element's opening tag. The
// leading whitespace is optional because the FIRST attribute has none left
// once the tag name has been matched off.
var leaseAttrRE = regexp.MustCompile(`(?:\s|^)([a-zA-Z-]+)="([^"]*)"`)

// leaseTimeElem is one <time> element's attributes and text.
type leaseTimeElem struct {
	attrs map[string]string
	text  string
}

func (e leaseTimeElem) attr(name string) string { return e.attrs[name] }

// leaseTimeElements is every <time> element in the markup carrying
// data-krill="task-lease" -- the hook the countdown script keys on. It
// returns every match rather than the first so a view that renders the
// instant twice cannot pass by having one of the two right.
func leaseTimeElements(page string) []leaseTimeElem {
	var out []leaseTimeElem
	for _, m := range leaseTimeTagRE.FindAllStringSubmatch(page, -1) {
		e := leaseTimeElem{attrs: map[string]string{}, text: m[2]}
		for _, a := range leaseAttrRE.FindAllStringSubmatch(m[1], -1) {
			e.attrs[a[1]] = a[2]
		}
		if e.attrs["data-krill"] != "task-lease" {
			continue
		}
		out = append(out, e)
	}
	return out
}

// countdownTargets is the subset the head's leaseCountdownScript rewrites.
// Its selector is `time[data-krill="task-lease"][datetime]`, so a lease
// element that is missing datetime is invisible to it however correct the
// rest of its markup is -- which is why this is decided by the attribute
// rather than by the presence of the hook.
func countdownTargets(elems []leaseTimeElem) []leaseTimeElem {
	var out []leaseTimeElem
	for _, e := range elems {
		if e.attr("datetime") != "" {
			out = append(out, e)
		}
	}
	return out
}

// leaseTimeView is one task view's markup, rendered with exactly one task.
type leaseTimeView struct {
	name string
	// render serves this view with a single task holding the given lease,
	// or with a task whose claim was never taken when lease is nil, and
	// returns the whole markup an operator receives.
	render func(t *testing.T, lease *time.Time) string
}

// leaseTimeViews is every task view that states a lease: the Product-wide
// Tasks table, the task detail, and the two per-milestone views the
// redesigned scope redirected away from. The Product-wide Board is left out
// -- it is the site the other three were brought into line with, and
// product_board_card_states_test.go already holds these assertions.
func leaseTimeViews() []leaseTimeView {
	return []leaseTimeView{
		{name: "tasks table", render: leaseTimeTasksTable},
		{name: "task detail", render: leaseTimeTaskDetail},
		{name: "legacy milestone board", render: leaseTimeLegacyBoard},
		{name: "legacy milestone list", render: leaseTimeLegacyList},
	}
}

// leaseTimeTasksTable renders the Product-wide Tasks table around a single
// row, through the real shell registrations.
func leaseTimeTasksTable(t *testing.T, lease *time.Time) string {
	t.Helper()
	row := productTaskRowFixture()[0]
	row.ClaimID, row.LeaseExpiresAt = nil, nil
	if lease != nil {
		claim := uuid.New()
		row.ClaimID, row.LeaseExpiresAt = &claim, lease
	}

	tasks := &recordingProductTasks{rows: []store.ProductTaskRow{row}, total: 1}
	rec := fetch(t, productTaskMux(t, tasks, productTaskListing(), nil), productTaskTasksURL(""))
	require.Equal(t, http.StatusOK, rec.Code)
	return rec.Body.String()
}

// leaseTimeTaskDetail renders the task detail page's Claim section around a
// single task.
func leaseTimeTaskDetail(t *testing.T, lease *time.Time) string {
	t.Helper()
	f := newDetailFixture(t)
	task := store.Task{Title: "lease-task", CurrentLane: store.LaneTesting}
	if lease != nil {
		claim := uuid.New()
		task.CurrentClaimID, task.LeaseExpiresAt = &claim, lease
		f.store.claim = store.Claim{ID: claim, SessionID: store.SessionID(uuid.New()), ClaimedAt: time.Now().UTC()}
	}
	task = f.add(task)

	code, html := f.get(task.ID.String(), true)
	require.Equal(t, http.StatusOK, code)
	return html
}

// leaseTimeLegacyBoard renders the pre-redesign per-milestone board. Its
// URL is a Successor now, but the handler is still mounted directly by
// these tests and the markup is still what the template compiles to.
func leaseTimeLegacyBoard(t *testing.T, lease *time.Time) string {
	t.Helper()
	f := boardFixture(t)
	f.tasks.tasks[f.mid] = []store.TaskSummary{leaseTaskSummary(lease)}

	code, html := f.getBoard(f.pid.String(), f.mid.String(), true)
	require.Equal(t, http.StatusOK, code)
	return html
}

// leaseTimeLegacyList renders the pre-redesign per-milestone task list, for
// the same reason as the legacy board beside it.
func leaseTimeLegacyList(t *testing.T, lease *time.Time) string {
	t.Helper()
	f := newTaskFixture(t)
	f.tasks.tasks[f.mid] = []store.TaskSummary{leaseTaskSummary(lease)}

	code, html := f.get(f.pid.String(), f.mid.String(), true)
	require.Equal(t, http.StatusOK, code)
	return html
}

func leaseTaskSummary(lease *time.Time) store.TaskSummary {
	s := store.TaskSummary{ID: uuid.New(), Title: "lease-task", CurrentLane: store.LaneTesting}
	if lease != nil {
		claim := uuid.New()
		s.CurrentClaimID, s.LeaseExpiresAt, s.HasLiveClaim = &claim, lease, true
	}
	return s
}

// TestEveryTaskViewLeaseIsATimeElementWithAnInstant is the NFR's rule, one
// case per view: the lease is a <time> whose datetime is the instant the
// read observed, whose title repeats it for hover, and whose own text is
// that same absolute instant -- the whole answer when the client's
// countdown never runs.
func TestEveryTaskViewLeaseIsATimeElementWithAnInstant(t *testing.T) {
	for _, view := range leaseTimeViews() {
		t.Run(view.name, func(t *testing.T) {
			// Truncated to the second because that is the precision the
			// views format to; a lease the assertion and the markup
			// disagree about by a sub-second remainder would make every
			// case below a coin flip rather than a statement.
			lease := time.Now().Add(20 * time.Minute).UTC().Truncate(time.Second)
			page := view.render(t, &lease)

			elems := leaseTimeElements(page)
			require.Len(t, elems, 1, "the view states no lease element, or states it twice:\n%s", page)
			instant := lease.Format(time.RFC3339)

			assert.Equal(t, instant, elems[0].attr("datetime"),
				"the enhancer's datetime must be the instant the read observed")
			assert.Equal(t, instant, elems[0].attr("title"),
				"the exact UTC instant has to be available on hover")
			assert.Equal(t, instant, elems[0].text,
				"with scripting off the operator must still read when the lease runs out")
			assert.Empty(t, relativeLeaseRE.FindString(elems[0].text),
				"and must not read it as the response's age")
		})
	}
}

// TestEveryTaskViewLeaseIsUpgradedByTheCountdownScript is the JS-on half.
// The head's selector is `time[data-krill="task-lease"][datetime]`; a view
// whose lease element omits datetime renders a correct-looking instant
// that simply never counts down, and no assertion about the text alone
// would notice.
func TestEveryTaskViewLeaseIsUpgradedByTheCountdownScript(t *testing.T) {
	for _, view := range leaseTimeViews() {
		t.Run(view.name, func(t *testing.T) {
			lease := time.Now().Add(20 * time.Minute).UTC().Truncate(time.Second)
			page := view.render(t, &lease)

			elems := leaseTimeElements(page)
			require.Len(t, elems, 1, "the view states no lease element:\n%s", page)
			assert.Len(t, countdownTargets(elems), 1,
				"the countdown script's selector must match this view's lease element")
		})
	}
}

// TestEveryTaskViewRendersNoLeaseWithoutAClaim keeps the no-lease case
// honest at every site: a task nobody claimed has no instant to state, so
// the element must be absent rather than present and empty or naming some
// default. The Product-wide Board's own version of this is
// product_board_card_test.go's; this is the three that lacked it.
func TestEveryTaskViewRendersNoLeaseWithoutAClaim(t *testing.T) {
	for _, view := range leaseTimeViews() {
		t.Run(view.name, func(t *testing.T) {
			page := view.render(t, nil)
			assert.Empty(t, leaseTimeElements(page),
				"an unclaimed task must render no lease element at all:\n%s", page)
		})
	}
}
