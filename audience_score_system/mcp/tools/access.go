// M2's access-management MCP write tool group (issue #1718, NFR3's
// dual-surface rule): invite_co_creator (FR30), promote_to_co_creator
// (FR31), and remove_channel_person (FR33) -- the MCP half of access
// management, mirroring web/invite and (once built) web's access page.
// Every tool here performs its OWN stricter authorization check inside
// mutate (store.CanInvite or store.CanRemove, ../../store/authz.go from
// #1714) rather than relying on server.RegisterWrite's automatic
// ChannelScoped gate, which only enforces store.CanWrite (Creator, Co-
// Creator, or Analyst) -- far too permissive for granting/revoking a
// role. See ../../store/invite.go (InviteStore, #1715) for the per-tier
// Generate/Consume API invite_co_creator wraps, and ../../store/role.go
// (RoleStore) for AddRole/RemoveRole/RowID/RowByID.
//
// # ref encoding (LB4)
//
// server.WriteMutate returns a single uuid.UUID "ref"; server.WriteRender
// must reconstruct the full response from that ref ALONE (see
// ../server/registry.go's RegisterWrite doc comment) -- including on an
// idempotency-key replay, when mutate does not run again at all. Every
// tool below needs its response to report not just an entity's current
// state but also a BOOLEAN mutate alone knows (already_live/changed/
// removed: did THIS call's mutate actually change something, or was it a
// no-op against something already true). Since render never sees
// anything mutate computed beyond ref, that boolean has to be encoded
// INTO ref itself. Every tool here does this via flipRefBit: a real
// entity id (invite id for invite_co_creator; channel_person row id for
// promote_to_co_creator/remove_channel_person) with one fixed bit
// flipped to mark the "unchanged" outcome. render tries the id
// unflipped first, then flipped, and whichever lookup succeeds tells it
// both the entity's current state AND which boolean value to report --
// no wall-clock heuristics, no process-local cache (LB4 forbids both).
//
// remove_channel_person has one narrower case flipRefBit cannot cover:
// removing a Person who has NEVER held any role on this Channel (no
// channel_person row, open or closed, exists at all for that pair).
// There ref falls back to the bare target person id, and render cannot
// re-derive channel_id from that alone (a person id does not identify
// which Channel a long-past call meant) -- removeChannelPersonRender's
// doc comment covers this narrow, documented gap.
package tools

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/audience_score_system/mcp/server"
	"github.com/whale-net/everything/audience_score_system/store"
)

// flipRefBit toggles a single fixed bit of id and returns the result --
// a fully reversible (flipRefBit(flipRefBit(x)) == x), collision-free-in-
// practice way to mark a real entity id as the "unchanged/no-op" outcome
// rather than the "changed" one, so render can recover both the entity
// AND that boolean from one uuid.UUID. See this file's doc comment's
// "ref encoding" section.
func flipRefBit(id uuid.UUID) uuid.UUID {
	id[0] ^= 0x80
	return id
}

// hasAccessRole reports whether roles contains want -- a plain set
// membership check, mirroring store/authz.go's containsRole (unexported
// there) for this file's own use.
func hasAccessRole(roles []store.Role, want store.Role) bool {
	for _, r := range roles {
		if r == want {
			return true
		}
	}
	return false
}

// -- invite_co_creator (FR30) -------------------------------------------------

// InviteCoCreatorInput is invite_co_creator's argument schema.
type InviteCoCreatorInput struct {
	ChannelID string `json:"channel_id" jsonschema:"Channel to invite a Co-Creator to, as a UUID string"`
	// IdempotencyKeyArg backs IdempotencyKey() below -- see
	// schedule_draft.go's identical naming rationale.
	IdempotencyKeyArg string `json:"idempotency_key,omitempty" jsonschema:"optional caller-supplied idempotency key; not required -- InviteStore.Generate's own (channel, tier) natural key already converges repeated calls onto the same live code (NFR11)"`
}

// ChannelScopeID implements server.ChannelScoped.
func (i InviteCoCreatorInput) ChannelScopeID() uuid.UUID {
	id, _ := uuid.Parse(i.ChannelID)
	return id
}

// IdempotencyKey implements server.IdempotencyKeyed.
func (i InviteCoCreatorInput) IdempotencyKey() string { return i.IdempotencyKeyArg }

// InviteCoCreatorOutput is invite_co_creator's structured result.
type InviteCoCreatorOutput struct {
	Code        string `json:"code" jsonschema:"the invite code to share with the Person being invited"`
	Role        string `json:"role" jsonschema:"always co_creator"`
	ChannelID   string `json:"channel_id" jsonschema:"the Channel this invite grants access to, as a UUID string"`
	CreatedAt   string `json:"created_at" jsonschema:"when this invite code was created, RFC3339"`
	AlreadyLive bool   `json:"already_live" jsonschema:"true if a live Co-Creator invite already existed on this Channel and this call returned it unchanged rather than minting a new one (NFR11); a live Analyst invite on the same Channel is a separate tier and is never affected either way"`
}

// registerInviteCoCreator registers invite_co_creator via
// server.RegisterWrite. RegisterWrite's automatic ChannelScoped gate only
// enforces store.CanWrite (Creator, Co-Creator, or Analyst) -- FR30
// requires the stricter store.CanInvite (Founder or Co-Creator, FR32), so
// inviteCoCreatorMutate checks it explicitly before calling
// store.InviteStore.Generate.
func registerInviteCoCreator(reg *server.Registry, invites store.InviteStore, roles store.RoleStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name: "invite_co_creator",
		Description: "Generate (or return the already-live) Co-Creator invite code for a Channel (FR30). Founder or " +
			"Co-Creator only (store.CanInvite, FR32) -- an Analyst calling this is rejected with a permission error. " +
			"Idempotent per (Channel, co_creator tier) (NFR11): a live Co-Creator invite already on this Channel is " +
			"returned unchanged (already_live=true) rather than a new code being minted; a live Analyst invite on the " +
			"same Channel is a separate tier and is left untouched either way. idempotency_key is accepted for " +
			"uniformity but not required.",
	}, inviteCoCreatorMutate(invites, roles), inviteCoCreatorRender(invites))
}

func inviteCoCreatorMutate(invites store.InviteStore, roles store.RoleStore) server.WriteMutate[InviteCoCreatorInput] {
	return func(ctx context.Context, in InviteCoCreatorInput) (uuid.UUID, error) {
		channelID, err := uuid.Parse(in.ChannelID)
		if err != nil {
			return uuid.Nil, fmt.Errorf("channel_id is not a valid UUID: %w", err)
		}

		person := server.PersonFromContext(ctx)
		if person == nil {
			return uuid.Nil, fmt.Errorf("unauthenticated: no caller credential resolved")
		}

		canInvite, err := store.CanInvite(ctx, roles, channelID, person.ID)
		if err != nil {
			return uuid.Nil, fmt.Errorf("check invite authority: %w", err)
		}
		if !canInvite {
			return uuid.Nil, fmt.Errorf("permission denied: only a Channel's Founder or Co-Creator may invite a Co-Creator (FR30/FR32)")
		}

		// Peek before Generate to learn already_live -- see
		// InviteStore.LiveForRole's doc comment on the small, accepted
		// race this has against a concurrent Generate for the same
		// (channel, role): advisory only, never load-bearing for
		// authorization or for NFR11's "at most one live code" guarantee,
		// which Generate's own transaction enforces regardless.
		_, hadLive, err := invites.LiveForRole(ctx, channelID, store.RoleCoCreator)
		if err != nil {
			return uuid.Nil, fmt.Errorf("check existing invite: %w", err)
		}

		inv, err := invites.Generate(ctx, channelID, person.ID, store.RoleCoCreator)
		if err != nil {
			return uuid.Nil, err
		}

		ref := inv.ID
		if hadLive {
			ref = flipRefBit(ref)
		}
		return ref, nil
	}
}

// inviteCoCreatorRender re-derives the response from ref alone (LB4),
// including on an idempotency-key replay when mutate never runs: it
// first tries ref as the invite's own id directly (the "freshly minted"
// outcome), then, if that misses, as that id with flipRefBit's bit
// flipped (the "already_live" outcome) -- see this file's doc comment.
func inviteCoCreatorRender(invites store.InviteStore) server.WriteRender[InviteCoCreatorOutput] {
	return func(ctx context.Context, ref uuid.UUID) (*mcp.CallToolResult, InviteCoCreatorOutput, error) {
		if inv, err := invites.GetByID(ctx, ref); err == nil {
			return nil, toInviteCoCreatorOutput(inv, false), nil
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return nil, InviteCoCreatorOutput{}, fmt.Errorf("load generated invite: %w", err)
		}

		inv, err := invites.GetByID(ctx, flipRefBit(ref))
		if err != nil {
			return nil, InviteCoCreatorOutput{}, fmt.Errorf("load existing invite: %w", err)
		}
		return nil, toInviteCoCreatorOutput(inv, true), nil
	}
}

func toInviteCoCreatorOutput(inv store.Invite, alreadyLive bool) InviteCoCreatorOutput {
	return InviteCoCreatorOutput{
		Code:        inv.Code,
		Role:        string(inv.Role),
		ChannelID:   inv.ChannelID.String(),
		CreatedAt:   inv.CreatedAt.Format(time.RFC3339),
		AlreadyLive: alreadyLive,
	}
}

// -- promote_to_co_creator (FR31) ---------------------------------------------

// PromoteToCoCreatorInput is promote_to_co_creator's argument schema.
type PromoteToCoCreatorInput struct {
	ChannelID         string `json:"channel_id" jsonschema:"Channel the target Person belongs to, as a UUID string"`
	PersonID          string `json:"person_id" jsonschema:"the Analyst to promote to Co-Creator, as a UUID string"`
	IdempotencyKeyArg string `json:"idempotency_key,omitempty" jsonschema:"optional caller-supplied idempotency key; not required -- promoting an already-promoted Co-Creator converges to the same result (changed=false)"`
}

// ChannelScopeID implements server.ChannelScoped.
func (i PromoteToCoCreatorInput) ChannelScopeID() uuid.UUID {
	id, _ := uuid.Parse(i.ChannelID)
	return id
}

// IdempotencyKey implements server.IdempotencyKeyed.
func (i PromoteToCoCreatorInput) IdempotencyKey() string { return i.IdempotencyKeyArg }

// PromoteToCoCreatorOutput is promote_to_co_creator's structured result.
type PromoteToCoCreatorOutput struct {
	ChannelID string `json:"channel_id" jsonschema:"the Channel this promotion applies to, as a UUID string"`
	PersonID  string `json:"person_id" jsonschema:"the promoted Person, as a UUID string"`
	Role      string `json:"role" jsonschema:"always co_creator on success"`
	Changed   bool   `json:"changed" jsonschema:"true if this call actually promoted the target from analyst to co_creator; false if they already held co_creator -- an idempotent no-op (FR31), not an error"`
}

// registerPromoteToCoCreator registers promote_to_co_creator via
// server.RegisterWrite. Like invite_co_creator, this checks
// store.CanInvite explicitly (Founder or Co-Creator, FR32) rather than
// relying on RegisterWrite's automatic store.CanWrite gate, since
// promoting someone to Co-Creator needs the same authority as inviting
// one (FR31).
func registerPromoteToCoCreator(reg *server.Registry, roles store.RoleStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name: "promote_to_co_creator",
		Description: "Promote an existing Analyst on a Channel to Co-Creator, without them going through the " +
			"invite-code flow again (FR31). Founder or Co-Creator only (store.CanInvite, FR32) -- an Analyst calling " +
			"this is rejected with a permission error. Idempotent: promoting a Person who already holds co_creator " +
			"returns success with changed=false rather than an error. Rejected as a permission error, with no change, " +
			"if person_id currently holds creator (no path re-tiers or demotes a Founder) or holds no role at all on " +
			"this Channel (only an existing Analyst may be promoted).",
	}, promoteToCoCreatorMutate(roles), promoteToCoCreatorRender(roles))
}

func promoteToCoCreatorMutate(roles store.RoleStore) server.WriteMutate[PromoteToCoCreatorInput] {
	return func(ctx context.Context, in PromoteToCoCreatorInput) (uuid.UUID, error) {
		channelID, err := uuid.Parse(in.ChannelID)
		if err != nil {
			return uuid.Nil, fmt.Errorf("channel_id is not a valid UUID: %w", err)
		}
		targetID, err := uuid.Parse(in.PersonID)
		if err != nil {
			return uuid.Nil, fmt.Errorf("person_id is not a valid UUID: %w", err)
		}

		person := server.PersonFromContext(ctx)
		if person == nil {
			return uuid.Nil, fmt.Errorf("unauthenticated: no caller credential resolved")
		}

		canInvite, err := store.CanInvite(ctx, roles, channelID, person.ID)
		if err != nil {
			return uuid.Nil, fmt.Errorf("check promote authority: %w", err)
		}
		if !canInvite {
			return uuid.Nil, fmt.Errorf("permission denied: only a Channel's Founder or Co-Creator may promote a Co-Creator (FR31/FR32)")
		}

		targetRoles, err := roles.RolesFor(ctx, channelID, targetID)
		if err != nil {
			return uuid.Nil, fmt.Errorf("load target's roles: %w", err)
		}

		switch {
		case hasAccessRole(targetRoles, store.RoleCoCreator):
			// Idempotent no-op (FR31): already co_creator, nothing to
			// change. ref = the existing open row's id, flipped to mark
			// changed=false on render.
			rowID, found, err := roles.RowID(ctx, channelID, targetID)
			if err != nil {
				return uuid.Nil, fmt.Errorf("load existing co_creator row: %w", err)
			}
			if !found {
				return uuid.Nil, fmt.Errorf("target holds co_creator but has no channel_person row -- inconsistent state")
			}
			return flipRefBit(rowID), nil
		case hasAccessRole(targetRoles, store.RoleCreator):
			return uuid.Nil, fmt.Errorf("permission denied: cannot promote a Channel's Founder -- no path re-tiers or demotes a Founder (FR31)")
		case hasAccessRole(targetRoles, store.RoleAnalyst):
			if err := roles.AddRole(ctx, channelID, targetID, store.RoleCoCreator, person.ID); err != nil {
				return uuid.Nil, fmt.Errorf("promote to co_creator: %w", err)
			}
			rowID, found, err := roles.RowID(ctx, channelID, targetID)
			if err != nil {
				return uuid.Nil, fmt.Errorf("load newly promoted row: %w", err)
			}
			if !found {
				return uuid.Nil, fmt.Errorf("promote to co_creator: no channel_person row found immediately after grant")
			}
			return rowID, nil
		default:
			return uuid.Nil, fmt.Errorf("permission denied: person_id is not currently a member of this Channel -- only an existing Analyst may be promoted (FR31)")
		}
	}
}

// promoteToCoCreatorRender re-derives the response from ref alone (LB4):
// it tries ref as a channel_person row id directly (the "freshly
// promoted" outcome, changed=true), then flipped (the "already
// co_creator" no-op outcome, changed=false) -- see this file's doc
// comment. A successful promote_to_co_creator call always has an open
// co_creator row to find one of these two ways; a miss on both is an
// error, not a third, degenerate case.
func promoteToCoCreatorRender(roles store.RoleStore) server.WriteRender[PromoteToCoCreatorOutput] {
	return func(ctx context.Context, ref uuid.UUID) (*mcp.CallToolResult, PromoteToCoCreatorOutput, error) {
		if row, err := roles.RowByID(ctx, ref); err == nil {
			return nil, toPromoteToCoCreatorOutput(row, true), nil
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return nil, PromoteToCoCreatorOutput{}, fmt.Errorf("load promoted channel_person row: %w", err)
		}

		row, err := roles.RowByID(ctx, flipRefBit(ref))
		if err != nil {
			return nil, PromoteToCoCreatorOutput{}, fmt.Errorf("load existing co_creator row: %w", err)
		}
		return nil, toPromoteToCoCreatorOutput(row, false), nil
	}
}

func toPromoteToCoCreatorOutput(row store.ChannelPerson, changed bool) PromoteToCoCreatorOutput {
	return PromoteToCoCreatorOutput{
		ChannelID: row.ChannelID.String(),
		PersonID:  row.PersonID.String(),
		Role:      string(row.Role),
		Changed:   changed,
	}
}

// -- remove_channel_person (FR33) ---------------------------------------------

// RemoveChannelPersonInput is remove_channel_person's argument schema.
type RemoveChannelPersonInput struct {
	ChannelID         string `json:"channel_id" jsonschema:"Channel to remove the Person's role from, as a UUID string"`
	PersonID          string `json:"person_id" jsonschema:"the Person to remove, as a UUID string"`
	IdempotencyKeyArg string `json:"idempotency_key,omitempty" jsonschema:"optional caller-supplied idempotency key; not required -- removing an already-removed Person converges to the same result (removed=false)"`
}

// ChannelScopeID implements server.ChannelScoped.
func (i RemoveChannelPersonInput) ChannelScopeID() uuid.UUID {
	id, _ := uuid.Parse(i.ChannelID)
	return id
}

// IdempotencyKey implements server.IdempotencyKeyed.
func (i RemoveChannelPersonInput) IdempotencyKey() string { return i.IdempotencyKeyArg }

// RemoveChannelPersonOutput is remove_channel_person's structured result.
// ChannelID is "" in the one narrow case documented on
// removeChannelPersonRender: removing a Person who has never held any
// role on this Channel at all.
type RemoveChannelPersonOutput struct {
	ChannelID string `json:"channel_id" jsonschema:"the Channel this removal applies to, as a UUID string; empty in the narrow case where person_id never held any role on this Channel at all (see removed)"`
	PersonID  string `json:"person_id" jsonschema:"the target Person, as a UUID string"`
	Removed   bool   `json:"removed" jsonschema:"true if this call actually revoked an open role; false if the target already held no open role on this Channel -- an idempotent no-op (FR33), not an error"`
}

// registerRemoveChannelPerson registers remove_channel_person via
// server.RegisterWrite. RegisterWrite's automatic ChannelScoped gate only
// enforces store.CanWrite -- FR33's removal matrix is enforced explicitly
// via store.CanRemove (#1714) instead.
func registerRemoveChannelPerson(reg *server.Registry, roles store.RoleStore, persons store.PersonStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name: "remove_channel_person",
		Description: "Remove a Person's role from a Channel (FR33), per the removal matrix: a Founder may remove a " +
			"Co-Creator or an Analyst; a Co-Creator may remove an Analyst only. No action ever removes a Founder, " +
			"including a Founder removing themselves. Idempotent: removing a Person who already holds no open role " +
			"on this Channel returns success with removed=false, not an error, and makes no change to " +
			"channel_person. An unauthorized removal attempt (e.g. an Analyst calling this, or a Co-Creator " +
			"targeting a Founder or another Co-Creator) is rejected with a permission error and makes no change " +
			"either.",
	}, removeChannelPersonMutate(roles), removeChannelPersonRender(roles, persons))
}

func removeChannelPersonMutate(roles store.RoleStore) server.WriteMutate[RemoveChannelPersonInput] {
	return func(ctx context.Context, in RemoveChannelPersonInput) (uuid.UUID, error) {
		channelID, err := uuid.Parse(in.ChannelID)
		if err != nil {
			return uuid.Nil, fmt.Errorf("channel_id is not a valid UUID: %w", err)
		}
		targetID, err := uuid.Parse(in.PersonID)
		if err != nil {
			return uuid.Nil, fmt.Errorf("person_id is not a valid UUID: %w", err)
		}

		person := server.PersonFromContext(ctx)
		if person == nil {
			return uuid.Nil, fmt.Errorf("unauthenticated: no caller credential resolved")
		}

		canRemove, err := store.CanRemove(ctx, roles, channelID, person.ID, targetID)
		if err != nil {
			return uuid.Nil, fmt.Errorf("check remove authority: %w", err)
		}

		// existingRowID is the most recent channel_person row (open or
		// closed) for this exact pair, fetched before any write below --
		// when canRemove is true, this IS the open row RemoveRole is
		// about to close, so it doubles as "the row this call affected".
		existingRowID, existingFound, err := roles.RowID(ctx, channelID, targetID)
		if err != nil {
			return uuid.Nil, fmt.Errorf("load target's channel_person row: %w", err)
		}

		if !canRemove {
			// store.CanRemove's doc comment: false means EITHER "target
			// holds no open role" (FR33's idempotent no-op -- success,
			// not an error) OR "actor may not remove target's role" (a
			// real authorization error). Distinguish exactly as
			// prescribed: a separate RolesFor call on the target alone.
			targetRoles, rfErr := roles.RolesFor(ctx, channelID, targetID)
			if rfErr != nil {
				return uuid.Nil, fmt.Errorf("load target's roles: %w", rfErr)
			}
			if len(targetRoles) == 0 {
				if existingFound {
					return flipRefBit(existingRowID), nil
				}
				// No channel_person row exists at all for this pair --
				// see removeChannelPersonRender's doc comment on this
				// narrow fallback.
				return targetID, nil
			}
			return uuid.Nil, fmt.Errorf("permission denied: not authorized to remove this Person's role on this Channel (FR33)")
		}

		removed, err := roles.RemoveRole(ctx, channelID, targetID, person.ID)
		if err != nil {
			return uuid.Nil, fmt.Errorf("remove role: %w", err)
		}
		if !removed || !existingFound {
			// Should not happen given canRemove was true (which implies
			// an open role, hence a row, existed) -- fall back safely
			// rather than ever panicking on an inconsistent read.
			return targetID, nil
		}
		return existingRowID, nil
	}
}

// removeChannelPersonRender re-derives the response from ref alone
// (LB4): it tries ref as a channel_person row id directly (the "just
// removed" outcome, removed=true), then flipped (a row exists but was
// already closed before this call -- the "already removed" no-op,
// removed=false) -- see this file's doc comment.
//
// If NEITHER lookup finds a row, ref is the bare target person id: the
// narrowest no-op case, where person_id has never held ANY role (open or
// closed) on ANY Channel-person pair matching this call, ever. There is
// no channel_person row to anchor a fresh channel_id read to, and a
// person id alone cannot identify which Channel a (possibly much later,
// idempotency-key-replayed) call meant -- so channel_id is reported as
// "" in this one case. removed=false and person_id remain correct
// either way, which is what FR33's idempotency contract actually
// requires; channel_id here is a documented, narrow rendering gap, not a
// correctness or authorization concern.
func removeChannelPersonRender(roles store.RoleStore, persons store.PersonStore) server.WriteRender[RemoveChannelPersonOutput] {
	return func(ctx context.Context, ref uuid.UUID) (*mcp.CallToolResult, RemoveChannelPersonOutput, error) {
		if row, err := roles.RowByID(ctx, ref); err == nil {
			return nil, RemoveChannelPersonOutput{ChannelID: row.ChannelID.String(), PersonID: row.PersonID.String(), Removed: true}, nil
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return nil, RemoveChannelPersonOutput{}, fmt.Errorf("load removed channel_person row: %w", err)
		}

		if row, err := roles.RowByID(ctx, flipRefBit(ref)); err == nil {
			return nil, RemoveChannelPersonOutput{ChannelID: row.ChannelID.String(), PersonID: row.PersonID.String(), Removed: false}, nil
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return nil, RemoveChannelPersonOutput{}, fmt.Errorf("load channel_person row: %w", err)
		}

		person, err := persons.GetByID(ctx, ref)
		if err != nil {
			return nil, RemoveChannelPersonOutput{}, fmt.Errorf("load target person: %w", err)
		}
		return nil, RemoveChannelPersonOutput{PersonID: person.ID.String(), Removed: false}, nil
	}
}

// -- registration ------------------------------------------------------------

// RegisterAccess registers invite_co_creator, promote_to_co_creator, and
// remove_channel_person against reg (see ../server/registry.go), backed
// by st's InviteStore/RoleStore/PersonStore.
func RegisterAccess(reg *server.Registry, st *store.Store) {
	registerInviteCoCreator(reg, st.Invites(), st.Roles())
	registerPromoteToCoCreator(reg, st.Roles())
	registerRemoveChannelPerson(reg, st.Roles(), st.Persons())
}
