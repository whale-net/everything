// list_channels -- MCP channel-discovery tool (issue #1631): resolves
// which Channels the calling Person can see, and what role(s) they hold on
// each, so an MCP-only client can find/resolve a channel_id without
// dropping to the web UI or asking a human to paste a UUID. Every other
// Channel-scoped tool requires a channel_id argument up front and simply
// rejects an unauthorized caller -- this is the one tool that answers
// "which Channels do I have access to in the first place?". Like whoami,
// it reports the caller's own state and takes no channel_id, so it does
// not implement server.ChannelScoped and is left unscoped by
// server.RegisterRead.
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
	// Roles is a slice, not a single value: LB2 does not assume at most
	// one role per (Channel, Person) pair, mirroring store.RoleStore.
	// RolesFor's own doc comment.
	Roles []string `json:"roles" jsonschema:"Every role the caller currently holds on this Channel -- creator and/or analyst (LB2: a Person may hold more than one)"`
}

// ListChannelsOutput is list_channels' structured result.
type ListChannelsOutput struct {
	Channels []ChannelAccessOutput `json:"channels" jsonschema:"Every Channel the caller currently holds a live role on, ordered by title then id"`
}

// RegisterListChannels registers list_channels via server.RegisterRead.
// Takes no input -- like whoami, it reports the caller's own state, so
// there is nothing to Channel-scope.
func RegisterListChannels(reg *server.Registry, roles store.RoleStore) {
	server.RegisterRead(reg, &mcp.Tool{
		Name: "list_channels",
		Description: "List every Channel the calling Person currently has access to, with that Channel's title, " +
			"connection_state, and the caller's role(s) (creator and/or analyst) on it. This is the discovery entry " +
			"point for every other Channel-scoped tool: use the returned channel_id to call get_channel_overview, " +
			"list_ideas, save_research_note, and the rest.",
	}, listChannelsHandler(roles))
}

func listChannelsHandler(roles store.RoleStore) mcp.ToolHandlerFor[any, ListChannelsOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, ListChannelsOutput, error) {
		person := server.PersonFromContext(ctx)
		if person == nil {
			return nil, ListChannelsOutput{}, fmt.Errorf("unauthenticated: no caller credential resolved")
		}

		channels, err := roles.ChannelsForPerson(ctx, person.ID)
		if err != nil {
			return nil, ListChannelsOutput{}, fmt.Errorf("list_channels: list channels for person %s: %w", person.ID, err)
		}

		out := ListChannelsOutput{Channels: make([]ChannelAccessOutput, 0, len(channels))}
		for _, ch := range channels {
			channelRoles, err := roles.RolesFor(ctx, ch.ID, person.ID)
			if err != nil {
				return nil, ListChannelsOutput{}, fmt.Errorf("list_channels: roles for channel %s: %w", ch.ID, err)
			}

			roleStrs := make([]string, len(channelRoles))
			for i, r := range channelRoles {
				roleStrs[i] = string(r)
			}

			out.Channels = append(out.Channels, ChannelAccessOutput{
				ChannelID:       ch.ID.String(),
				Title:           ch.Title,
				ConnectionState: string(ch.ConnectionState),
				Roles:           roleStrs,
			})
		}
		return nil, out, nil
	}
}
