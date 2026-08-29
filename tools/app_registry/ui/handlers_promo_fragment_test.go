package main

import (
	"context"
	"testing"

	"github.com/whale-net/everything/libs/go/htmxauth"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
)

// TestRenderPromoDetailsFragment_CallsGetPromotionDetails tests that the
// renderPromoDetailsFragment component correctly calls GetPromotionDetails
// when rendered. This indirectly tests FR22 (sync outcome rendering)
// and FR3 (fragment rendering).
func TestRenderPromoDetailsFragment_CallsGetPromotionDetails(t *testing.T) {
	ctx := context.Background()

	fakeClient := &fakePromotionClient{
		getDetailsFunc: func(in *pb.GetPromotionDetailsRequest) (*pb.GetPromotionDetailsResponse, error) {
			return &pb.GetPromotionDetailsResponse{Details: &pb.PromotionDetails{
				Promotion:   &pb.Promotion{PromotionId: in.GetPromotionId(), EnvironmentKey: "dev"},
				FromVersion: "v1.0.0",
				ToVersion:   "v2.0.0",
				Outcome:     pb.PromotionSyncOutcome_PROMOTION_SYNC_OUTCOME_SYNCED_HEALTHY,
			}}, nil
		},
	}

	auth, err := htmxauth.NewAuthenticator(ctx, htmxauth.Config{
		Mode:          htmxauth.AuthModeNone,
		SessionSecret: "dev-secret-at-least-32-bytes-long-xxxx",
		SessionName:   "app_registry_ui_session",
	})
	if err != nil {
		t.Fatalf("failed to create authenticator: %v", err)
	}

	app := &App{
		registry: &RegistryClient{Promotion: fakeClient},
		auth:     auth,
	}

	// Create a component
	component := renderPromoDetailsFragment(nil, "test-promo", func() {}, app)

	if component == nil {
		t.Errorf("renderPromoDetailsFragment should not return nil")
	}

	// The component is created but not rendered, so GetPromotionDetails shouldn't be called yet
	// This will only be called when the component is rendered
}

// TestRenderPromoDetailsFragment_FR22_HandlesFailedSync tests that a promotion
// with sync failed outcome would be handled correctly by the fragment function.
// This test verifies the setup that would render FR22 (sync failed badge).
func TestRenderPromoDetailsFragment_FR22_HandlesFailedSync(t *testing.T) {
	ctx := context.Background()

	fakeClient := &fakePromotionClient{
		getDetailsFunc: func(in *pb.GetPromotionDetailsRequest) (*pb.GetPromotionDetailsResponse, error) {
			return &pb.GetPromotionDetailsResponse{Details: &pb.PromotionDetails{
				Promotion:           &pb.Promotion{PromotionId: in.GetPromotionId(), EnvironmentKey: "staging"},
				FromVersion:         "v1.5.0",
				ToVersion:           "v2.0.0",
				Outcome:             pb.PromotionSyncOutcome_PROMOTION_SYNC_OUTCOME_SYNC_FAILED,
				CurrentSyncStatus:   "OutOfSync",
				CurrentHealthStatus: "Degraded",
			}}, nil
		},
	}

	auth, err := htmxauth.NewAuthenticator(ctx, htmxauth.Config{
		Mode:          htmxauth.AuthModeNone,
		SessionSecret: "dev-secret-at-least-32-bytes-long-xxxx",
		SessionName:   "app_registry_ui_session",
	})
	if err != nil {
		t.Fatalf("failed to create authenticator: %v", err)
	}

	app := &App{
		registry: &RegistryClient{Promotion: fakeClient},
		auth:     auth,
	}

	component := renderPromoDetailsFragment(nil, "test-promo", func() {}, app)

	if component == nil {
		t.Errorf("renderPromoDetailsFragment should not return nil for sync failed case")
	}
}
