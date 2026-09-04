// get_channel_access (issue #1719, FR35): a Channel's current roster
// (mirroring web/access's page) plus its grant/revoke audit trail
// (v_channel_person_audit, migration 009) -- the MCP counterpart to
// web/access, and the ONLY surface (web or MCP) that exposes the audit
// trail as of this task.
//
// server.RegisterRead's automatic ChannelScoped gate only enforces
// store.CanRead (Creator, Co-Creator, AND Analyst) -- too permissive for
// FR35, which restricts audit visibility to Creator and Co-Creator only.
// getChannelAccess therefore calls store.CanViewAudit explicitly before
// touching AccessStore.Roster/AuditTrail, and returns a permission error
// with NEITHER roster NOR history populated for an Analyst -- one tool,
// refused as a whole, never a partial response.
package tools

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/audience_score_system/mcp/server"
	"github.com/whale-net/everything/audience_score_system/store"
)

// defaultChannelAccessHistoryLimit bounds get_channel_access's audit
// trail when history_limit is omitted or <= 0.
const defaultChannelAccessHistoryLimit = 50

// unknownDisplayName is what get_channel_access renders in place of an
// unresolved grant/revoke actor or granter -- store.RosterEntry.
// GrantedByDisplayName's and store.AuditEvent.ActorDisplayName's own doc
// comments both say "render unknown upstream rather than inventing one";
// this is that one rendering site for both.
const unknownDisplayName = "unknown"

// orUnknown returns name, or unknownDisplayName if name is empty --
// see unknownDisplayName's doc comment.
func orUnknown(name string) string {
	if name == "" {
		return unknownDisplayName
	}
	return name
}

// GetChannelAccessInput is get_channel_access's argument schema.
type GetChannelAccessInput struct {
	ChannelID    string `json:"channel_id" jsonschema:"Channel to view the access roster and audit trail for, as a UUID string"`
	HistoryLimit int    `json:"history_limit,omitempty" jsonschema:"Maximum audit trail rows to return, most-recent first (default 50)"`
}

// ChannelScopeID implements server.ChannelScoped.
func (i GetChannelAccessInput) ChannelScopeID() uuid.UUID {
	id, _ := uuid.Parse(i.ChannelID)
	return id
}

// RosterEntryOutput is one Person holding an open role on the Channel, as
// get_channel_access's roster field renders it.
type RosterEntryOutput struct {
	PersonID             string `json:"person_id" jsonschema:"This Person's ID, as a UUID string"`
	DisplayName          string `json:"display_name" jsonschema:"This Person's display name"`
	Email                string `json:"email" jsonschema:"This Person's email address"`
	Role                 string `json:"role" jsonschema:"creator, co_creator, or analyst"`
	GrantedAt            string `json:"granted_at" jsonschema:"When this role was granted, RFC3339"`
	GrantedByDisplayName string `json:"granted_by_display_name" jsonschema:"Who granted this role; \"unknown\" for pre-M2 rows that predate grant attribution -- never fabricated"`
}

func toRosterEntryOutput(e store.RosterEntry) RosterEntryOutput {
	return RosterEntryOutput{
		PersonID:             e.PersonID.String(),
		DisplayName:          e.DisplayName,
		Email:                e.Email,
		Role:                 string(e.Role),
		GrantedAt:            e.GrantedAt.Format(time.RFC3339),
		GrantedByDisplayName: orUnknown(e.GrantedByDisplayName),
	}
}

// AuditEventOutput is one grant or revoke event, as get_channel_access's
// history field renders it (FR35).
type AuditEventOutput struct {
	Event              string `json:"event" jsonschema:"granted or revoked"`
	OccurredAt         string `json:"occurred_at" jsonschema:"When this event occurred, RFC3339"`
	SubjectPersonID    string `json:"subject_person_id" jsonschema:"The Person this grant/revoke concerns, as a UUID string"`
	SubjectDisplayName string `json:"subject_display_name" jsonschema:"The subject's display name"`
	Role               string `json:"role" jsonschema:"creator, co_creator, or analyst"`
	ActorDisplayName   string `json:"actor_display_name" jsonschema:"Who performed this grant/revoke; \"unknown\" for pre-M2 rows with no recorded actor -- never fabricated"`
}

func toAuditEventOutput(e store.AuditEvent) AuditEventOutput {
	return AuditEventOutput{
		Event:              e.Event,
		OccurredAt:         e.OccurredAt.Format(time.RFC3339),
		SubjectPersonID:    e.SubjectPersonID.String(),
		SubjectDisplayName: e.SubjectDisplayName,
		Role:               string(e.Role),
		ActorDisplayName:   orUnknown(e.ActorDisplayName),
	}
}

// GetChannelAccessOutput is get_channel_access's structured result.
type GetChannelAccessOutput struct {
	Roster  []RosterEntryOutput `json:"roster" jsonschema:"Every Person currently holding an open role on this Channel"`
	History []AuditEventOutput  `json:"history" jsonschema:"Grant/revoke history, most-recent first, capped at history_limit"`
}

// RegisterChannelAccess registers get_channel_access via
// server.RegisterRead. See package doc comment for why the store.
// CanViewAudit check is explicit rather than relying on RegisterRead's
// automatic store.CanRead gate.
func RegisterChannelAccess(reg *server.Registry, access store.AccessStore, roles store.RoleStore) {
	server.RegisterRead(reg, &mcp.Tool{
		Name: "get_channel_access",
		Description: "Read a Channel's current access roster and grant/revoke audit trail (FR35). Founder or " +
			"Co-Creator only (store.CanViewAudit) -- an Analyst calling this is rejected with a permission error and " +
			"gets neither the roster nor the history, not a partial response. History is most-recent-first and " +
			"includes both grant and revoke events, capped at history_limit (default 50). Pre-M2 rows with no " +
			"recorded actor/granter render as \"unknown\", never a fabricated name.",
	}, getChannelAccess(access, roles))
}

func getChannelAccess(access store.AccessStore, roles store.RoleStore) mcp.ToolHandlerFor[GetChannelAccessInput, GetChannelAccessOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in GetChannelAccessInput) (*mcp.CallToolResult, GetChannelAccessOutput, error) {
		channelID := in.ChannelScopeID()

		person := server.PersonFromContext(ctx)
		if person == nil {
			return nil, GetChannelAccessOutput{}, fmt.Errorf("unauthenticated: no caller credential resolved")
		}

		canViewAudit, err := store.CanViewAudit(ctx, roles, channelID, person.ID)
		if err != nil {
			return nil, GetChannelAccessOutput{}, fmt.Errorf("get_channel_access: check audit authority: %w", err)
		}
		if !canViewAudit {
			return nil, GetChannelAccessOutput{}, fmt.Errorf(
				"permission denied: only a Channel's Founder or Co-Creator may view its access roster and audit trail (FR35)")
		}

		limit := in.HistoryLimit
		if limit <= 0 {
			limit = defaultChannelAccessHistoryLimit
		}

		roster, err := access.Roster(ctx, channelID)
		if err != nil {
			return nil, GetChannelAccessOutput{}, fmt.Errorf("get_channel_access: load roster: %w", err)
		}
		history, err := access.AuditTrail(ctx, channelID, limit)
		if err != nil {
			return nil, GetChannelAccessOutput{}, fmt.Errorf("get_channel_access: load audit trail: %w", err)
		}

		out := GetChannelAccessOutput{
			Roster:  make([]RosterEntryOutput, 0, len(roster)),
			History: make([]AuditEventOutput, 0, len(history)),
		}
		for _, r := range roster {
			out.Roster = append(out.Roster, toRosterEntryOutput(r))
		}
		for _, e := range history {
			out.History = append(out.History, toAuditEventOutput(e))
		}
		return nil, out, nil
	}
}
