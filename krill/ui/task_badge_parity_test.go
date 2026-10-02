package main

// Acceptance coverage for the one-mapper-per-value rule: one lane mapper
// and one state mapper, so the same task renders the same lane badge and
// the same state badges on the list, the board and the detail -- and
// neither value reaches any of those three pages as bare text.
//
// The three views are driven through their real handlers over one store,
// so a parity failure means the views disagree, not that the fixture
// described two different tasks.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
)

// badgeSpanRE matches one htmxui.Badge: its class set, its data-krill
// hook and its label. The class set is captured so a colour or
// soft-treatment drift between two pages reads as a difference rather
// than as two badges that happen to share a label.
var badgeSpanRE = regexp.MustCompile(`<span class="(badge[^"]*)" data-krill="([^"]+)">([^<]*)</span>`)

// The detail's two <dd>s: the one the lane value lives in and the one the
// state values live in. Both used to hold bare text, which is why they
// are the places a bare-text regression would show up.
var (
	detailLaneDD  = regexp.MustCompile(`(?s)Current lane</dt>\s*<dd[^>]*>(.*?)</dd>`)
	detailStateDD = regexp.MustCompile(`(?s)<dd data-krill="task-state">(.*?)</dd>`)
)

// badgePair is one page's rendering of a task's lane and state, kept as
// whole span markup so the comparison is over what an operator sees.
type badgePair struct {
	lane  string
	state []string
}

// stripBadges removes every badge span, leaving only the markup and text
// that reached the page outside a badge.
func stripBadges(html string) string {
	return badgeSpanRE.ReplaceAllString(html, "")
}

// badgePairOf reads one page's lane badge and state badges.
func badgePairOf(t *testing.T, page, html string) badgePair {
	t.Helper()
	var p badgePair
	for _, m := range badgeSpanRE.FindAllStringSubmatch(html, -1) {
		switch {
		case m[2] == "task-lane":
			p.lane = m[0]
		case strings.HasPrefix(m[2], "task-badge-"):
			p.state = append(p.state, m[0])
		}
	}
	require.NotEmpty(t, p.lane, "%s rendered no lane badge at all", page)
	return p
}

// badgeKeys are the data-krill hooks of a page's state badges, in the
// order the page renders them.
func badgeKeys(t *testing.T, spans []string) []string {
	t.Helper()
	var keys []string
	for _, s := range spans {
		m := badgeSpanRE.FindStringSubmatch(s)
		require.Len(t, m, 4, "not a badge span: %s", s)
		keys = append(keys, m[2])
	}
	return keys
}

// ddInner is the inner markup of one of the detail's value <dd>s, or ""
// when the placeholder is gone -- which is itself the failure this test
// is about.
func ddInner(t *testing.T, page, html string, re *regexp.Regexp) string {
	t.Helper()
	m := re.FindStringSubmatch(html)
	require.Len(t, m, 2, "%s has no lane/state <dd> left to check", page)
	return m[1]
}

// assertNoBareText fails when value still reaches region as text rather
// than inside a badge.
func assertNoBareText(t *testing.T, page, region, value string) {
	t.Helper()
	assert.NotContains(t, stripBadges(region), ">"+value+"<",
		"%s renders %q as bare text, not as a badge", page, value)
}

// parityStore serves one task through both reads the three task views
// make: the list and the board read a TaskSummary, the detail reads the
// Task. Both are built from the same fields (see parityFixture.seed), so
// a parity failure is a disagreement between views.
type parityStore struct {
	store.TaskStore
	summary store.TaskSummary
	task    store.Task
	claim   store.Claim
}

func (f *parityStore) CountEscalatedTasks(context.Context, store.ListEscalatedTasksParams) (int, error) {
	return 0, nil
}

func (f *parityStore) SummarizeProductTaskProgress(_ context.Context, p store.ProductTaskProgressParams) (store.ProductTaskProgress, error) {
	return store.ProductTaskProgress{ProductID: p.ProductID, Containers: []store.ContainerTaskProgress{}}, nil
}

func (f *parityStore) CountConsoleOverview(context.Context, store.ConsoleOverviewParams) (store.ConsoleOverviewCounts, error) {
	return store.ConsoleOverviewCounts{}, nil
}

func (f *parityStore) ListTasksByMilestone(_ context.Context, _ uuid.UUID) ([]store.TaskSummary, error) {
	return []store.TaskSummary{f.summary}, nil
}

func (f *parityStore) GetTaskByID(_ context.Context, id uuid.UUID) (store.Task, error) {
	if id != f.task.ID {
		return store.Task{}, store.ErrNotFound
	}
	return f.task, nil
}

func (f *parityStore) ListDependencies(context.Context, uuid.UUID, uuid.UUID) ([]store.TaskDependency, error) {
	return nil, nil
}

func (f *parityStore) ListNotesForTask(context.Context, uuid.UUID, uuid.UUID) ([]store.Note, error) {
	return nil, nil
}

func (f *parityStore) GetClaimByID(context.Context, uuid.UUID) (store.Claim, error) {
	return f.claim, nil
}

type parityFixture struct {
	pid, mid uuid.UUID
	store    *parityStore
	mux      *http.ServeMux
}

// newParityFixture mounts the list, board and detail routes over one store
// and one product, so all three pages describe the same single task.
func newParityFixture(t *testing.T) *parityFixture {
	t.Helper()
	f := &parityFixture{pid: uuid.New(), mid: uuid.New(), store: &parityStore{}}
	spec := &fakeSliceSpec{fakeSpecReader: &fakeSpecReader{listing: slice.DeliveryListing{
		Milestones: []slice.MilestoneListingEntry{{ID: f.mid, Name: "Plain"}},
	}}}
	app := &App{spec: spec, tasks: f.store, scopes: chromeScopes{}}
	f.mux = http.NewServeMux()
	f.mux.HandleFunc("GET /spec/products/{id}/milestones/{mid}/tasks", app.handleTaskList)
	f.mux.HandleFunc("GET /spec/products/{id}/milestones/{mid}/board", app.handleTaskBoard)
	f.mux.HandleFunc("GET /spec/products/{id}/milestones/{mid}/tasks/{tid}", app.handleTaskDetail)
	return f
}

// seed installs the one task every view will show, described identically
// as the summary the list and board read and the task the detail reads.
func (f *parityFixture) seed(s store.TaskSummary) store.TaskSummary {
	s.ID = uuid.New()
	s.Title = "parity-probe"
	f.store.summary = s
	f.store.task = store.Task{
		ID: s.ID, MilestoneID: f.mid, Title: s.Title,
		LaneSequence: []store.Lane{s.CurrentLane}, CurrentLane: s.CurrentLane,
		AttemptCount:        s.AttemptCount,
		CurrentClaimID:      s.CurrentClaimID,
		LeaseExpiresAt:      s.LeaseExpiresAt,
		CurrentEscalationID: s.CurrentEscalationID,
		CancelledAt:         s.CancelledAt,
	}
	if s.CurrentClaimID != nil {
		f.store.claim = store.Claim{
			ID: *s.CurrentClaimID, SessionID: store.SessionID(uuid.New()),
			ClaimedAt: time.Now().UTC(),
		}
	}
	return s
}

// pages renders the list, the board and the detail for the seeded task.
func (f *parityFixture) pages(t *testing.T, tid uuid.UUID) map[string]string {
	t.Helper()
	base := "/spec/products/" + f.pid.String() + "/milestones/" + f.mid.String()
	out := map[string]string{}
	for page, path := range map[string]string{
		"list":   base + "/tasks",
		"board":  base + "/board",
		"detail": base + "/tasks/" + tid.String(),
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("HX-Request", "true")
		rec := httptest.NewRecorder()
		f.mux.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, "%s page", page)
		out[page] = rec.Body.String()
	}
	return out
}

// assertSamePairOnAllThree is the acceptance assertion proper: whatever
// the list renders, the board and the detail render the same badges for.
func assertSamePairOnAllThree(t *testing.T, pages map[string]string) badgePair {
	t.Helper()
	pairs := map[string]badgePair{}
	for _, page := range []string{"list", "board", "detail"} {
		pairs[page] = badgePairOf(t, page, pages[page])
	}
	assert.Equal(t, pairs["list"], pairs["board"], "the board must render the same badge pair as the list")
	assert.Equal(t, pairs["list"], pairs["detail"], "the detail must render the same badge pair as the list")
	return pairs["list"]
}

func TestTaskLaneAndStateBadgesAreIdenticalOnListBoardAndDetail(t *testing.T) {
	f := newParityFixture(t)
	claim, esc := uuid.New(), uuid.New()
	lease := time.Now().Add(time.Hour).UTC()
	cancelled := time.Now().Add(-time.Hour).UTC()

	// One task per canonical lane, each holding four states at once, so
	// every lane is checked against a badge set big enough for the three
	// views to disagree about.
	for _, lane := range store.CanonicalLaneOrder {
		t.Run(string(lane), func(t *testing.T) {
			seeded := f.seed(store.TaskSummary{
				CurrentLane: lane, AttemptCount: store.DefaultAttemptCap,
				CurrentClaimID: &claim, LeaseExpiresAt: &lease,
				CurrentEscalationID: &esc, CancelledAt: &cancelled,
			})
			pages := f.pages(t, seeded.ID)
			pair := assertSamePairOnAllThree(t, pages)

			assert.Contains(t, pair.lane, ">"+string(lane)+"<", "the lane badge must carry the lane's own name")
			assert.Equal(t,
				[]string{"task-badge-claimed", "task-badge-capped", "task-badge-escalated", "task-badge-cancelled"},
				badgeKeys(t, pair.state))

			// None of it reaches a page as text: not in the list row, not
			// in the board card, and not in the detail's two <dd>s.
			assertNoBareText(t, "list", stripBadges(pages["list"]), string(lane))
			assertNoBareText(t, "board", stripBadges(pages["board"]), string(lane))
			for _, label := range []string{"Claimed", "Capped", "Escalated", "Cancelled"} {
				assertNoBareText(t, "list", stripBadges(pages["list"]), label)
				assertNoBareText(t, "board", stripBadges(pages["board"]), label)
				assertNoBareText(t, "detail", ddInner(t, "detail", pages["detail"], detailStateDD), label)
			}
			laneDD := ddInner(t, "detail", pages["detail"], detailLaneDD)
			assert.Equal(t, pair.lane, strings.TrimSpace(laneDD),
				"the detail's lane <dd> must hold the lane badge and nothing else")
			stateDD := strings.TrimSpace(ddInner(t, "detail", pages["detail"], detailStateDD))
			assert.Equal(t, strings.Join(pair.state, ""), stateDD,
				"the detail's state <dd> must hold exactly the list's state badges")
		})
	}
}

func TestExpiredLeaseReplacesClaimedOnAllThreeViews(t *testing.T) {
	f := newParityFixture(t)
	claim := uuid.New()
	past := time.Now().Add(-time.Hour).UTC()
	seeded := f.seed(store.TaskSummary{
		CurrentLane: store.LaneTesting, CurrentClaimID: &claim, LeaseExpiresAt: &past,
	})
	pages := f.pages(t, seeded.ID)
	pair := assertSamePairOnAllThree(t, pages)

	assert.Equal(t, []string{"task-badge-lease-expired"}, badgeKeys(t, pair.state))
	for _, page := range []string{"list", "board", "detail"} {
		assert.NotContains(t, pages[page], `data-krill="task-badge-claimed"`,
			"%s must not show a claimed badge for a lapsed lease", page)
		assertNoBareText(t, page, stripBadges(pages[page]), "Lease expired")
	}
}

func TestDoneTaskWithNoLiveStateShowsNoStateBadgeOnAnyView(t *testing.T) {
	f := newParityFixture(t)
	seeded := f.seed(store.TaskSummary{CurrentLane: store.LaneDone})
	pages := f.pages(t, seeded.ID)
	pair := assertSamePairOnAllThree(t, pages)

	assert.Empty(t, pair.state, "a Done task with nothing outstanding has no state to badge")
	assert.Equal(t, "", strings.TrimSpace(ddInner(t, "detail", pages["detail"], detailStateDD)),
		"the detail's state <dd> must be empty rather than say \"active\"")
	assert.Equal(t, pair.lane, strings.TrimSpace(ddInner(t, "detail", pages["detail"], detailLaneDD)),
		"a Done task still badges its lane")
	for _, page := range []string{"list", "board", "detail"} {
		assert.NotContains(t, pages[page], "active",
			"%s must not fall back to a bare-text state for an unstate'd task", page)
	}
}
