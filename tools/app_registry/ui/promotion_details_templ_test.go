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
