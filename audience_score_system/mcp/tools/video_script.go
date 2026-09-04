// video_script.go covers the MCP surface for `video_script` (migration
// 010, milestone video-script-model, issues #1823/#1825): save_
// video_script (FR36, C18) and its lifecycle trio greenlight_video_script/
// deny_video_script/archive_video_script (FR37-FR39, C19). Structured like
// schedule_draft.go (the surface this milestone replaces) and verdict.go
// (the closest write-tool shape). Every handler here is a thin
// authorization + rendering wrapper around store.VideoScriptStore
// (../../store/video_script.go, already real from #1824) -- no schema or
// store logic belongs in this file.
//
// Scaffold only: every mutate function below returns
// errVideoScriptToolNotImplemented until Implementation wires in the real
// calls, same scaffold/feat split store/video_script.go's own methods
// followed (issue #1824).
package tools

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/audience_score_system/mcp/server"
	"github.com/whale-net/everything/audience_score_system/store"
)

// errVideoScriptToolNotImplemented is returned by every video_script tool's
// mutate function until Implementation lands (issue #1825's Implementation
// phase). Never wrapped into a store error -- this is scaffold-only
// scaffolding, not a runtime condition a caller can hit through the real
// store.
var errVideoScriptToolNotImplemented = errors.New("video_script tool not implemented yet")

// -- shared rendering ---------------------------------------------------------

// VideoScriptOutput is one `video_script` row, as save_video_script,
// greenlight_video_script, deny_video_script, and archive_video_script all
// render it -- the idea, the exact verdict version it's bound to (LB3),
// the strategy it's proposed under, status, and decision/creation
// timestamps a caller needs without a follow-up call.
type VideoScriptOutput struct {
	VideoScriptID  string `json:"video_script_id" jsonschema:"this video_script's ID, as a UUID string"`
	ChannelID      string `json:"channel_id" jsonschema:"the Channel this video_script belongs to, as a UUID string"`
	IdeaID         string `json:"idea_id" jsonschema:"the Idea this video_script is for, as a UUID string -- always derived from verdict_id, never a caller-supplied input"`
	IdeaTitle      string `json:"idea_title" jsonschema:"that Idea's title"`
	VerdictID      string `json:"verdict_id" jsonschema:"the specific viability_verdict version this video_script is bound to (LB3), as a UUID string -- never a copy of the verdict text, never just the idea_id"`
	VerdictVersion int    `json:"verdict_version" jsonschema:"that verdict version's number, so a caller can tell a stale binding from a fresh one"`
	VerdictValue   string `json:"verdict_value" jsonschema:"that verdict version's value: viable, not-viable, or needs-more-research"`
	StrategyID     string `json:"strategy_id" jsonschema:"the Strategy this video_script is proposed under, as a UUID string"`
	StrategyTitle  string `json:"strategy_title" jsonschema:"that Strategy's title"`
	Title          string `json:"title" jsonschema:"this video_script's title"`
	ScriptText     string `json:"script_text" jsonschema:"this video_script's full script text"`
	Status         string `json:"status" jsonschema:"proposed, greenlit, denied, or archived (FR36-FR40)"`

	TargetPublishDate *string `json:"target_publish_date,omitempty" jsonschema:"the proposed target publish date, RFC3339, if set"`

	DecidedByPersonID    *string `json:"decided_by_person_id,omitempty" jsonschema:"the Person who greenlit, denied, or archived this video_script, as a UUID string, if decided"`
	DecidedByDisplayName *string `json:"decided_by_display_name,omitempty" jsonschema:"that Person's display name, if decided"`
	DecidedAt            *string `json:"decided_at,omitempty" jsonschema:"when this video_script was greenlit, denied, or archived, RFC3339, if decided"`

	CreatedByPersonID    string `json:"created_by_person_id" jsonschema:"the Person who proposed this video_script, as a UUID string"`
	CreatedByDisplayName string `json:"created_by_display_name" jsonschema:"that Person's display name"`
	CreatedAt            string `json:"created_at" jsonschema:"when this video_script was first proposed, RFC3339"`
}

// renderVideoScript resolves script's idea title, bound verdict version/
// value, strategy title, author display name, and (if decided) decider
// display name, and renders the result as VideoScriptOutput -- the single
// call site every tool that renders a video_script shares, so they can
// never disagree on shape. Always re-derives everything from the store
// (never a cached value) per LB4/RegisterWrite's render contract.
func renderVideoScript(ctx context.Context, ideas store.IdeaStore, verdicts store.VerdictStore, strategies store.StrategyStore, persons store.PersonStore, script store.VideoScript) (VideoScriptOutput, error) {
	idea, err := ideas.GetByID(ctx, script.IdeaID)
	if err != nil {
		return VideoScriptOutput{}, fmt.Errorf("load video_script's idea: %w", err)
	}

	verdict, err := verdicts.GetByID(ctx, script.VerdictID)
	if err != nil {
		return VideoScriptOutput{}, fmt.Errorf("load video_script's bound verdict: %w", err)
	}

	strategy, err := strategies.GetByID(ctx, script.StrategyID)
	if err != nil {
		return VideoScriptOutput{}, fmt.Errorf("load video_script's strategy: %w", err)
	}

	creator, err := persons.GetByID(ctx, script.CreatedByPersonID)
	if err != nil {
		return VideoScriptOutput{}, fmt.Errorf("load video_script's author: %w", err)
	}

	out := VideoScriptOutput{
		VideoScriptID:        script.ID.String(),
		ChannelID:            script.ChannelID.String(),
		IdeaID:               script.IdeaID.String(),
		IdeaTitle:            idea.Title,
		VerdictID:            script.VerdictID.String(),
		VerdictVersion:       verdict.Version,
		VerdictValue:         string(verdict.Verdict),
		StrategyID:           script.StrategyID.String(),
		StrategyTitle:        strategy.Title,
		Title:                script.Title,
		ScriptText:           script.ScriptText,
		Status:               string(script.Status),
		CreatedByPersonID:    script.CreatedByPersonID.String(),
		CreatedByDisplayName: creator.DisplayName,
		CreatedAt:            script.CreatedAt.Format(time.RFC3339),
	}
	if script.TargetPublishDate != nil {
		formatted := script.TargetPublishDate.Format(time.RFC3339)
		out.TargetPublishDate = &formatted
	}
	if script.DecidedByPersonID != nil {
		decider, err := persons.GetByID(ctx, *script.DecidedByPersonID)
		if err != nil {
			return VideoScriptOutput{}, fmt.Errorf("load video_script's decider: %w", err)
		}
		id := script.DecidedByPersonID.String()
		out.DecidedByPersonID = &id
		out.DecidedByDisplayName = &decider.DisplayName
	}
	if script.DecidedAt != nil {
		decidedAt := script.DecidedAt.Format(time.RFC3339)
		out.DecidedAt = &decidedAt
	}
	return out, nil
}

// renderVideoScriptByID re-reads scriptID from the store and renders it --
// the shared render step every one of this file's four write tools uses,
// so none of them ever returns a value that isn't a fresh read (LB4, see
// ../server/registry.go's RegisterWrite contract).
func renderVideoScriptByID(ideas store.IdeaStore, verdicts store.VerdictStore, strategies store.StrategyStore, persons store.PersonStore, scripts store.VideoScriptStore) server.WriteRender[VideoScriptOutput] {
	return func(ctx context.Context, ref uuid.UUID) (*mcp.CallToolResult, VideoScriptOutput, error) {
		script, err := scripts.GetByID(ctx, ref)
		if err != nil {
			return nil, VideoScriptOutput{}, fmt.Errorf("load video_script: %w", err)
		}
		out, err := renderVideoScript(ctx, ideas, verdicts, strategies, persons, script)
		if err != nil {
			return nil, VideoScriptOutput{}, err
		}
		return nil, out, nil
	}
}

// -- save_video_script ---------------------------------------------------------

// SaveVideoScriptInput is save_video_script's argument schema (FR36, C18).
// idea_id is deliberately NOT a field here -- it is always derived from
// verdict_id by the store (LB3), never accepted from the caller.
type SaveVideoScriptInput struct {
	ChannelID         string `json:"channel_id" jsonschema:"Channel this video_script belongs to, as a UUID string"`
	VerdictID         string `json:"verdict_id" jsonschema:"the specific viability_verdict version this video_script is bound to, as a UUID string (LB3); must be viable"`
	StrategyID        string `json:"strategy_id" jsonschema:"the Strategy this video_script is proposed under, as a UUID string"`
	Title             string `json:"title" jsonschema:"this video_script's title; must not be empty"`
	ScriptText        string `json:"script_text" jsonschema:"this video_script's full script text; must not be empty"`
	TargetPublishDate string `json:"target_publish_date,omitempty" jsonschema:"optional target publish date, RFC3339"`
	// IdempotencyKeyArg backs IdempotencyKey() below -- named ...Arg because
	// a Go type cannot declare both a field and a method named
	// IdempotencyKey (mirrors verdict.go's identical pattern).
	IdempotencyKeyArg string `json:"idempotency_key,omitempty" jsonschema:"Caller-supplied idempotency key. Strongly recommended: a retry without one may propose a spurious duplicate video_script (NFR12)."`
}

// ChannelScopeID implements server.ChannelScoped.
func (i SaveVideoScriptInput) ChannelScopeID() uuid.UUID {
	id, _ := uuid.Parse(i.ChannelID)
	return id
}

// IdempotencyKey implements server.IdempotencyKeyed.
func (i SaveVideoScriptInput) IdempotencyKey() string { return i.IdempotencyKeyArg }

// registerSaveVideoScript registers save_video_script via
// server.RegisterWrite, so the idempotency middleware (NFR2/LB4/NFR12) and
// the ChannelScoped store.CanWrite gate apply automatically. CanWrite is
// the correct tier here -- Creator, co-Creator, AND Analyst may all
// propose (NFR13).
func registerSaveVideoScript(reg *server.Registry, scripts store.VideoScriptStore, ideas store.IdeaStore, verdicts store.VerdictStore, strategies store.StrategyStore, persons store.PersonStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name: "save_video_script",
		Description: "Propose a new video_script for a viable Idea, under a Strategy (FR36). Bound by foreign key to " +
			"the exact verdict version that judged the idea viable (LB3) -- idea_id is always derived from verdict_id, " +
			"never a caller-supplied input. Rejects a verdict that is not viable, or a verdict/strategy that does not " +
			"belong to channel_id -- nothing is written in that case. Founder, Co-Creator, and Analyst may all propose " +
			"(NFR13). Always supply idempotency_key: a retry without one may propose a spurious duplicate.",
	}, saveVideoScriptMutate(scripts), renderVideoScriptByID(ideas, verdicts, strategies, persons, scripts))
}

func saveVideoScriptMutate(scripts store.VideoScriptStore) server.WriteMutate[SaveVideoScriptInput] {
	return func(ctx context.Context, in SaveVideoScriptInput) (uuid.UUID, error) {
		return uuid.Nil, errVideoScriptToolNotImplemented
	}
}

// -- greenlight_video_script ----------------------------------------------------

// GreenlightVideoScriptInput is greenlight_video_script's argument schema
// (FR37, C19).
type GreenlightVideoScriptInput struct {
	ChannelID         string `json:"channel_id" jsonschema:"Channel the video_script belongs to, as a UUID string"`
	VideoScriptID     string `json:"video_script_id" jsonschema:"the proposed video_script to greenlight, as a UUID string"`
	IdempotencyKeyArg string `json:"idempotency_key,omitempty" jsonschema:"Caller-supplied idempotency key. Strongly recommended: a replay with the same key is a no-op returning the original result (NFR12); greenlighting an already-decided script without one is rejected as a conflict, never a silent no-op."`
}

// ChannelScopeID implements server.ChannelScoped.
func (i GreenlightVideoScriptInput) ChannelScopeID() uuid.UUID {
	id, _ := uuid.Parse(i.ChannelID)
	return id
}

// IdempotencyKey implements server.IdempotencyKeyed.
func (i GreenlightVideoScriptInput) IdempotencyKey() string { return i.IdempotencyKeyArg }

// registerGreenlightVideoScript registers greenlight_video_script via
// server.RegisterWrite for idempotency, but -- unlike save_video_script --
// its mutate function must ADDITIONALLY perform an explicit
// store.CanApprove check before calling the store: RegisterWrite's
// built-in ChannelScoped gate is store.CanWrite, which an Analyst passes,
// and is not sufficient on its own for this tool (FR37). Mirrors
// commit_schedule_draft's gating (schedule_draft.go).
func registerGreenlightVideoScript(reg *server.Registry, scripts store.VideoScriptStore, ideas store.IdeaStore, verdicts store.VerdictStore, strategies store.StrategyStore, persons store.PersonStore, roles store.RoleStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name: "greenlight_video_script",
		Description: "Transition a proposed video_script to greenlit, recording the approving Founder or Co-Creator " +
			"(FR37). Requires Creator-tier authority (store.CanApprove -- Founder or Co-Creator) -- an Analyst calling " +
			"this is rejected with a permission error, even though an Analyst may propose the script via " +
			"save_video_script. Rejected with a conflict if video_script_id is not currently proposed. Always supply " +
			"idempotency_key: greenlighting an already-decided script without one is rejected as a conflict, not " +
			"treated as a no-op replay.",
	}, greenlightVideoScriptMutate(scripts, roles), renderVideoScriptByID(ideas, verdicts, strategies, persons, scripts))
}

func greenlightVideoScriptMutate(scripts store.VideoScriptStore, roles store.RoleStore) server.WriteMutate[GreenlightVideoScriptInput] {
	return func(ctx context.Context, in GreenlightVideoScriptInput) (uuid.UUID, error) {
		return uuid.Nil, errVideoScriptToolNotImplemented
	}
}

// -- deny_video_script -----------------------------------------------------------

// DenyVideoScriptInput is deny_video_script's argument schema (FR38, C19).
type DenyVideoScriptInput struct {
	ChannelID         string `json:"channel_id" jsonschema:"Channel the video_script belongs to, as a UUID string"`
	VideoScriptID     string `json:"video_script_id" jsonschema:"the proposed video_script to deny, as a UUID string"`
	IdempotencyKeyArg string `json:"idempotency_key,omitempty" jsonschema:"Caller-supplied idempotency key. Strongly recommended: a replay with the same key is a no-op returning the original result (NFR12); denying an already-decided script without one is rejected as a conflict, never a silent no-op."`
}

// ChannelScopeID implements server.ChannelScoped.
func (i DenyVideoScriptInput) ChannelScopeID() uuid.UUID {
	id, _ := uuid.Parse(i.ChannelID)
	return id
}

// IdempotencyKey implements server.IdempotencyKeyed.
func (i DenyVideoScriptInput) IdempotencyKey() string { return i.IdempotencyKeyArg }

// registerDenyVideoScript registers deny_video_script via
// server.RegisterWrite for idempotency, with the same explicit
// store.CanApprove check as greenlight_video_script -- RegisterWrite's
// automatic ChannelScoped gate (store.CanWrite) is not sufficient on its
// own for this tool (FR38).
func registerDenyVideoScript(reg *server.Registry, scripts store.VideoScriptStore, ideas store.IdeaStore, verdicts store.VerdictStore, strategies store.StrategyStore, persons store.PersonStore, roles store.RoleStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name: "deny_video_script",
		Description: "Transition a proposed video_script to denied (terminal), recording the denying Founder or " +
			"Co-Creator (FR38). Requires Creator-tier authority (store.CanApprove -- Founder or Co-Creator) -- an " +
			"Analyst calling this is rejected with a permission error. Rejected with a conflict if video_script_id " +
			"is not currently proposed. Always supply idempotency_key: denying an already-decided script without one " +
			"is rejected as a conflict, not treated as a no-op replay.",
	}, denyVideoScriptMutate(scripts, roles), renderVideoScriptByID(ideas, verdicts, strategies, persons, scripts))
}

func denyVideoScriptMutate(scripts store.VideoScriptStore, roles store.RoleStore) server.WriteMutate[DenyVideoScriptInput] {
	return func(ctx context.Context, in DenyVideoScriptInput) (uuid.UUID, error) {
		return uuid.Nil, errVideoScriptToolNotImplemented
	}
}

// -- archive_video_script --------------------------------------------------------

// ArchiveVideoScriptInput is archive_video_script's argument schema (FR39,
// C19).
type ArchiveVideoScriptInput struct {
	ChannelID         string `json:"channel_id" jsonschema:"Channel the video_script belongs to, as a UUID string"`
	VideoScriptID     string `json:"video_script_id" jsonschema:"the greenlit video_script to archive, as a UUID string"`
	IdempotencyKeyArg string `json:"idempotency_key,omitempty" jsonschema:"Caller-supplied idempotency key. Strongly recommended: a replay with the same key is a no-op returning the original result (NFR12); archiving an already-decided script without one is rejected as a conflict, never a silent no-op."`
}

// ChannelScopeID implements server.ChannelScoped.
func (i ArchiveVideoScriptInput) ChannelScopeID() uuid.UUID {
	id, _ := uuid.Parse(i.ChannelID)
	return id
}

// IdempotencyKey implements server.IdempotencyKeyed.
func (i ArchiveVideoScriptInput) IdempotencyKey() string { return i.IdempotencyKeyArg }

// registerArchiveVideoScript registers archive_video_script via
// server.RegisterWrite for idempotency, with the same explicit
// store.CanApprove check as greenlight_video_script/deny_video_script --
// RegisterWrite's automatic ChannelScoped gate (store.CanWrite) is not
// sufficient on its own for this tool (FR39).
func registerArchiveVideoScript(reg *server.Registry, scripts store.VideoScriptStore, ideas store.IdeaStore, verdicts store.VerdictStore, strategies store.StrategyStore, persons store.PersonStore, roles store.RoleStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name: "archive_video_script",
		Description: "Transition a greenlit video_script to archived, recording the archiving Founder or Co-Creator " +
			"(FR39). Requires Creator-tier authority (store.CanApprove -- Founder or Co-Creator) -- an Analyst calling " +
			"this is rejected with a permission error. Rejected with a conflict if video_script_id is not currently " +
			"greenlit, and frozen (a distinct error, no state change) if its matched video has already been published " +
			"-- an already-published video's script can never be archived. Always supply idempotency_key: archiving " +
			"an already-decided script without one is rejected as a conflict, not treated as a no-op replay.",
	}, archiveVideoScriptMutate(scripts, roles), renderVideoScriptByID(ideas, verdicts, strategies, persons, scripts))
}

func archiveVideoScriptMutate(scripts store.VideoScriptStore, roles store.RoleStore) server.WriteMutate[ArchiveVideoScriptInput] {
	return func(ctx context.Context, in ArchiveVideoScriptInput) (uuid.UUID, error) {
		return uuid.Nil, errVideoScriptToolNotImplemented
	}
}

// -- registration ------------------------------------------------------------

// RegisterVideoScript registers save_video_script, greenlight_video_script,
// deny_video_script, and archive_video_script against reg (see
// ../server/registry.go), backed by st's
// VideoScriptStore/IdeaStore/VerdictStore/StrategyStore/PersonStore/
// RoleStore.
func RegisterVideoScript(reg *server.Registry, st *store.Store) {
	registerSaveVideoScript(reg, st.VideoScripts(), st.Ideas(), st.Verdicts(), st.Strategies(), st.Persons())
	registerGreenlightVideoScript(reg, st.VideoScripts(), st.Ideas(), st.Verdicts(), st.Strategies(), st.Persons(), st.Roles())
	registerDenyVideoScript(reg, st.VideoScripts(), st.Ideas(), st.Verdicts(), st.Strategies(), st.Persons(), st.Roles())
	registerArchiveVideoScript(reg, st.VideoScripts(), st.Ideas(), st.Verdicts(), st.Strategies(), st.Persons(), st.Roles())
}
