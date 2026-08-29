package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/whale-net/everything/libs/go/htmxsse"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
)

func newPromotionDetailsTestApp(promo *fakePromotionClient) *App {
	// Create a Hub with a no-op attach function (tests don't need real connections)
	attachFunc := func(ctx context.Context) (htmxsse.Transport, error) {
		return nil, fmt.Errorf("test stub: no transport")
	}
	hub := htmxsse.NewHub(attachFunc, htmxsse.DefaultConfig())
	return &App{registry: &RegistryClient{Promotion: promo}, sseHub: hub}
}

// --- handlePromotionDetails: malformed/missing id --------------------------

// TestHandlePromotionDetails_MissingID_NotFound proves an empty path param
// (never expected from the real "/promotions/{id}" route, but a defensive
// guard against a malformed request) short-circuits to a real HTTP 404
// before any RPC is attempted.
func TestHandlePromotionDetails_MissingID_NotFound(t *testing.T) {
	promo := &fakePromotionClient{}
	app := newPromotionDetailsTestApp(promo)

	req := httptest.NewRequest(http.MethodGet, "/promotions/", nil)
	// Deliberately no SetPathValue("id", ...) -- PathValue("id") returns ""
	// exactly like a route registered without the {id} segment matching.
	w := httptest.NewRecorder()
	app.handlePromotionDetails(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
	if promo.getDetailsCallCount() != 0 {
		t.Errorf("GetPromotionDetails calls = %d, want 0 (must not call the RPC with an empty id)", promo.getDetailsCallCount())
	}
}

// --- handlePromotionDetails: found / not-found ------------------------------

// TestHandlePromotionDetails_Found_RendersPage proves a successful
// GetPromotionDetails renders the page (HTTP 200) with the promotion_id
// echoed and no error banner.
func TestHandlePromotionDetails_Found_RendersPage(t *testing.T) {
	promo := &fakePromotionClient{getDetailsFunc: func(in *pb.GetPromotionDetailsRequest) (*pb.GetPromotionDetailsResponse, error) {
		return &pb.GetPromotionDetailsResponse{Details: &pb.PromotionDetails{
			Promotion:   &pb.Promotion{PromotionId: in.GetPromotionId(), EnvironmentKey: "dev"},
			ToVersion:   "v2.0.0",
			FromVersion: "v1.0.0",
		}}, nil
	}}
	app := newPromotionDetailsTestApp(promo)

	req := httptest.NewRequest(http.MethodGet, "/promotions/promo-1", nil)
	req.SetPathValue("id", "promo-1")
	w := httptest.NewRecorder()
	app.handlePromotionDetails(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "v1.0.0") || !strings.Contains(body, "v2.0.0") {
		t.Errorf("expected both from/to versions rendered; body = %s", body)
	}
	if strings.Contains(body, "alert-error") {
		t.Errorf("a successful load must render no error banner; body = %s", body)
	}
	if promo.getDetailsCallCount() != 1 {
		t.Errorf("GetPromotionDetails calls = %d, want 1", promo.getDetailsCallCount())
	}
	got := promo.getDetailsCalls[0]
	if got.GetPromotionId() != "promo-1" {
		t.Errorf("GetPromotionDetailsRequest.PromotionId = %q, want promo-1", got.GetPromotionId())
	}
}

// TestHandlePromotionDetails_UnknownID_RendersErrorBanner proves an
// RPC-level NotFound (unknown promotion_id) does not crash the handler --
// it renders the page with an error banner, mirroring handleReleaseStatus's
// LoadErr precedent (handlers_release.go) rather than a raw HTTP error.
func TestHandlePromotionDetails_UnknownID_RendersErrorBanner(t *testing.T) {
	promo := &fakePromotionClient{getDetailsFunc: func(in *pb.GetPromotionDetailsRequest) (*pb.GetPromotionDetailsResponse, error) {
		return nil, status.Error(codes.NotFound, "promotion nonexistent not found")
	}}
	app := newPromotionDetailsTestApp(promo)

	req := httptest.NewRequest(http.MethodGet, "/promotions/nonexistent", nil)
	req.SetPathValue("id", "nonexistent")
	w := httptest.NewRecorder()
	app.handlePromotionDetails(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (errors render inline, not as an HTTP error status)", w.Code, http.StatusOK)
	}
	body := w.Body.String()
	if !strings.Contains(body, "alert-error") {
		t.Errorf("expected an error banner for an unknown promotion_id; body = %s", body)
	}
	if !strings.Contains(body, "promotion nonexistent not found") {
		t.Errorf("expected the server's NotFound message rendered; body = %s", body)
	}
}

// --- FR7a: writeback commit link vs "not yet committed" --------------------

// TestHandlePromotionDetails_CommitSHA_RendersArgok8sLink proves a non-empty
// writeback_commit_sha renders a link to the argok8s commit.
func TestHandlePromotionDetails_CommitSHA_RendersArgok8sLink(t *testing.T) {
	promo := &fakePromotionClient{getDetailsFunc: func(in *pb.GetPromotionDetailsRequest) (*pb.GetPromotionDetailsResponse, error) {
		return &pb.GetPromotionDetailsResponse{Details: &pb.PromotionDetails{
			Promotion:          &pb.Promotion{PromotionId: in.GetPromotionId()},
			WritebackCommitSha: "deadbeefcafe",
		}}, nil
	}}
	app := newPromotionDetailsTestApp(promo)

	req := httptest.NewRequest(http.MethodGet, "/promotions/promo-1", nil)
	req.SetPathValue("id", "promo-1")
	w := httptest.NewRecorder()
	app.handlePromotionDetails(w, req)

	body := w.Body.String()
	if !strings.Contains(body, `href="https://github.com/whale-net/argok8s/commit/deadbeefcafe"`) {
		t.Errorf("expected a link to the argok8s commit; body = %s", body)
	}
	if strings.Contains(body, "Not yet committed") {
		t.Errorf("a real commit SHA must never also render the not-yet-committed fallback; body = %s", body)
	}
}

// TestHandlePromotionDetails_NoCommitSHA_RendersNotYetCommitted_NoBrokenLink
// proves FR7a's "never a broken link" contract: an empty
// writeback_commit_sha renders explanatory text, not an <a href> pointing
// at an empty/undefined commit.
func TestHandlePromotionDetails_NoCommitSHA_RendersNotYetCommitted_NoBrokenLink(t *testing.T) {
	promo := &fakePromotionClient{getDetailsFunc: func(in *pb.GetPromotionDetailsRequest) (*pb.GetPromotionDetailsResponse, error) {
		return &pb.GetPromotionDetailsResponse{Details: &pb.PromotionDetails{
			Promotion: &pb.Promotion{PromotionId: in.GetPromotionId()},
		}}, nil
	}}
	app := newPromotionDetailsTestApp(promo)

	req := httptest.NewRequest(http.MethodGet, "/promotions/promo-1", nil)
	req.SetPathValue("id", "promo-1")
	w := httptest.NewRecorder()
	app.handlePromotionDetails(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "Not yet committed") {
		t.Errorf("expected the not-yet-committed fallback text; body = %s", body)
	}
	if strings.Contains(body, "https://github.com/whale-net/argok8s/commit/") {
		t.Errorf("must never render an argok8s commit link with no real SHA (FR7a); body = %s", body)
	}
}

// TestHandlePromotionDetails_StubPathLocation_RendersDistinctText proves the
// dev-test-stub-path sub-case (a writeback_location was written, e.g. by
// StubActivities.Publish, but no real git commit ever happened) renders
// distinct explanatory text naming where it was written -- not the same
// bare "Not yet committed" as the never-published case, and still no
// broken commit link.
func TestHandlePromotionDetails_StubPathLocation_RendersDistinctText(t *testing.T) {
	promo := &fakePromotionClient{getDetailsFunc: func(in *pb.GetPromotionDetailsRequest) (*pb.GetPromotionDetailsResponse, error) {
		return &pb.GetPromotionDetailsResponse{Details: &pb.PromotionDetails{
			Promotion:         &pb.Promotion{PromotionId: in.GetPromotionId()},
			WritebackLocation: "/tmp/app-registry-writeback/dev.json",
		}}, nil
	}}
	app := newPromotionDetailsTestApp(promo)

	req := httptest.NewRequest(http.MethodGet, "/promotions/promo-1", nil)
	req.SetPathValue("id", "promo-1")
	w := httptest.NewRecorder()
	app.handlePromotionDetails(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "dev-test stub path") {
		t.Errorf("expected the stub-path explanation naming it as such; body = %s", body)
	}
	if !strings.Contains(body, "/tmp/app-registry-writeback/dev.json") {
		t.Errorf("expected the stub write location named; body = %s", body)
	}
	if strings.Contains(body, "https://github.com/whale-net/argok8s/commit/") {
		t.Errorf("must never render an argok8s commit link with no real SHA (FR7a); body = %s", body)
	}
}

// --- FR8: three visibly distinct outcome states -----------------------------

func TestHandlePromotionDetails_Outcome_SyncedHealthy(t *testing.T) {
	promo := &fakePromotionClient{getDetailsFunc: func(in *pb.GetPromotionDetailsRequest) (*pb.GetPromotionDetailsResponse, error) {
		return &pb.GetPromotionDetailsResponse{Details: &pb.PromotionDetails{
			Promotion:           &pb.Promotion{PromotionId: in.GetPromotionId()},
			Outcome:             pb.PromotionSyncOutcome_PROMOTION_SYNC_OUTCOME_SYNCED_HEALTHY,
			CurrentSyncStatus:   "Synced",
			CurrentHealthStatus: "Healthy",
		}}, nil
	}}
	app := newPromotionDetailsTestApp(promo)

	req := httptest.NewRequest(http.MethodGet, "/promotions/promo-1", nil)
	req.SetPathValue("id", "promo-1")
	w := httptest.NewRecorder()
	app.handlePromotionDetails(w, req)

	body := w.Body.String()
	if !strings.Contains(body, ">Synced<") {
		t.Errorf("expected the Synced outcome badge; body = %s", body)
	}
	if strings.Contains(body, "Sync failed:") {
		t.Errorf("a synced/healthy outcome must never also render the sync-failed reason banner; body = %s", body)
	}
}

func TestHandlePromotionDetails_Outcome_SyncFailed_ReasonVisible(t *testing.T) {
	promo := &fakePromotionClient{getDetailsFunc: func(in *pb.GetPromotionDetailsRequest) (*pb.GetPromotionDetailsResponse, error) {
		return &pb.GetPromotionDetailsResponse{Details: &pb.PromotionDetails{
			Promotion:           &pb.Promotion{PromotionId: in.GetPromotionId()},
			Outcome:             pb.PromotionSyncOutcome_PROMOTION_SYNC_OUTCOME_SYNC_FAILED,
			CurrentSyncStatus:   "OutOfSync",
			CurrentHealthStatus: "Degraded",
		}}, nil
	}}
	app := newPromotionDetailsTestApp(promo)

	req := httptest.NewRequest(http.MethodGet, "/promotions/promo-1", nil)
	req.SetPathValue("id", "promo-1")
	w := httptest.NewRecorder()
	app.handlePromotionDetails(w, req)

	body := w.Body.String()
	if !strings.Contains(body, ">Sync failed<") {
		t.Errorf("expected the Sync failed outcome badge; body = %s", body)
	}
	// FR8: the raw ArgoCD strings ARE the reason -- no separate free-text
	// field, so they must be visible verbatim alongside the failure state.
	if !strings.Contains(body, "Sync failed: OutOfSync / Degraded") {
		t.Errorf("expected the raw sync/health strings rendered as the failure reason; body = %s", body)
	}
}

func TestHandlePromotionDetails_Outcome_Pending(t *testing.T) {
	promo := &fakePromotionClient{getDetailsFunc: func(in *pb.GetPromotionDetailsRequest) (*pb.GetPromotionDetailsResponse, error) {
		return &pb.GetPromotionDetailsResponse{Details: &pb.PromotionDetails{
			Promotion: &pb.Promotion{PromotionId: in.GetPromotionId()},
			Outcome:   pb.PromotionSyncOutcome_PROMOTION_SYNC_OUTCOME_PENDING,
		}}, nil
	}}
	app := newPromotionDetailsTestApp(promo)

	req := httptest.NewRequest(http.MethodGet, "/promotions/promo-1", nil)
	req.SetPathValue("id", "promo-1")
	w := httptest.NewRecorder()
	app.handlePromotionDetails(w, req)

	body := w.Body.String()
	if !strings.Contains(body, ">Pending<") {
		t.Errorf("expected the Pending outcome badge; body = %s", body)
	}
	if strings.Contains(body, ">Synced<") || strings.Contains(body, ">Sync failed<") {
		t.Errorf("a pending outcome must render only the Pending badge, never Synced or Sync failed; body = %s", body)
	}
}

// TestHandlePromotionDetails_ThreeOutcomes_AreVisiblyDistinct proves FR8's
// core requirement directly: the three real outcome states never share the
// same badge text.
func TestHandlePromotionDetails_ThreeOutcomes_AreVisiblyDistinct(t *testing.T) {
	labels := map[pb.PromotionSyncOutcome]string{
		pb.PromotionSyncOutcome_PROMOTION_SYNC_OUTCOME_SYNCED_HEALTHY: "",
		pb.PromotionSyncOutcome_PROMOTION_SYNC_OUTCOME_SYNC_FAILED:    "",
		pb.PromotionSyncOutcome_PROMOTION_SYNC_OUTCOME_PENDING:        "",
	}
	seen := map[string]bool{}
	for outcome := range labels {
		promo := &fakePromotionClient{getDetailsFunc: func(in *pb.GetPromotionDetailsRequest) (*pb.GetPromotionDetailsResponse, error) {
			return &pb.GetPromotionDetailsResponse{Details: &pb.PromotionDetails{
				Promotion: &pb.Promotion{PromotionId: in.GetPromotionId()},
				Outcome:   outcome,
			}}, nil
		}}
		app := newPromotionDetailsTestApp(promo)
		req := httptest.NewRequest(http.MethodGet, "/promotions/promo-1", nil)
		req.SetPathValue("id", "promo-1")
		w := httptest.NewRecorder()
		app.handlePromotionDetails(w, req)

		badge := extractBadgeText(w.Body.String())
		if badge == "" {
			t.Fatalf("outcome %v: no badge text found in body: %s", outcome, w.Body.String())
		}
		if seen[badge] {
			t.Errorf("outcome %v: badge text %q was already used by a different outcome -- FR8 requires three visibly distinct states", outcome, badge)
		}
		seen[badge] = true
	}
	if len(seen) != 3 {
		t.Errorf("expected 3 distinct badge labels, got %d: %v", len(seen), seen)
	}
}

// extractBadgeText pulls the first "badge" span's text out of a rendered
// promotion-details page body -- good enough for this test's purpose
// (asserting distinctness across three known-shape renders), not a general
// HTML parser.
func extractBadgeText(body string) string {
	const marker = `<span class="badge`
	i := strings.Index(body, marker)
	if i < 0 {
		return ""
	}
	rest := body[i:]
	open := strings.Index(rest, ">")
	if open < 0 {
		return ""
	}
	rest = rest[open+1:]
	end := strings.Index(rest, "<")
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:end])
}
