// trigger_channel_sync -- manual C6/C9 sync trigger (issue #1650): lets a
// caller force an immediate, out-of-band run of a Channel's Temporal
// worker/sync.ChannelSyncWorkflow instead of waiting for its regular
// ASS_SYNC_INTERVAL cadence (default 24h -- see
// ../../ENV.md). This is the pain point issue #1650 hit directly:
// committing a schedule draft or reconnecting a Channel has no way to
// verify the change took effect without polling list_pending_matches /
// get_channel_overview for up to a full sync interval. `web` has no
// equivalent surface -- NFR3 keeps this MCP-only, same as every other
// C6/C9 tool (see ../../ARCHITECTURE.md "NFR3 interface allocation").
package tools

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/audience_score_system/mcp/server"
	"github.com/whale-net/everything/audience_score_system/store"
)

// ScheduleTrigger is the subset of worker/sync.ScheduleManager's behavior
// trigger_channel_sync needs -- factored out so this package doesn't
// import worker/sync directly (mirrors web/channel.go's scheduleManager
// interface); ../main.go constructs the real sync.NewScheduleManager and
// passes it in through this narrow interface.
type ScheduleTrigger interface {
	TriggerNow(ctx context.Context, channelID uuid.UUID) error
}

// TriggerChannelSyncInput is trigger_channel_sync's argument schema.
type TriggerChannelSyncInput struct {
	ChannelID string `json:"channel_id" jsonschema:"the Channel to sync now, as a UUID string"`
}

// ChannelScopeID implements server.ChannelScoped -- RegisterWrite
// authorizes every call via store.CanWrite before the handler below runs
// (NFR5).
func (i TriggerChannelSyncInput) ChannelScopeID() uuid.UUID {
	id, _ := uuid.Parse(i.ChannelID)
	return id
}

// TriggerChannelSyncOutput is trigger_channel_sync's structured result.
type TriggerChannelSyncOutput struct {
	ChannelID string `json:"channel_id" jsonschema:"the Channel a sync run was just triggered for, as a UUID string"`
	Triggered bool   `json:"triggered" jsonschema:"always true on success -- the schedule's ChannelSyncWorkflow was queued to run immediately, subject to its SCHEDULE_OVERLAP_POLICY_SKIP (a run already in flight is skipped, not stacked)"`
}

// RegisterTriggerChannelSync registers trigger_channel_sync via
// server.RegisterWrite (NFR2 idempotency middleware applies, though this
// tool declares no idempotency key -- see triggerChannelSyncMutate's doc
// comment for why). Creator and Analyst may both trigger (store.CanWrite,
// the same gate save_schedule_draft/set_pacing_policy use) -- there is no
// product reason to reserve a manual sync for the Creator only. channels
// is used to turn a nonexistent channel_id into a clear error rather than
// an opaque Temporal "schedule not found" one; trigger is ../main.go's
// real sync.ScheduleManager (via the ScheduleTrigger interface above).
func RegisterTriggerChannelSync(reg *server.Registry, channels store.ChannelStore, trigger ScheduleTrigger) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name: "trigger_channel_sync",
		Description: "Force an immediate, out-of-band run of a Channel's schedule/outcome sync (C6/C9) instead of " +
			"waiting for its regular ASS_SYNC_INTERVAL cadence -- useful right after committing a schedule draft or " +
			"reconnecting a Channel, to verify the change took effect without polling. Safe to call repeatedly: the " +
			"schedule's overlap policy skips a trigger while a run is already in flight rather than stacking " +
			"concurrent runs.",
	}, triggerChannelSyncMutate(channels, trigger), triggerChannelSyncRender())
}

// triggerChannelSyncMutate declares no idempotency key -- unlike every
// other write tool in this package, "trigger a sync" is not itself a
// persisted entity a replay could re-derive a response from, and a
// deliberate second manual trigger is meant to actually trigger again
// (e.g. after fixing a draft and re-committing), not be deduped into a
// no-op. SCHEDULE_OVERLAP_POLICY_SKIP already makes an accidental
// double-call harmless (Temporal itself skips the extra run while one is
// in flight), so this tool does not implement server.IdempotencyKeyed and
// simply runs mutate on every call, per server.RegisterWrite's documented
// no-key path.
func triggerChannelSyncMutate(channels store.ChannelStore, trigger ScheduleTrigger) server.WriteMutate[TriggerChannelSyncInput] {
	return func(ctx context.Context, in TriggerChannelSyncInput) (uuid.UUID, error) {
		channelID, err := uuid.Parse(in.ChannelID)
		if err != nil {
			return uuid.Nil, fmt.Errorf("channel_id is not a valid UUID: %w", err)
		}

		if _, err := channels.GetByID(ctx, channelID); err != nil {
			return uuid.Nil, fmt.Errorf("load channel: %w", err)
		}

		if err := trigger.TriggerNow(ctx, channelID); err != nil {
			return uuid.Nil, fmt.Errorf("trigger_channel_sync: %w", err)
		}
		return channelID, nil
	}
}

// triggerChannelSyncRender needs no store lookup -- ref is already the
// channelID triggerChannelSyncMutate resolved, and there is no persisted
// row to re-read (see that function's doc comment on why this tool has no
// idempotency key to replay against).
func triggerChannelSyncRender() server.WriteRender[TriggerChannelSyncOutput] {
	return func(ctx context.Context, ref uuid.UUID) (*mcp.CallToolResult, TriggerChannelSyncOutput, error) {
		return nil, TriggerChannelSyncOutput{ChannelID: ref.String(), Triggered: true}, nil
	}
}
