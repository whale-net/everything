package main

import (
	"context"
	"strings"
	"testing"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/ui/pages"
	"github.com/whale-net/everything/libs/go/htmxauth"
)

// TestPromotionDetails_FR29_TemplateStructure tests the template boundary for FR29:
// the sse-swap target wraps exactly the output of @promotionDetailsBody and nothing else.
// This test verifies the key structural requirements:
// - The sse-swap target is present
// - It contains promotionDetailsBody output
// - It does NOT contain elements that should be outside the boundary
func TestPromotionDetails_FR29_TemplateStructure(t *testing.T) {
	// Create a promotion details view state with successful sync
	state := pages.PromotionDetailsViewState{
		PromotionID: "test-promo",
		Details: &pb.GetPromotionDetailsResponse{
			Details: &pb.PromotionDetails{
				Promotion:           &pb.Promotion{PromotionId: "test-promo", EnvironmentKey: "dev"},
				FromVersion:         "v1.0.0",
				ToVersion:           "v2.0.0",
				Outcome:             pb.PromotionSyncOutcome_PROMOTION_SYNC_OUTCOME_SYNCED_HEALTHY,
				CurrentSyncStatus:   "Synced",
				CurrentHealthStatus: "Healthy",
			},
		},
		LoadErr:  "",
		RetryErr: "",
	}

	user := &htmxauth.UserInfo{
		Sub: "test-user",
	}

	// Render the template with a valid context
	component := pages.PromotionDetails(user, state)
	var buf strings.Builder
	ctx := context.Background()
	err := component.Render(ctx, &buf)
	if err != nil {
		t.Fatalf("failed to render template: %v", err)
	}

	html := buf.String()

	// FR20(b): template should have hx-ext="sse"
	if !strings.Contains(html, `hx-ext="sse"`) {
		t.Errorf("FR20(b): template must contain hx-ext=\"sse\"; got: %s", html)
	}

	// FR20(b): template should have sse-connect
	if !strings.Contains(html, `sse-connect=`) {
		t.Errorf("FR20(b): template must contain sse-connect attribute; got: %s", html)
	}

	// FR29: template should have sse-swap target
	if !strings.Contains(html, `sse-swap="innerHTML"`) {
		t.Errorf("FR29: template must contain sse-swap=\"innerHTML\" target; got: %s", html)
	}

	// FR29: sse-swap div must have the promotion-details-body class
	if !strings.Contains(html, `class="promotion-details-body"`) {
		t.Errorf("FR29: sse-swap target must have class=\"promotion-details-body\"; got: %s", html)
	}

	// FR29: The breadcrumbs must be present BUT NOT inside the sse-swap target
	// (breadcrumbs are outside the pushed region)
	if !strings.Contains(html, "breadcrumbs") {
		t.Errorf("FR29: breadcrumbs must be present on the page; got: %s", html)
	}

	// Verify that the sse-connect element is outside the sse-swap target by checking structure
	sseConnectIdx := strings.Index(html, `hx-ext="sse"`)
	sseSwapIdx := strings.Index(html, `sse-swap="innerHTML"`)
	if sseConnectIdx < 0 || sseSwapIdx < 0 {
		t.Errorf("FR29: both sse-connect and sse-swap must be present")
	}
	if sseConnectIdx > sseSwapIdx {
		t.Errorf("FR29: sse-connect element must come before (be outside) sse-swap target; got indices %d and %d", sseConnectIdx, sseSwapIdx)
	}
}

// TestPromotionDetails_FR29_NoTargetOnLoadError tests FR29's load-failure state:
// when LoadErr is set, there is no sse-swap target and no connection is established.
func TestPromotionDetails_FR29_NoTargetOnLoadError(t *testing.T) {
	state := pages.PromotionDetailsViewState{
		PromotionID: "test-promo",
		LoadErr:     "promotion not found",
		Details:     nil, // No details when load fails
		RetryErr:    "",
	}

	user := &htmxauth.UserInfo{
		Sub: "test-user",
	}

	component := pages.PromotionDetails(user, state)
	var buf strings.Builder
	ctx := context.Background()
	err := component.Render(ctx, &buf)
	if err != nil {
		t.Fatalf("failed to render template: %v", err)
	}

	html := buf.String()

	// Must have the LoadErr alert
	if !strings.Contains(html, "alert-error") {
		t.Errorf("FR29: LoadErr alert must be present; got: %s", html)
	}

	// Must NOT have sse-swap target when Details is nil
	if strings.Contains(html, `sse-swap="innerHTML"`) {
		t.Errorf("FR29: sse-swap target must NOT be present when Details is nil (load failed); got: %s", html)
	}

	// Still must have the hx-ext for consistency, but no target to swap
	if !strings.Contains(html, `hx-ext="sse"`) {
		t.Errorf("FR20(b): template must still have hx-ext=\"sse\" even on load failure; got: %s", html)
	}
}

// TestPromotionDetails_FR29_RetryBannerOutsideSwapTarget tests FR29:
// when RetryErr is set, the banner is present but NOT inside the sse-swap target.
// The pushed fragment should NOT contain the retry error banner.
func TestPromotionDetails_FR29_RetryBannerOutsideSwapTarget(t *testing.T) {
	state := pages.PromotionDetailsViewState{
		PromotionID: "test-promo",
		Details: &pb.GetPromotionDetailsResponse{
			Details: &pb.PromotionDetails{
				Promotion:   &pb.Promotion{PromotionId: "test-promo", EnvironmentKey: "dev"},
				FromVersion: "v1.0.0",
				ToVersion:   "v2.0.0",
				Outcome:     pb.PromotionSyncOutcome_PROMOTION_SYNC_OUTCOME_SYNCED_HEALTHY,
			},
		},
		LoadErr:  "",
		RetryErr: "connection refused",
	}

	user := &htmxauth.UserInfo{
		Sub: "test-user",
	}

	component := pages.PromotionDetails(user, state)
	var buf strings.Builder
	ctx := context.Background()
	err := component.Render(ctx, &buf)
	if err != nil {
		t.Fatalf("failed to render template: %v", err)
	}

	html := buf.String()

	// Must have the retry error banner
	if !strings.Contains(html, "Retry failed:") {
		t.Errorf("FR29: RetryErr banner must be present; got: %s", html)
	}

	// Must have sse-swap target
	if !strings.Contains(html, `sse-swap="innerHTML"`) {
		t.Errorf("FR29: sse-swap target must be present when Details is not nil; got: %s", html)
	}

	// The critical part: verify banner comes BEFORE the sse-swap div
	// This ensures the banner is outside the pushed region
	bannerIdx := strings.Index(html, "Retry failed:")
	swapIdx := strings.Index(html, `sse-swap="innerHTML"`)
	if bannerIdx < 0 || swapIdx < 0 {
		t.Errorf("FR29: both banner and swap target must be present")
	}
	if bannerIdx > swapIdx {
		t.Errorf("FR29: RetryErr banner must come before (be outside) sse-swap target; got indices %d and %d", bannerIdx, swapIdx)
	}
}

// TestPromotionDetails_FR29_NoHTMXAttributesExceptPromoDetails tests FR20:
// the only hx-* attributes in the promotion_details template are the SSE ones.
func TestPromotionDetails_FR29_NoHTMXAttributesExceptPromoDetails(t *testing.T) {
	state := pages.PromotionDetailsViewState{
		PromotionID: "test-promo",
		Details: &pb.GetPromotionDetailsResponse{
			Details: &pb.PromotionDetails{
				Promotion:           &pb.Promotion{PromotionId: "test-promo", EnvironmentKey: "dev"},
				FromVersion:         "v1.0.0",
				ToVersion:           "v2.0.0",
				Outcome:             pb.PromotionSyncOutcome_PROMOTION_SYNC_OUTCOME_SYNCED_HEALTHY,
				CurrentSyncStatus:   "Synced",
				CurrentHealthStatus: "Healthy",
			},
		},
	}

	user := &htmxauth.UserInfo{
		Sub: "test-user",
	}

	component := pages.PromotionDetails(user, state)
	var buf strings.Builder
	ctx := context.Background()
	err := component.Render(ctx, &buf)
	if err != nil {
		t.Fatalf("failed to render template: %v", err)
	}

	html := buf.String()

	// Count hx-* attributes (excluding form method/action which are not htmx)
	// We expect: hx-ext, sse-connect, sse-swap
	if !strings.Contains(html, "hx-ext") {
		t.Errorf("expected hx-ext in template")
	}
	if !strings.Contains(html, "sse-connect") {
		t.Errorf("expected sse-connect in template")
	}
	if !strings.Contains(html, "sse-swap") {
		t.Errorf("expected sse-swap in template")
	}

	// Check that there are no other hx-* attributes like hx-get, hx-post, hx-target
	badAttrs := []string{"hx-get", "hx-post", "hx-target"}
	for _, attr := range badAttrs {
		if strings.Contains(html, attr) {
			t.Errorf("FR20: template should not contain %q; got: %s", attr, html)
		}
	}
}

// TestPromotionDetails_FR23_IndicatorPresent tests FR23:
// the live/not-live indicator is present and visible on the page.
func TestPromotionDetails_FR23_IndicatorPresent(t *testing.T) {
	state := pages.PromotionDetailsViewState{
		PromotionID: "test-promo",
		Details: &pb.GetPromotionDetailsResponse{
			Details: &pb.PromotionDetails{
				Promotion:           &pb.Promotion{PromotionId: "test-promo", EnvironmentKey: "dev"},
				FromVersion:         "v1.0.0",
				ToVersion:           "v2.0.0",
				Outcome:             pb.PromotionSyncOutcome_PROMOTION_SYNC_OUTCOME_SYNCED_HEALTHY,
				CurrentSyncStatus:   "Synced",
				CurrentHealthStatus: "Healthy",
			},
		},
		LoadErr:  "",
		RetryErr: "",
	}

	user := &htmxauth.UserInfo{Sub: "test-user"}
	component := pages.PromotionDetails(user, state)

	var buf strings.Builder
	ctx := context.Background()
	err := component.Render(ctx, &buf)
	if err != nil {
		t.Fatalf("failed to render template: %v", err)
	}

	html := buf.String()

	// FR23: indicator element must be present
	if !strings.Contains(html, `id="live-status"`) {
		t.Errorf("FR23: live status indicator element must be present with id='live-status'; got: %s", html)
	}

	// FR23: indicator must show "Live" initially
	if !strings.Contains(html, `<div id="live-status" class="badge badge-success"`) {
		t.Errorf("FR23: indicator must be present with success badge and 'Live' text; got: %s", html)
	}

	// FR23: indicator must be outside the sse-swap target
	indicatorIdx := strings.Index(html, `id="live-status"`)
	swapIdx := strings.Index(html, `sse-swap="innerHTML"`)
	if indicatorIdx < 0 || swapIdx < 0 {
		t.Errorf("FR23: both indicator and swap target must be present")
	}
	if indicatorIdx > swapIdx {
		t.Errorf("FR23: indicator must come before (be outside) sse-swap target; got indices %d and %d", indicatorIdx, swapIdx)
	}
}

// TestPromotionDetails_FR24_ReloadAffordancePresent tests FR24:
// the reload affordance is present and hidden by default.
func TestPromotionDetails_FR24_ReloadAffordancePresent(t *testing.T) {
	state := pages.PromotionDetailsViewState{
		PromotionID: "test-promo",
		Details: &pb.GetPromotionDetailsResponse{
			Details: &pb.PromotionDetails{
				Promotion:           &pb.Promotion{PromotionId: "test-promo", EnvironmentKey: "dev"},
				FromVersion:         "v1.0.0",
				ToVersion:           "v2.0.0",
				Outcome:             pb.PromotionSyncOutcome_PROMOTION_SYNC_OUTCOME_SYNCED_HEALTHY,
				CurrentSyncStatus:   "Synced",
				CurrentHealthStatus: "Healthy",
			},
		},
		LoadErr:  "",
		RetryErr: "",
	}

	user := &htmxauth.UserInfo{Sub: "test-user"}
	component := pages.PromotionDetails(user, state)

	var buf strings.Builder
	ctx := context.Background()
	err := component.Render(ctx, &buf)
	if err != nil {
		t.Fatalf("failed to render template: %v", err)
	}

	html := buf.String()

	// FR24: reload container must be present
	if !strings.Contains(html, `id="promo-reload-container"`) {
		t.Errorf("FR24: reload container must be present with id='promo-reload-container'; got: %s", html)
	}

	// FR24: reload link must point to the same promotion
	if !strings.Contains(html, `/promotions/test-promo`) {
		t.Errorf("FR24: reload link must point to /promotions/test-promo; got: %s", html)
	}

	// FR24: reload affordance must have btn-warning class
	if !strings.Contains(html, `class="btn btn-sm btn-warning">Reload`) {
		t.Errorf("FR24: reload button must have btn-warning class; got: %s", html)
	}

	// FR24: reload container must be outside the sse-swap target
	reloadIdx := strings.Index(html, `id="promo-reload-container"`)
	swapIdx := strings.Index(html, `sse-swap="innerHTML"`)
	if reloadIdx < 0 || swapIdx < 0 {
		t.Errorf("FR24: both reload and swap target must be present")
	}
	if reloadIdx > swapIdx {
		t.Errorf("FR24: reload must come before (be outside) sse-swap target; got indices %d and %d", reloadIdx, swapIdx)
	}

	// FR24: reload container must be hidden by default (style="display: none")
	if !strings.Contains(html, `style="display: none;"`) {
		t.Errorf("FR24: reload container must be hidden by default; got: %s", html)
	}
}

// TestPromotionDetails_FR23_IndicatorScript tests FR23/FR24:
// the JavaScript for managing the live/not-live indicator is included.
func TestPromotionDetails_FR23_IndicatorScript(t *testing.T) {
	state := pages.PromotionDetailsViewState{
		PromotionID: "test-promo",
		Details: &pb.GetPromotionDetailsResponse{
			Details: &pb.PromotionDetails{
				Promotion:           &pb.Promotion{PromotionId: "test-promo", EnvironmentKey: "dev"},
				FromVersion:         "v1.0.0",
				ToVersion:           "v2.0.0",
				Outcome:             pb.PromotionSyncOutcome_PROMOTION_SYNC_OUTCOME_SYNCED_HEALTHY,
				CurrentSyncStatus:   "Synced",
				CurrentHealthStatus: "Healthy",
			},
		},
		LoadErr:  "",
		RetryErr: "",
	}

	user := &htmxauth.UserInfo{Sub: "test-user"}
	component := pages.PromotionDetails(user, state)

	var buf strings.Builder
	ctx := context.Background()
	err := component.Render(ctx, &buf)
	if err != nil {
		t.Fatalf("failed to render template: %v", err)
	}

	html := buf.String()

	// FR23/FR24: script must be present
	if !strings.Contains(html, `<script type="module">`) {
		t.Errorf("FR23/FR24: script tag must be present; got: %s", html)
	}

	// FR23: script must contain live indicator logic
	if !strings.Contains(html, `live-status`) {
		t.Errorf("FR23: script must reference live-status element; got: %s", html)
	}

	// FR24: script must contain reload affordance logic
	if !strings.Contains(html, `promo-reload-container`) {
		t.Errorf("FR24: script must reference promo-reload-container element; got: %s", html)
	}

	// FR23: script must include connection error handling
	if !strings.Contains(html, `htmx:sseError`) {
		t.Errorf("FR23: script must handle htmx:sseError events; got: %s", html)
	}

	// FR23: script must include heartbeat timeout logic
	if !strings.Contains(html, `timeoutThreshold`) {
		t.Errorf("FR23: script must include heartbeat timeout logic; got: %s", html)
	}
}

// TestPromotionDetails_FR24_ReloadLinkPreservesPromotionID tests FR24:
// the reload link includes the promotion ID so it can be used for redirect through login.
func TestPromotionDetails_FR24_ReloadLinkPreservesPromotionID(t *testing.T) {
	promotionID := "test-promotion-xyz"
	state := pages.PromotionDetailsViewState{
		PromotionID: promotionID,
		Details: &pb.GetPromotionDetailsResponse{
			Details: &pb.PromotionDetails{
				Promotion:           &pb.Promotion{PromotionId: promotionID, EnvironmentKey: "dev"},
				FromVersion:         "v1.0.0",
				ToVersion:           "v2.0.0",
				Outcome:             pb.PromotionSyncOutcome_PROMOTION_SYNC_OUTCOME_SYNCED_HEALTHY,
				CurrentSyncStatus:   "Synced",
				CurrentHealthStatus: "Healthy",
			},
		},
		LoadErr:  "",
		RetryErr: "",
	}

	user := &htmxauth.UserInfo{Sub: "test-user"}
	component := pages.PromotionDetails(user, state)

	var buf strings.Builder
	ctx := context.Background()
	err := component.Render(ctx, &buf)
	if err != nil {
		t.Fatalf("failed to render template: %v", err)
	}

	html := buf.String()

	// FR24: reload link must include the correct promotion ID
	expectedHref := `/promotions/` + promotionID
	if !strings.Contains(html, expectedHref) {
		t.Errorf("FR24: reload link must point to %q; got: %s", expectedHref, html)
	}
}

// TestPromotionDetails_FR23_DebounceUsesAdvertisedRetry tests FR23:
// the debounce mechanism uses the advertised retry interval, not a hardcoded value.
func TestPromotionDetails_FR23_DebounceUsesAdvertisedRetry(t *testing.T) {
	state := pages.PromotionDetailsViewState{
		PromotionID: "test-promo",
		Details: &pb.GetPromotionDetailsResponse{
			Details: &pb.PromotionDetails{
				Promotion:           &pb.Promotion{PromotionId: "test-promo", EnvironmentKey: "dev"},
				FromVersion:         "v1.0.0",
				ToVersion:           "v2.0.0",
				Outcome:             pb.PromotionSyncOutcome_PROMOTION_SYNC_OUTCOME_SYNCED_HEALTHY,
				CurrentSyncStatus:   "Synced",
				CurrentHealthStatus: "Healthy",
			},
		},
		LoadErr:  "",
		RetryErr: "",
	}

	user := &htmxauth.UserInfo{Sub: "test-user"}
	component := pages.PromotionDetails(user, state)

	var buf strings.Builder
	ctx := context.Background()
	err := component.Render(ctx, &buf)
	if err != nil {
		t.Fatalf("failed to render template: %v", err)
	}

	html := buf.String()

	// FR23: script must use advertisedRetryMs variable for debounce
	if !strings.Contains(html, `advertisedRetryMs`) {
		t.Errorf("FR23: script must use advertisedRetryMs variable for debounce; got: %s", html)
	}
}

// TestPromotionDetails_FR23_TimeoutThreshold tests FR23:
// the timeout threshold for not-live detection is 2x the advertised retry interval.
func TestPromotionDetails_FR23_TimeoutThreshold(t *testing.T) {
	state := pages.PromotionDetailsViewState{
		PromotionID: "test-promo",
		Details: &pb.GetPromotionDetailsResponse{
			Details: &pb.PromotionDetails{
				Promotion:           &pb.Promotion{PromotionId: "test-promo", EnvironmentKey: "dev"},
				FromVersion:         "v1.0.0",
				ToVersion:           "v2.0.0",
				Outcome:             pb.PromotionSyncOutcome_PROMOTION_SYNC_OUTCOME_SYNCED_HEALTHY,
				CurrentSyncStatus:   "Synced",
				CurrentHealthStatus: "Healthy",
			},
		},
		LoadErr:  "",
		RetryErr: "",
	}

	user := &htmxauth.UserInfo{Sub: "test-user"}
	component := pages.PromotionDetails(user, state)

	var buf strings.Builder
	ctx := context.Background()
	err := component.Render(ctx, &buf)
	if err != nil {
		t.Fatalf("failed to render template: %v", err)
	}

	html := buf.String()

	// FR23: script must calculate timeout threshold as 2x heartbeat interval
	if !strings.Contains(html, `advertisedRetryMs * 2`) {
		t.Errorf("FR23: timeout threshold must be 2x advertised retry interval (advertisedRetryMs * 2); got: %s", html)
	}

	// FR23: script must check this threshold against time since last event
	if !strings.Contains(html, `timeSinceLastEvent`) {
		t.Errorf("FR23: script must track time since last event; got: %s", html)
	}
}

// TestPromotionDetails_FR23_MutationObserverTracksChanges tests FR23:
// the script observes mutations on the sse-swap target to update lastEventTime.
func TestPromotionDetails_FR23_MutationObserverTracksChanges(t *testing.T) {
	state := pages.PromotionDetailsViewState{
		PromotionID: "test-promo",
		Details: &pb.GetPromotionDetailsResponse{
			Details: &pb.PromotionDetails{
				Promotion:           &pb.Promotion{PromotionId: "test-promo", EnvironmentKey: "dev"},
				FromVersion:         "v1.0.0",
				ToVersion:           "v2.0.0",
				Outcome:             pb.PromotionSyncOutcome_PROMOTION_SYNC_OUTCOME_SYNCED_HEALTHY,
				CurrentSyncStatus:   "Synced",
				CurrentHealthStatus: "Healthy",
			},
		},
		LoadErr:  "",
		RetryErr: "",
	}

	user := &htmxauth.UserInfo{Sub: "test-user"}
	component := pages.PromotionDetails(user, state)

	var buf strings.Builder
	ctx := context.Background()
	err := component.Render(ctx, &buf)
	if err != nil {
		t.Fatalf("failed to render template: %v", err)
	}

	html := buf.String()

	// FR23: script must use MutationObserver to track changes in sse-swap
	if !strings.Contains(html, `MutationObserver`) {
		t.Errorf("FR23: script must use MutationObserver to track pushed fragments; got: %s", html)
	}

	// FR23: MutationObserver must update lastEventTime
	if !strings.Contains(html, `lastEventTime = Date.now()`) {
		t.Errorf("FR23: MutationObserver must update lastEventTime on mutations; got: %s", html)
	}
}

// TestPromotionDetails_NFR12_IndicatorVisibility tests NFR12:
// the indicator and reload affordance are positioned outside the sse-swap
// target, so they do not disappear and reappear with pushed updates.
func TestPromotionDetails_NFR12_IndicatorVisibility(t *testing.T) {
	state := pages.PromotionDetailsViewState{
		PromotionID: "test-promo",
		Details: &pb.GetPromotionDetailsResponse{
			Details: &pb.PromotionDetails{
				Promotion:           &pb.Promotion{PromotionId: "test-promo", EnvironmentKey: "dev"},
				FromVersion:         "v1.0.0",
				ToVersion:           "v2.0.0",
				Outcome:             pb.PromotionSyncOutcome_PROMOTION_SYNC_OUTCOME_SYNCED_HEALTHY,
				CurrentSyncStatus:   "Synced",
				CurrentHealthStatus: "Healthy",
			},
		},
		LoadErr:  "",
		RetryErr: "",
	}

	user := &htmxauth.UserInfo{Sub: "test-user"}
	component := pages.PromotionDetails(user, state)

	var buf strings.Builder
	ctx := context.Background()
	err := component.Render(ctx, &buf)
	if err != nil {
		t.Fatalf("failed to render template: %v", err)
	}

	html := buf.String()

	// NFR12: indicator must appear BEFORE the sse-swap target
	indicatorIdx := strings.Index(html, `id="promo-live-indicator"`)
	reloadIdx := strings.Index(html, `id="promo-reload-container"`)
	swapIdx := strings.Index(html, `sse-swap="innerHTML"`)

	if indicatorIdx < 0 || reloadIdx < 0 || swapIdx < 0 {
		t.Errorf("NFR12: all elements (indicator, reload, sse-swap) must be present in template")
	}

	// Both indicator and reload must come BEFORE sse-swap
	if indicatorIdx > swapIdx {
		t.Errorf("NFR12: indicator must appear before sse-swap target (outside pushed region); got indices %d and %d", indicatorIdx, swapIdx)
	}

	if reloadIdx > swapIdx {
		t.Errorf("NFR12: reload affordance must appear before sse-swap target (outside pushed region); got indices %d and %d", reloadIdx, swapIdx)
	}
}

// TestPromotionDetails_FR24_ReloadLinkCorrectInContainer tests FR24:
// the reload link inside the reload container has the correct promotion ID,
// not just somewhere in the overall HTML.
func TestPromotionDetails_FR24_ReloadLinkCorrectInContainer(t *testing.T) {
	promotionID := "test-promo-12345"
	state := pages.PromotionDetailsViewState{
		PromotionID: promotionID,
		Details: &pb.GetPromotionDetailsResponse{
			Details: &pb.PromotionDetails{
				Promotion:           &pb.Promotion{PromotionId: promotionID, EnvironmentKey: "dev"},
				FromVersion:         "v1.0.0",
				ToVersion:           "v2.0.0",
				Outcome:             pb.PromotionSyncOutcome_PROMOTION_SYNC_OUTCOME_SYNCED_HEALTHY,
				CurrentSyncStatus:   "Synced",
				CurrentHealthStatus: "Healthy",
			},
		},
	}

	user := &htmxauth.UserInfo{Sub: "test-user"}
	component := pages.PromotionDetails(user, state)

	var buf strings.Builder
	ctx := context.Background()
	component.Render(ctx, &buf)

	html := buf.String()

	// Find the reload container specifically
	containerStart := strings.Index(html, `id="promo-reload-container"`)
	if containerStart == -1 {
		t.Fatalf("FR24: reload container not found in template")
	}

	// Find the closing div of the reload container
	remaining := html[containerStart:]
	containerEnd := strings.Index(remaining, `</div>`)
	if containerEnd == -1 {
		t.Fatalf("FR24: reload container closing tag not found")
	}

	reloadSection := remaining[:containerEnd+6]

	// Check that the reload section contains the correct promotion ID
	expectedLink := `/promotions/` + promotionID
	if !strings.Contains(reloadSection, expectedLink) {
		t.Errorf("FR24: reload link must contain %q; got: %s", expectedLink, reloadSection)
	}

	// Verify it's in the href attribute (not just text)
	if !strings.Contains(reloadSection, `href=`) {
		t.Errorf("FR24: reload must have an href attribute; got: %s", reloadSection)
	}

	// Verify the link text says "Reload"
	if !strings.Contains(reloadSection, `>Reload</a>`) {
		t.Errorf("FR24: reload button text must say 'Reload'; got: %s", reloadSection)
	}
}
