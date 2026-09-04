// list_channels -- MCP channel-discovery tool (issue #1631): resolves
// which Channels the calling Person can see, and what role they hold on
// each, so an MCP-only client can find/resolve a channel_id without
// dropping to the web UI or asking a human to paste a UUID. Every other
// Channel-scoped tool requires a channel_id argument up front and simply
// rejects an unauthorized caller -- this is the one tool that answers
// "which Channels do I have access to in the first place?". Like whoami,
// it reports the caller's own state and takes no channel_id, so it does
// not implement server.ChannelScoped and is left unscoped by
// server.RegisterRead.
//
// FR26/NFR9 (issue #1719): backed by store.AccessStore.
// ChannelsWithRoleForPerson, a single JOIN query -- NOT the earlier
// (pre-#1719) implementation, which called store.RoleStore.RolesFor once
// per Channel inside a `for` loop (an N+1 query pattern NFR9 forbids).
package tools

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/audience_score_system/mcp/server"
	"github.com/whale-net/everything/audience_score_system/store"
)

// ChannelAccessOutput is one Channel the caller has a live role on, as
// list_channels renders it.
type ChannelAccessOutput struct {
	ChannelID       string `json:"channel_id" jsonschema:"This Channel's ID, as a UUID string -- pass this as channel_id to every other Channel-scoped tool"`
	Title           string `json:"title" jsonschema:"The Channel's title"`
	ConnectionState string `json:"connection_state" jsonschema:"connected or needs_reauth (FR4) -- an agent should explain a stale schedule when this is needs_reauth rather than assume the sync is simply slow"`
	// Roles is a slice, not a single value, for schema stability: LB2 does
	// not assume at most one role per (Channel, Person) pair as a general
	// modeling rule, mirroring store.RoleStore.RolesFor's own doc comment.
	// In practice this slice always holds exactly one entry --
	// store.AccessStore.ChannelsWithRoleForPerson's doc comment notes
	// migration 001's channel_person_channel_id_person_id_current partial
	// unique index guarantees at most one OPEN (channel_id, person_id)
	// row -- so a Person never holds two simultaneous roles on the same
	// Channel.
	Roles []string `json:"roles" jsonschema:"The caller's single currently-held role on this Channel, as a one-element list: creator (Founder), co_creator, or analyst"`
}

// ListChannelsOutput is list_channels' structured result.
type ListChannelsOutput struct {
	Channels []ChannelAccessOutput `json:"channels" jsonschema:"Every Channel the caller currently holds a live role on, ordered by title then id"`
}

// RegisterListChannels registers list_channels via server.RegisterRead.
// Takes no input -- like whoami, it reports the caller's own state, so
// there is nothing to Channel-scope.
func RegisterListChannels(reg *server.Registry, access store.AccessStore) {
	server.RegisterRead(reg, &mcp.Tool{
		Name: "list_channels",
		Description: "List every Channel the calling Person currently has access to, with that Channel's title, " +
			"connection_state, and the caller's role (creator, co_creator, or analyst) on it. This is the discovery " +
			"entry point for every other Channel-scoped tool: use the returned channel_id to call " +
			"get_channel_overview, list_ideas, save_research_note, and the rest.",
	}, listChannelsHandler(access))
}

func listChannelsHandler(access store.AccessStore) mcp.ToolHandlerFor[any, ListChannelsOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, ListChannelsOutput, error) {
		person := server.PersonFromContext(ctx)
		if person == nil {
			return nil, ListChannelsOutput{}, fmt.Errorf("unauthenticated: no caller credential resolved")
		}

		channelRoles, err := access.ChannelsWithRoleForPerson(ctx, person.ID)
		if err != nil {
			return nil, ListChannelsOutput{}, fmt.Errorf("list_channels: list channels for person %s: %w", person.ID, err)
		}

		out := ListChannelsOutput{Channels: make([]ChannelAccessOutput, 0, len(channelRoles))}
		for _, cr := range channelRoles {
			out.Channels = append(out.Channels, ChannelAccessOutput{
				ChannelID:       cr.Channel.ID.String(),
				Title:           cr.Channel.Title,
				ConnectionState: string(cr.Channel.ConnectionState),
				Roles:           []string{string(cr.Role)},
			})
		}
		return nil, out, nil
	}
}
