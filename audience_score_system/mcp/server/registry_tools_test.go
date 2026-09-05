// FR41 (issue #1832): proves the schedule-draft and pacing MCP tool
// surface schedule_draft.go used to expose is actually gone from the live
// registry, not merely deleted from disk. External package (server_test)
// rather than registry_test.go's internal (server) package on purpose:
// this file imports mcp/tools, which imports mcp/server -- an internal
// test file doing the same import is a genuine dependency cycle to
// rules_go's Go compiler (this package's own test archive would import
// mcp/tools importing mcp/server), not just a style preference. Mirrors
// server_integration_test.go's same external-package-for-the-same-reason
// choice, but needs no Docker/Postgres: registration only builds
// closures, it never queries, so a bare *store.Store over a nil pool is
// enough, and skipping server.New's PersonMiddleware (irrelevant to what
// this file asserts) means tools/list needs no caller credential either.
package server_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/audience_score_system/mcp/server"
	"github.com/whale-net/everything/audience_score_system/mcp/tools"
	"github.com/whale-net/everything/audience_score_system/store"
)

// noopScheduleTrigger is a minimal tools.ScheduleTrigger stand-in --
// RegisterTriggerChannelSync needs one to register trigger_channel_sync,
// but this test never calls any tool, only lists them.
type noopScheduleTrigger struct{}

func (noopScheduleTrigger) TriggerNow(context.Context, uuid.UUID) error { return nil }

// retiredScheduleDraftAndPacingToolNames is exactly the eight
// schedule_draft.go tool names FR41 (issue #1832) retired.
// generate_schedule_plan -- the ninth tool schedule_draft.go's own doc
// comment used to list alongside these -- is deliberately excluded here:
// it lives in strategy.go, is owned by #1833's FR47 cadence retirement,
// and is still registered below via tools.RegisterStrategy, so including
// it would fail this test the moment that separate task lands.
var retiredScheduleDraftAndPacingToolNames = []string{
	"save_schedule_draft",
	"commit_schedule_draft",
	"uncommit_schedule_draft",
	"update_schedule_draft",
	"list_schedule_entries",
	"get_drafting_context",
	"get_pacing_policy",
	"set_pacing_policy",
}

// TestRegistry_RetiredScheduleDraftAndPacingTools_NotRegistered mirrors
// ../main.go's full tool registration (every tools.RegisterXxx call `mcp`
// wires at boot, in the same order) against a bare *store.Store (nil
// pool), then lists the live registry's tools by name over a real
// in-memory MCP client/server connection (mcp.NewInMemoryTransports).
// Asserting by name against the registry's tools/list response -- not by
// grepping schedule_draft.go, which this task already deleted outright --
// means a future accidental re-add of any of these eight names fails this
// test loudly instead of silently reappearing in the tool surface.
func TestRegistry_RetiredScheduleDraftAndPacingTools_NotRegistered(t *testing.T) {
	ctx := context.Background()
	st := store.New(nil)

	srv := mcp.NewServer(server.Implementation, nil)
	reg := server.NewRegistry(srv, st)

	tools.RegisterWhoami(reg)
	tools.RegisterListChannels(reg, st.Access())
	tools.RegisterResearch(reg, st)
	tools.RegisterVerdict(reg, st)
	tools.RegisterVideoScript(reg, st)
	tools.RegisterMatches(reg, st)
	tools.RegisterBrowse(reg, st)
	tools.RegisterStrategy(reg, st)
	tools.RegisterOutcomeBar(reg, st.OutcomeBars(), st.Calibration())
	tools.RegisterTriggerChannelSync(reg, st.Channels(), noopScheduleTrigger{})
	tools.RegisterAccess(reg, st)
	tools.RegisterMyWork(reg, st.MyWork())
	tools.RegisterChannelAccess(reg, st.Access(), st.Roles())

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	_, err := srv.Connect(ctx, serverTransport, nil)
	require.NoError(t, err)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })

	registered := map[string]bool{}
	for tool, err := range cs.Tools(ctx, nil) {
		require.NoError(t, err)
		registered[tool.Name] = true
	}

	require.NotEmpty(t, registered, "sanity check: registration must have produced at least one tool")
	assert.True(t, registered["save_video_script"], "sanity check: the video_script replacement surface must be registered")

	for _, name := range retiredScheduleDraftAndPacingToolNames {
		assert.False(t, registered[name], "%s must not be registered -- FR41 retired it", name)
	}

	// #1883: set_outcome_bar/get_outcome_bar are the new additive surface
	// over the outcome bar -- present alongside the retired names' absence,
	// proving this task's RegisterOutcomeBar call actually lands in the
	// live registry, not just on disk.
	assert.True(t, registered["set_outcome_bar"], "set_outcome_bar must be registered")
	assert.True(t, registered["get_outcome_bar"], "get_outcome_bar must be registered")

	// #1885: get_calibration_trend is the C14 read surface over the
	// calibration store, wired through the same RegisterOutcomeBar call.
	assert.True(t, registered["get_calibration_trend"], "get_calibration_trend must be registered")
}
