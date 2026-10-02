package main

// Handler-level coverage for the read-only task board (board_page.go).

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
)

func (f *taskFixture) getBoard(pid, mid string, hx bool) (int, string) {
	req := httptest.NewRequest(http.MethodGet, "/spec/products/"+pid+"/milestones/"+mid+"/board", nil)
	if hx {
		req.Header.Set("HX-Request", "true")
	}
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func boardFixture(t *testing.T) *taskFixture {
	f := newTaskFixture(t)
	f.mux.HandleFunc("GET /spec/products/{id}/milestones/{mid}/board", f.app.handleTaskBoard)
	return f
}

func TestBoardFiveColumnsCountsAndPlacement(t *testing.T) {
	f := boardFixture(t)
	claim := uuid.New()
	future := time.Now().Add(time.Hour).UTC()
	f.tasks.tasks[f.mid] = []store.TaskSummary{
		{ID: uuid.New(), Title: "skipper", CurrentLane: store.Lane("Validation"), AttemptCount: 2, CurrentClaimID: &claim, LeaseExpiresAt: &future, HasLiveClaim: true},
		{ID: uuid.New(), Title: "other", CurrentLane: store.Lane("Validation")},
	}
	code, html := f.getBoard(f.pid.String(), f.mid.String(), true)
	require.Equal(t, 200, code)

	hdr := regexp.MustCompile(`data-krill="column-header">\s*(\w+) \(<span data-krill="column-count">(\d+)</span>\)`).FindAllStringSubmatch(html, -1)
	var got []string
	for _, m := range hdr {
		got = append(got, m[1]+"="+m[2])
	}
	assert.Equal(t, []string{"Scaffold=0", "Implementation=0", "Testing=0", "Validation=2", "Done=0"}, got)
	assert.Equal(t, 5, strings.Count(html, `data-krill="board-column"`))
	assert.Equal(t, 1, strings.Count(html, "skipper"))
	// The card sits inside the Validation column, before the Done column.
	assert.Less(t, strings.Index(html, `data-krill-lane="Validation"`), strings.Index(html, "skipper"))
	assert.Less(t, strings.Index(html, "skipper"), strings.Index(html, `data-krill-lane="Done"`))
	assert.Contains(t, html, "2 of 3")
	assert.Contains(t, html, future.Format(time.RFC3339))
	assert.Contains(t, html, `data-krill-claim-id="`+claim.String()+`"`)
	assert.Contains(t, html, "/milestones/"+f.mid.String()+"/tasks/")
}

func TestBoardBadgesStayInColumn(t *testing.T) {
	f := boardFixture(t)
	claim, esc := uuid.New(), uuid.New()
	past := time.Now().Add(-time.Hour).UTC()
	f.tasks.tasks[f.mid] = []store.TaskSummary{
		{ID: uuid.New(), Title: "stale", CurrentLane: store.Lane("Testing"), CurrentClaimID: &claim, LeaseExpiresAt: &past},
		{ID: uuid.New(), Title: "capped", CurrentLane: store.Lane("Testing"), AttemptCount: store.DefaultAttemptCap},
		{ID: uuid.New(), Title: "esc", CurrentLane: store.Lane("Testing"), CurrentEscalationID: &esc},
		{ID: uuid.New(), Title: "gone", CurrentLane: store.Lane("Testing"), CancelledAt: &past},
	}
	_, html := f.getBoard(f.pid.String(), f.mid.String(), true)
	for _, k := range []string{"lease-expired", "capped", "escalated", "cancelled"} {
		assert.Contains(t, html, `data-krill="task-badge-`+k+`"`)
	}
	assert.NotContains(t, html, `data-krill="task-badge-claimed"`)
	assert.Equal(t, 5, strings.Count(html, `data-krill="board-column"`))
	assert.Contains(t, html, "Testing (<span data-krill=\"column-count\">4</span>)")
}

func TestBoardCutMilestoneLinksMilepebbles(t *testing.T) {
	f := boardFixture(t)
	f.tasks.tasks[f.cutID] = []store.TaskSummary{{ID: uuid.New(), Title: "must-not-appear"}}
	_, html := f.getBoard(f.pid.String(), f.cutID.String(), true)
	assert.Contains(t, html, milestoneTasksPath(f.pid, f.mpID))
	assert.Contains(t, html, milestoneBoardPath(f.pid, f.mpID))
	assert.NotContains(t, html, "must-not-appear")
	assert.NotContains(t, html, `data-krill="board-column"`)
	assert.Empty(t, f.tasks.calls)
}

func TestBoardCrossProductAndMalformed(t *testing.T) {
	f := boardFixture(t)
	code, _ := f.getBoard(f.pid.String(), uuid.NewString(), false)
	assert.Equal(t, http.StatusNotFound, code)
	code, _ = f.getBoard(f.pid.String(), "not-a-uuid", false)
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Empty(t, f.tasks.calls)
}

func TestBoardEmptyReadErrorAndFragmentShape(t *testing.T) {
	f := boardFixture(t)
	_, frag := f.getBoard(f.pid.String(), f.mid.String(), true)
	_, full := f.getBoard(f.pid.String(), f.mid.String(), false)

	assert.True(t, strings.HasPrefix(strings.TrimSpace(frag), `<section id="krill-task-board"`))
	assert.Equal(t, 1, strings.Count(frag, `id="krill-task-board"`))
	assert.Contains(t, frag, `hx-target="#krill-task-board"`)
	assert.Contains(t, frag, `hx-get="`+milestoneBoardPath(f.pid, f.mid)+`"`)
	assert.Contains(t, frag, `data-krill="loaded-at"`)
	assert.Contains(t, frag, milestoneTasksPath(f.pid, f.mid))
	assert.Contains(t, frag, "/spec/products/"+f.pid.String()+"/delivery")
	assert.NotContains(t, frag, "<html")
	assert.Contains(t, full, "<html")
	for _, body := range []string{frag, full} {
		assert.NotContains(t, body, "<form")
		assert.NotContains(t, body, "hx-post")
		assert.NotContains(t, body, "hx-put")
		assert.NotContains(t, body, "hx-delete")
		assert.NotContains(t, body, "hx-trigger=\"every")
	}

	f.tasks.err = assert.AnError
	_, failed := f.getBoard(f.pid.String(), f.mid.String(), true)
	assert.Contains(t, failed, `data-krill="task-board-error"`)
	assert.NotContains(t, failed, `data-krill="board-column"`)
}

func TestBoardNoTruncation(t *testing.T) {
	f := boardFixture(t)
	var ts []store.TaskSummary
	for i := 0; i < 250; i++ {
		ts = append(ts, store.TaskSummary{ID: uuid.New(), Title: "bulk", CurrentLane: store.Lane("Scaffold")})
	}
	f.tasks.tasks[f.mid] = ts
	_, html := f.getBoard(f.pid.String(), f.mid.String(), true)
	assert.Equal(t, 250, len(regexp.MustCompile(`data-krill="board-card"`).FindAllString(html, -1)))
}
