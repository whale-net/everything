package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

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

// ChannelsWithRoleForPerson joins channel_person (open rows only) to
// channel in one query (NFR9), ordered by title then id -- see the
// interface doc comment above for the exact contract.
func (s accessStore) ChannelsWithRoleForPerson(ctx context.Context, personID uuid.UUID) ([]ChannelRole, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT c.id, c.youtube_channel_id, COALESCE(c.title, ''), c.connection_state, c.connection_state_changed_at, c.created_at, cp.role
		FROM channel c
		JOIN channel_person cp ON cp.channel_id = c.id
		WHERE cp.person_id = $1 AND cp.valid_to IS NULL
		ORDER BY c.title, c.id
	`, personID)
	if err != nil {
		return nil, fmt.Errorf("channels with role for person: %w", err)
	}
	defer rows.Close()

	var out []ChannelRole
	for rows.Next() {
		var cr ChannelRole
		if err := rows.Scan(
			&cr.Channel.ID, &cr.Channel.YouTubeChannelID, &cr.Channel.Title,
			&cr.Channel.ConnectionState, &cr.Channel.ConnectionStateChangedAt, &cr.Channel.CreatedAt,
			&cr.Role,
		); err != nil {
			return nil, fmt.Errorf("scan channel role: %w", err)
		}
		out = append(out, cr)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("channels with role for person: %w", err)
	}
	return out, nil
}

// Roster joins channel_person (open rows only) to person twice -- once for
// the subject, once (LEFT JOIN, since granted_by_person_id can be NULL on
// pre-M2 rows) for the granter -- in one query (NFR9). The ORDER BY's CASE
// expression is a presentation-only tier ordering local to this query, not
// a rank column in the schema or a comparable Go type (NFR7).
func (s accessStore) Roster(ctx context.Context, channelID uuid.UUID) ([]RosterEntry, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT p.id, COALESCE(p.display_name, ''), COALESCE(p.email, ''), cp.role, cp.valid_from, COALESCE(granter.display_name, '')
		FROM channel_person cp
		JOIN person p ON p.id = cp.person_id
		LEFT JOIN person granter ON granter.id = cp.granted_by_person_id
		WHERE cp.channel_id = $1 AND cp.valid_to IS NULL
		ORDER BY CASE cp.role WHEN 'creator' THEN 0 WHEN 'co_creator' THEN 1 ELSE 2 END, p.display_name
	`, channelID)
	if err != nil {
		return nil, fmt.Errorf("roster: %w", err)
	}
	defer rows.Close()

	var out []RosterEntry
	for rows.Next() {
		var re RosterEntry
		if err := rows.Scan(&re.PersonID, &re.DisplayName, &re.Email, &re.Role, &re.GrantedAt, &re.GrantedByDisplayName); err != nil {
			return nil, fmt.Errorf("scan roster entry: %w", err)
		}
		out = append(out, re)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("roster: %w", err)
	}
	return out, nil
}

// AuditTrail selects from v_channel_person_audit (migration 009) --
// deliberately not a hand-rolled re-derivation of its grant/revoke union
// (see the package-level doc comment above). limit <= 0 means no LIMIT
// clause at all.
func (s accessStore) AuditTrail(ctx context.Context, channelID uuid.UUID, limit int) ([]AuditEvent, error) {
	query := `
		SELECT event, occurred_at, subject_person_id, COALESCE(subject_display_name, ''), role, actor_person_id, COALESCE(actor_display_name, '')
		FROM v_channel_person_audit
		WHERE channel_id = $1
		ORDER BY occurred_at DESC, event
	`
	args := []any{channelID}
	if limit > 0 {
		query += ` LIMIT $2`
		args = append(args, limit)
	}

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("audit trail: %w", err)
	}
	defer rows.Close()

	var out []AuditEvent
	for rows.Next() {
		var ev AuditEvent
		if err := rows.Scan(&ev.Event, &ev.OccurredAt, &ev.SubjectPersonID, &ev.SubjectDisplayName, &ev.Role, &ev.ActorPersonID, &ev.ActorDisplayName); err != nil {
			return nil, fmt.Errorf("scan audit event: %w", err)
		}
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("audit trail: %w", err)
	}
	return out, nil
}
