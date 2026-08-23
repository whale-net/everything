package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
)

// TestHandleRetryArgoSync_MissingID_NotFound mirrors
// TestHandlePromotionDetails_MissingID_NotFound's guard: an empty {id} path
// param short-circuits to a real HTTP 404 before either RPC is attempted.
func TestHandleRetryArgoSync_MissingID_NotFound(t *testing.T) {
	promo := &fakePromotionClient{}
	app := newPromotionDetailsTestApp(promo)

	req := httptest.NewRequest(http.MethodPost, "/promotions//retry", nil)
	// Deliberately no SetPathValue("id", ...) -- see the identical guard in
	// handlers_promotion_details_test.go.
	w := httptest.NewRecorder()
	app.handleRetryArgoSync(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
	if len(promo.retryArgoSyncCalls) != 0 {
		t.Errorf("RetryArgoSync calls = %d, want 0 (must not call the RPC with an empty id)", len(promo.retryArgoSyncCalls))
	}
	if promo.getDetailsCallCount() != 0 {
		t.Errorf("GetPromotionDetails calls = %d, want 0", promo.getDetailsCallCount())
	}
}

// TestHandleRetryArgoSync_Success_CallsRPCThenRerendersDetails proves the
// FR12 UI wiring: a successful RetryArgoSync call is followed by a
// GetPromotionDetails re-read of the SAME promotion_id, rendered with no
// RetryErr banner -- never a redirect, so a caller sees the freshly-written
// retry_triggered/retry_observed rows immediately.
func TestHandleRetryArgoSync_Success_CallsRPCThenRerendersDetails(t *testing.T) {
	promo := &fakePromotionClient{}
	app := newPromotionDetailsTestApp(promo)

	req := httptest.NewRequest(http.MethodPost, "/promotions/promo-1/retry", nil)
	req.SetPathValue("id", "promo-1")
	w := httptest.NewRecorder()
	app.handleRetryArgoSync(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if len(promo.retryArgoSyncCalls) != 1 {
		t.Fatalf("RetryArgoSync calls = %d, want 1", len(promo.retryArgoSyncCalls))
	}
	if got := promo.retryArgoSyncCalls[0].GetPromotionId(); got != "promo-1" {
		t.Errorf("RetryArgoSyncRequest.PromotionId = %q, want promo-1", got)
	}
	if promo.getDetailsCallCount() != 1 {
		t.Fatalf("GetPromotionDetails calls = %d, want 1 (re-render after a successful retry)", promo.getDetailsCallCount())
	}
	if got := promo.getDetailsCalls[0].GetPromotionId(); got != "promo-1" {
		t.Errorf("GetPromotionDetailsRequest.PromotionId = %q, want promo-1", got)
	}
	body := w.Body.String()
	if strings.Contains(body, "Retry failed:") {
		t.Errorf("a successful retry must render no RetryErr banner; body = %s", body)
	}
}

// TestHandleRetryArgoSync_RPCFailure_RendersRetryErrBanner proves a failed
// RetryArgoSync call (e.g. server-side PermissionDenied for a non-admin
// session that bypassed the disabled UI control, or a genuine Temporal
// failure) still re-renders the details page -- HTTP 200, not a raw error
// status -- with the failure surfaced inline via RetryErr, distinct from
// LoadErr.
func TestHandleRetryArgoSync_RPCFailure_RendersRetryErrBanner(t *testing.T) {
	promo := &fakePromotionClient{retryArgoSyncErr: errors.New("rpc error: code = PermissionDenied desc = missing role app-registry-admin")}
	app := newPromotionDetailsTestApp(promo)

	req := httptest.NewRequest(http.MethodPost, "/promotions/promo-1/retry", nil)
	req.SetPathValue("id", "promo-1")
	w := httptest.NewRecorder()
	app.handleRetryArgoSync(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (a failed retry renders inline, not as an HTTP error status)", w.Code, http.StatusOK)
	}
	if promo.getDetailsCallCount() != 1 {
		t.Fatalf("GetPromotionDetails calls = %d, want 1 (still re-renders the page after a failed retry)", promo.getDetailsCallCount())
	}
	body := w.Body.String()
	if !strings.Contains(body, "alert-error") || !strings.Contains(body, "Retry failed:") {
		t.Errorf("expected a RetryErr banner; body = %s", body)
	}
	if !strings.Contains(body, "PermissionDenied") {
		t.Errorf("expected the underlying RPC error surfaced; body = %s", body)
	}
}

// TestHandleRetryArgoSync_RPCFailure_GetPromotionDetailsAlsoFails_RendersLoadErr
// proves the LoadErr path still wins when BOTH calls fail: LoadErr is set
// (matching handlePromotionDetails' own failure rendering), never a
// RetryErr banner over a page whose Details is nil.
func TestHandleRetryArgoSync_RPCFailure_GetPromotionDetailsAlsoFails_RendersLoadErr(t *testing.T) {
	promo := &fakePromotionClient{
		retryArgoSyncErr: errors.New("permission denied"),
		getDetailsFunc: func(in *pb.GetPromotionDetailsRequest) (*pb.GetPromotionDetailsResponse, error) {
			return nil, errors.New("promotion vanished")
		},
	}
	app := newPromotionDetailsTestApp(promo)

	req := httptest.NewRequest(http.MethodPost, "/promotions/promo-1/retry", nil)
	req.SetPathValue("id", "promo-1")
	w := httptest.NewRecorder()
	app.handleRetryArgoSync(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "promotion vanished") {
		t.Errorf("expected the GetPromotionDetails failure (LoadErr) rendered; body = %s", body)
	}
	if strings.Contains(body, "Retry failed:") {
		t.Errorf("RetryErr must not render when Details itself failed to load; body = %s", body)
	}
}
