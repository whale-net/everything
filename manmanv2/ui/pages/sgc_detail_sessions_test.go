package pages

import (
	"strings"
	"testing"

	"github.com/whale-net/everything/libs/go/htmxauth"
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
	"github.com/whale-net/everything/manmanv2/ui/components"
)

// This file guards #1531's "Session history" section on the deployment page
// (FR8, FR9, and the session-history half of FR12): every persona sees a
// newest-first list of the deployment's previous sessions, each entry
// linking to its own /sessions/{id} page, with an explicit empty state when
// there are none. sgc_detail.templ itself does not sort -- ordering is
// components.SortSessionsNewestFirst's job (verified independently and, at
// the handler layer, in handlers_sgc_test.go) -- so every case here builds
// its SGCDetailPageData.Sessions by sorting with the real helper first,
// mirroring what handleSGCDetail does, and then asserts the template
// preserves (rather than re-derives) that order.
//
// mutation-tested (verified red, by hand, then reverted): reversing
// SortSessionsNewestFirst's comparator (`si.GetStartedAt() < sj.GetStartedAt()`)
// made TestSGCDetail_SessionHistory_OrderedNewestFirst fail on its relative-
// index assertion; reverting restored green.

// sessionHistorySection isolates the "Session history" card's markup from
// the rest of the (much larger) SGCDetail page, so assertions about row
// order/hrefs/empty-state can't accidentally match unrelated sections --
// notably the Danger Zone's per-session stop forms further down the page,
// which also reference /sessions/{id}/stop hrefs and session ids.
func sessionHistorySection(t *testing.T, body string) string {
	t.Helper()
	start := strings.Index(body, ">Session history<")
	if start < 0 {
		t.Fatalf("expected a 'Session history' heading in rendered body, got %q", body)
	}
	end := strings.Index(body[start:], "Danger Zone: intentionally kept")
	if end < 0 {
		t.Fatalf("expected a Danger Zone marker after the Session history card, got %q", body)
	}
	return body[start : start+end]
}

// sortedSGCDetailData builds SGCDetailPageData with Sessions pre-sorted by
// components.SortSessionsNewestFirst, exactly as handleSGCDetail does
// before handing the slice to the template (NFR1): the template itself
// must never re-sort.
func sortedSGCDetailData(sgc *manmanpb.ServerGameConfig, user *htmxauth.UserInfo, sessions []*manmanpb.Session) SGCDetailPageData {
	sorted := make([]*manmanpb.Session, len(sessions))
	copy(sorted, sessions)
	components.SortSessionsNewestFirst(sorted)
	return SGCDetailPageData{
		Layout:   components.LayoutData{Title: "SGC", User: user},
		SGC:      sgc,
		Sessions: sorted,
	}
}

// --- 1. distinct started_at values render newest-first ----------------------

func TestSGCDetail_SessionHistory_OrderedNewestFirst(t *testing.T) {
	sgc := &manmanpb.ServerGameConfig{ServerGameConfigId: 1, Status: "active"}
	sessions := []*manmanpb.Session{
		{SessionId: 1, StartedAt: 100, Status: "stopped"},
		{SessionId: 2, StartedAt: 300, Status: "stopped"},
		{SessionId: 3, StartedAt: 200, Status: "stopped"},
	}

	data := sortedSGCDetailData(sgc, nil, sessions)
	body := renderPage(t, SGCDetail(data))
	section := sessionHistorySection(t, body)

	idx2 := strings.Index(section, "/sessions/2")
	idx3 := strings.Index(section, "/sessions/3")
	idx1 := strings.Index(section, "/sessions/1")
	if idx2 < 0 || idx3 < 0 || idx1 < 0 {
		t.Fatalf("expected all three session ids to render, got section %q", section)
	}
	if !(idx2 < idx3 && idx3 < idx1) {
		t.Errorf("expected newest-first order (session 2 [started_at=300], then 3 [200], then 1 [100]), got indices 2=%d 3=%d 1=%d in section %q", idx2, idx3, idx1, section)
	}
}

// --- 2. every row links to its own /sessions/{id} (FR9) ---------------------

func TestSGCDetail_SessionHistory_HrefsLinkToOwnSession(t *testing.T) {
	sgc := &manmanpb.ServerGameConfig{ServerGameConfigId: 2, Status: "active"}
	sessions := []*manmanpb.Session{
		{SessionId: 10, StartedAt: 100, Status: "stopped"},
		{SessionId: 11, StartedAt: 200, Status: "stopped"},
	}

	data := sortedSGCDetailData(sgc, nil, sessions)
	body := renderPage(t, SGCDetail(data))
	section := sessionHistorySection(t, body)

	for _, want := range []string{`href="/sessions/10"`, `href="/sessions/11"`} {
		if !strings.Contains(section, want) {
			t.Errorf("expected %q in the Session history section, got %q", want, section)
		}
	}
}

// --- 3. never-started sessions (started_at == 0) -----------------------------

func TestSGCDetail_SessionHistory_NeverStartedSessions_OrderedBySessionIdDesc(t *testing.T) {
	sgc := &manmanpb.ServerGameConfig{ServerGameConfigId: 3, Status: "active"}
	sessions := []*manmanpb.Session{
		{SessionId: 5, StartedAt: 0, Status: "pending"},
		{SessionId: 7, StartedAt: 0, Status: "pending"},
		{SessionId: 6, StartedAt: 0, Status: "pending"},
	}

	data := sortedSGCDetailData(sgc, nil, sessions)
	body := renderPage(t, SGCDetail(data))
	section := sessionHistorySection(t, body)

	idx7 := strings.Index(section, "/sessions/7")
	idx6 := strings.Index(section, "/sessions/6")
	idx5 := strings.Index(section, "/sessions/5")
	if idx7 < 0 || idx6 < 0 || idx5 < 0 {
		t.Fatalf("expected all three never-started session ids to render, got section %q", section)
	}
	if !(idx7 < idx6 && idx6 < idx5) {
		t.Errorf("expected descending session_id tie-break order (7, 6, 5) for equal started_at=0, got indices 7=%d 6=%d 5=%d in section %q", idx7, idx6, idx5, section)
	}
	if !strings.Contains(section, "Never") {
		t.Errorf("expected timeAgo(0) to render \"Never\" rather than panic or render a garbage time, got section %q", section)
	}
}

// --- 4. empty session list renders the empty state (FR12) -------------------

func TestSGCDetail_SessionHistory_EmptyState(t *testing.T) {
	sgc := &manmanpb.ServerGameConfig{ServerGameConfigId: 4, Status: "active"}

	data := sortedSGCDetailData(sgc, nil, nil)
	body := renderPage(t, SGCDetail(data))
	section := sessionHistorySection(t, body)

	if !strings.Contains(section, "No session history") {
		t.Errorf("expected the empty-state title, got section %q", section)
	}
	if !strings.Contains(section, "This deployment has no previous sessions yet.") {
		t.Errorf("expected the empty-state description, got section %q", section)
	}
	if strings.Contains(section, "<table") {
		t.Errorf("expected no table element for an empty session history, got section %q", section)
	}
}

// --- 5. persona-neutral: no role/admin conditional (FR8, NFR2) --------------

func TestSGCDetail_SessionHistory_PersonaNeutral(t *testing.T) {
	sgc := &manmanpb.ServerGameConfig{ServerGameConfigId: 5, Status: "active"}
	sessions := []*manmanpb.Session{
		{SessionId: 20, StartedAt: 100, Status: "stopped"},
		{SessionId: 21, StartedAt: 200, Status: "stopped"},
	}

	users := []*htmxauth.UserInfo{
		nil,
		{Sub: "gamer-1", Name: "Gamer", Roles: []string{}},
		{Sub: "admin-1", Name: "Admin", Roles: []string{"admin"}},
	}

	var sections []string
	for _, u := range users {
		data := sortedSGCDetailData(sgc, u, sessions)
		body := renderPage(t, SGCDetail(data))
		sections = append(sections, sessionHistorySection(t, body))
	}

	for i := 1; i < len(sections); i++ {
		if sections[i] != sections[0] {
			t.Errorf("expected the Session history section to render identically regardless of the layout's UserInfo (no persona conditional); user[0] section %q != user[%d] section %q", sections[0], i, sections[i])
		}
	}
}
