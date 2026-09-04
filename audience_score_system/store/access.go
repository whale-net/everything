package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrAccessNotImplemented is returned by every AccessStore method until
// Implementation wires in the real queries -- same scaffold/feat split
// other store methods in this package have followed.
var ErrAccessNotImplemented = errors.New("store: access read model not implemented")

// AccessStore is M2's read side of the access model (migration 009, issue
// #1716): "which Channels does this Person have access to and at what
// tier" (FR26), a Channel's current roster (FR30/FR31/FR33), and its
// grant/revoke audit trail over v_channel_person_audit (FR35).
//
// AccessStore performs NO authorization itself -- it is a plain read
// model over channel_person/person/v_channel_person_audit. Callers MUST
// gate Roster with store.CanRead and AuditTrail with store.CanViewAudit
// (authz.go) before calling; AccessStore is not a security boundary
// (NFR5).
type AccessStore interface {
	// ChannelsWithRoleForPerson returns every Channel personID currently
	// holds an open role on (migration 001's channel_person, valid_to IS
	// NULL), paired with that role. Exactly one row per open
	// channel_person row: migration 001's
	// channel_person_channel_id_person_id_current partial unique index
	// guarantees at most one open (channel_id, person_id) row, so this
	// never returns two ChannelRole entries for the same Channel. One
	// JOIN query (NFR9) -- this is FR26's Channel list/switcher data
	// source, and the intended replacement for the N+1 loop in
	// mcp/tools/list_channels.go (a separate RolesFor call per Channel;
	// issue #1719 repoints that call site here). Ordered by channel title
	// then id, matching RoleStore.ChannelsForPerson's existing order.
	ChannelsWithRoleForPerson(ctx context.Context, personID uuid.UUID) ([]ChannelRole, error)

	// Roster returns every Person holding an open role on channelID,
	// ordered Founder first, then Co-Creator, then Analyst, each group by
	// display name -- the access-management page's list (FR30/FR31/
	// FR33). One query, joining channel_person to person (subject) and
	// LEFT JOINed to person (granter) for GrantedByDisplayName, which is
	// "" for pre-M2 rows (migration 009 does not backfill
	// granted_by_person_id). Callers must gate this with store.CanRead.
	Roster(ctx context.Context, channelID uuid.UUID) ([]RosterEntry, error)

	// AuditTrail returns channelID's grant/revoke history from
	// v_channel_person_audit (migration 009, FR35), most-recent-first. A
	// limit <= 0 means no limit. ActorPersonID/ActorDisplayName are nil/
	// "" for rows with no recorded actor (pre-M2 rows, and grants that
	// predate migration 009's attribution columns) -- render "unknown"
	// upstream rather than inventing one. Callers must gate this with
	// store.CanViewAudit.
	AuditTrail(ctx context.Context, channelID uuid.UUID, limit int) ([]AuditEvent, error)
}

// accessStore implements AccessStore against `channel`/`channel_person`/
// `person` (migration 001) and the `v_channel_person_audit` view
// (migration 009). It never re-derives v_channel_person_audit's
// grant/revoke union in Go or in a hand-rolled join -- see that view's SQL
// comment (migrate/schema/migrations/009_co_creator_tier.up.sql) and
// AGENTS.md's SCD2 "Views" convention for why the join lives there once.
type accessStore struct{ pool *pgxpool.Pool }

var _ AccessStore = accessStore{}

func (s accessStore) ChannelsWithRoleForPerson(ctx context.Context, personID uuid.UUID) ([]ChannelRole, error) {
	return nil, ErrAccessNotImplemented
}

func (s accessStore) Roster(ctx context.Context, channelID uuid.UUID) ([]RosterEntry, error) {
	return nil, ErrAccessNotImplemented
}

func (s accessStore) AuditTrail(ctx context.Context, channelID uuid.UUID, limit int) ([]AuditEvent, error) {
	return nil, ErrAccessNotImplemented
}
