package pages

import (
	"strings"
	"testing"
	"time"

	"github.com/whale-net/everything/manmanv2/ui/components"
)

// This file guards task #2271's page-markup requirements: WD2's
// observation-only decision (no start/stop/restart control anywhere on
// /activity -- a deliberate divergence from the legacy 97-v2-activity
// wireframe) and FR2's terminology rule (no "SGC"/"server game config" in
// display text).

func activityFixtureData() ActivityPageData {
	now := time.Now()
	return ActivityPageData{
		Live: []ActivityLiveRow{
			{GameID: 10, GameName: "Minecraft", ServerID: 20, ServerName: "Alpha", ConfigName: "Survival", Uptime: 90 * time.Minute, SessionID: 1},
		},
		History: []ActivityHistoryRow{
			{GameID: 11, GameName: "Valheim", ServerID: 21, ServerName: "Beta", ConfigName: "Hardcore", TerminalStatus: "stopped", StartTime: now.Add(-2 * time.Hour), Duration: time.Hour, SessionID: 2},
		},
		FilterGameID: 0,
		FilterStatus: "",
	}
}

// TestActivity_NoStartStopRestartControl is the WD2 negative assertion:
// Activity is observation-only, so the rendered page must carry no
// start/stop/restart control. The only interactive control on the page is
// the GET filter form's "Apply Filters" submit button -- everything else
// (session links) is a plain anchor, never an hx-post action.
func TestActivity_NoStartStopRestartControl(t *testing.T) {
	body := renderPage(t, Activity(components.LayoutData{Title: "Activity"}, activityFixtureData()))

	if strings.Contains(body, "hx-post") {
		t.Errorf("expected no hx-post action anywhere on the observation-only Activity page (WD2), got body: %s", body)
	}
	for _, forbidden := range []string{">Stop<", ">Start<", ">Restart<"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("expected no %q control on the observation-only Activity page (WD2 -- this is a deliberate divergence from 97-v2-activity's Stop control), got body: %s", forbidden, body)
		}
	}
	// The theme switcher in the shared Layout chrome also renders <button>
	// elements (type="button", unrelated to any session control), so the
	// meaningful assertion is on submit buttons and forms, not on <button>
	// count as a whole: the only <form>/submit on the page is the filter
	// form's GET "Apply Filters".
	if got := strings.Count(body, "<form"); got != 1 {
		t.Errorf("expected exactly 1 <form> element (the GET filter form), got %d in body: %s", got, body)
	}
	if !strings.Contains(body, `<form method="GET" action="/activity">`) {
		t.Errorf("expected the sole form to be the GET /activity filter form, got body: %s", body)
	}
	if got := strings.Count(body, `type="submit"`); got != 1 {
		t.Errorf("expected exactly 1 submit control (Apply Filters), got %d in body: %s", got, body)
	}
	if !strings.Contains(body, ">Apply Filters<") {
		t.Errorf("expected the sole submit control to be the Apply Filters button, got body: %s", body)
	}
}

// TestActivity_NoSGCTerminology is the FR2 assertion: no "SGC" or "server
// game config" (case-insensitive) anywhere in display text. A deployment is
// named by its server and config, never a raw SGC identifier.
func TestActivity_NoSGCTerminology(t *testing.T) {
	body := renderPage(t, Activity(components.LayoutData{Title: "Activity"}, activityFixtureData()))

	lower := strings.ToLower(body)
	if strings.Contains(lower, "sgc") {
		t.Errorf("expected no \"SGC\" terminology anywhere on the Activity page (FR2), got body: %s", body)
	}
	if strings.Contains(lower, "server game config") {
		t.Errorf("expected no \"server game config\" terminology anywhere on the Activity page (FR2), got body: %s", body)
	}
}

// TestActivity_HistoryLinksToSessionDetail guards the session link contract:
// History's config cell is an anchor to /sessions/<id>.
func TestActivity_HistoryLinksToSessionDetail(t *testing.T) {
	body := renderPage(t, Activity(components.LayoutData{Title: "Activity"}, activityFixtureData()))

	if !strings.Contains(body, `href="/sessions/2"`) {
		t.Errorf("expected History's config cell to link to /sessions/2, got body: %s", body)
	}
}

// TestActivity_LiveLinksToSessionDetail guards the session link contract:
// Live's config cell is an anchor to /sessions/<id>.
func TestActivity_LiveLinksToSessionDetail(t *testing.T) {
	body := renderPage(t, Activity(components.LayoutData{Title: "Activity"}, activityFixtureData()))

	if !strings.Contains(body, `href="/sessions/1"`) {
		t.Errorf("expected Live's config cell to link to /sessions/1, got body: %s", body)
	}
}

// TestActivity_GameAndServerLinks guards that Game and Server cells link out
// to the game detail and infrastructure management pages.
func TestActivity_GameAndServerLinks(t *testing.T) {
	body := renderPage(t, Activity(components.LayoutData{Title: "Activity"}, activityFixtureData()))

	if !strings.Contains(body, `href="/games/10"`) {
		t.Errorf("expected Live game cell to link to /games/10, got body: %s", body)
	}
	if !strings.Contains(body, `href="/infrastructure?manage=20#host-manage-20"`) {
		t.Errorf("expected Live server cell to link to /infrastructure?manage=20#host-manage-20, got body: %s", body)
	}
	if !strings.Contains(body, `href="/games/11"`) {
		t.Errorf("expected History game cell to link to /games/11, got body: %s", body)
	}
	if !strings.Contains(body, `href="/infrastructure?manage=21#host-manage-21"`) {
		t.Errorf("expected History server cell to link to /infrastructure?manage=21#host-manage-21, got body: %s", body)
	}
}

// TestActivity_ZeroIDsRenderPlainTextWithoutLinks guards that unknown/zero
// IDs fall back to plain text rather than dead or invalid links.
func TestActivity_ZeroIDsRenderPlainTextWithoutLinks(t *testing.T) {
	now := time.Now()
	data := ActivityPageData{
		Live: []ActivityLiveRow{
			{GameName: "Unknown Game", ServerName: "Unknown Server", ConfigName: "Unknown Config", Uptime: 90 * time.Minute},
		},
		History: []ActivityHistoryRow{
			{GameName: "Unknown Game", ServerName: "Unknown Server", ConfigName: "Unknown Config", TerminalStatus: "stopped", StartTime: now.Add(-2 * time.Hour), Duration: time.Hour},
		},
	}
	body := renderPage(t, Activity(components.LayoutData{Title: "Activity"}, data))

	for _, forbidden := range []string{`href="/games/0"`, `href="/infrastructure?manage=0`, `href="/sessions/0"`} {
		if strings.Contains(body, forbidden) {
			t.Errorf("expected no %q link when entity IDs are 0, got body: %s", forbidden, body)
		}
	}
}
