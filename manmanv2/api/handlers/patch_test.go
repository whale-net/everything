package handlers

import (
	"testing"
	"time"

	manman "github.com/whale-net/everything/manmanv2/models"
)

// Guards task #2096's FR3 data surface: the persisted created_at/
// updated_at of a ConfigurationPatch must reach API clients (the UI's
// pending-override hint compares the patch's saved-at against a running
// session's start), rendered as Unix seconds. The DB already persists
// both columns; this mapping is what makes them observable.
//
// mutation-tested (verified red, by hand, then reverted): dropping the
// UpdatedAt assignment from patchToProto made
// TestPatchToProto_SurfacesSavedAtTimestamps fail; reverting restored
// green. (verified below)

func TestPatchToProto_SurfacesSavedAtTimestamps(t *testing.T) {
	created := time.Unix(1000, 0).UTC()
	updated := time.Unix(2000, 0).UTC()
	patch := &manman.ConfigurationPatch{
		PatchID:    7,
		StrategyID: 3,
		PatchLevel: "server_game_config",
		EntityID:   42,
		PatchOrder: 0,
		CreatedAt:  created,
		UpdatedAt:  updated,
	}

	got := patchToProto(patch)
	if got.GetCreatedAt() != 1000 {
		t.Errorf("created_at = %d, want 1000 (Unix seconds)", got.GetCreatedAt())
	}
	if got.GetUpdatedAt() != 2000 {
		t.Errorf("updated_at = %d, want 2000 (Unix seconds)", got.GetUpdatedAt())
	}
}

func TestPatchToProto_ZeroTimestampsRenderAsZero(t *testing.T) {
	got := patchToProto(&manman.ConfigurationPatch{PatchID: 1})
	if got.GetCreatedAt() != 0 || got.GetUpdatedAt() != 0 {
		t.Errorf("timestamps = (%d, %d), want (0, 0) so clients treat them as unknown",
			got.GetCreatedAt(), got.GetUpdatedAt())
	}
}
