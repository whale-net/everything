package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// pgxExecer is satisfied by both *pgxpool.Pool and pgx.Tx -- it lets
// addRoleTx run the SCD2 close-and-open pattern either as its own
// transaction (RoleStore.AddRole) or as part of a caller's larger
// transaction (InviteStore.Consume, which must atomically consume the
// code and grant role=analyst -- FR8).
type pgxExecer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// RoleStore covers `channel_person` (migration 001, the LB2 join table).
// This is the ONLY table M1 authorization reads from -- see authz.go's
// CanApprove/CanInvite/CanReconnect/CanRead/CanWrite, the sanctioned entry
// points built on top of it (NFR5).
type RoleStore interface {
	// RolesFor returns every currently-held (valid_to IS NULL) Role for
	// the (channelID, personID) pair -- ordinarily zero or one, but
	// callers must not assume at most one (LB2: no single-Creator
	// assumption is baked in).
	RolesFor(ctx context.Context, channelID, personID uuid.UUID) ([]Role, error)

	// AddRole grants role to personID on channelID, following the SCD2
	// close-and-open pattern from AGENTS.md: closes any existing open row
	// for the same (channelID, personID) pair, then inserts a new open
	// row for role, recording grantedByPersonID on it (FR34) -- the
	// Person who performed the grant (e.g. the invite generator, or the
	// same Person as a Channel-connect self-grant, see channel.go's
	// Create).
	AddRole(ctx context.Context, channelID, personID uuid.UUID, role Role, grantedByPersonID uuid.UUID) error

	// RemoveRole closes personID's open channel_person row on channelID
	// (valid_to = NOW(), revoked_by_person_id = revokedByPersonID) -- the
	// closing half of SCD2 alone; it never inserts a replacement row.
	// Returns removed=false with no error when personID has no open row
	// on channelID, which is what makes FR33's repeat-remove an
	// idempotent no-op success rather than an error. Callers authorize
	// via authz.go's CanRemove before calling this -- RemoveRole itself
	// performs no authorization check.
	RemoveRole(ctx context.Context, channelID, personID, revokedByPersonID uuid.UUID) (removed bool, err error)

	// ChannelsForPerson returns every Channel personID currently holds any
	// role on (creator, co_creator, or analyst) -- reads the open
	// (valid_to IS NULL) channel_person rows for personID joined to
	// channel. This is `web`'s signed-in home page (C1)'s data source for
	// "the Channels the Person has a live channel_person row for" -- see
	// web/main.go's handleHome.
	ChannelsForPerson(ctx context.Context, personID uuid.UUID) ([]Channel, error)
}

// roleStore implements RoleStore against `channel_person` (migration 001).
type roleStore struct{ pool *pgxpool.Pool }

var _ RoleStore = roleStore{}

func (s roleStore) RolesFor(ctx context.Context, channelID, personID uuid.UUID) ([]Role, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT role FROM channel_person
		WHERE channel_id = $1 AND person_id = $2 AND valid_to IS NULL
	`, channelID, personID)
	if err != nil {
		return nil, fmt.Errorf("roles for channel/person: %w", err)
	}
	defer rows.Close()

	var roles []Role
	for rows.Next() {
		var r Role
		if err := rows.Scan(&r); err != nil {
			return nil, fmt.Errorf("scan role: %w", err)
		}
		roles = append(roles, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("roles for channel/person: %w", err)
	}
	return roles, nil
}

// AddRole follows the SCD2 close-and-open pattern from AGENTS.md "SCD2":
// close any open row for (channelID, personID), then insert a new open
// row for role attributed to grantedByPersonID (FR34), in its own
// transaction.
func (s roleStore) AddRole(ctx context.Context, channelID, personID uuid.UUID, role Role, grantedByPersonID uuid.UUID) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := addRoleTx(ctx, tx, channelID, personID, role, grantedByPersonID); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// RemoveRole is the closing half of SCD2 alone (FR34): it closes
// personID's open channel_person row on channelID, stamping
// revoked_by_person_id alongside valid_to, and never inserts a
// replacement row. A zero-row UPDATE (no open row exists) is not an
// error -- it reports removed=false, which is what makes FR33's
// repeat-remove an idempotent no-op success (see authz.go's CanRemove
// doc comment for the authorization half of that split). RemoveRole and
// addRoleTx are the only two write paths that ever touch channel_person
// (NFR6): no other write path may UPDATE or INSERT into it.
func (s roleStore) RemoveRole(ctx context.Context, channelID, personID, revokedByPersonID uuid.UUID) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE channel_person SET valid_to = NOW(), revoked_by_person_id = $3
		WHERE channel_id = $1 AND person_id = $2 AND valid_to IS NULL
	`, channelID, personID, revokedByPersonID)
	if err != nil {
		return false, fmt.Errorf("remove role: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// ChannelsForPerson joins channel_person (open rows only) to channel,
// ordered by title then id for stable rendering on `web`'s home page.
func (s roleStore) ChannelsForPerson(ctx context.Context, personID uuid.UUID) ([]Channel, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT c.id, c.youtube_channel_id, COALESCE(c.title, ''), c.connection_state, c.connection_state_changed_at, c.created_at
		FROM channel c
		JOIN channel_person cp ON cp.channel_id = c.id
		WHERE cp.person_id = $1 AND cp.valid_to IS NULL
		ORDER BY c.title, c.id
	`, personID)
	if err != nil {
		return nil, fmt.Errorf("channels for person: %w", err)
	}
	defer rows.Close()

	var channels []Channel
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, fmt.Errorf("scan channel: %w", err)
		}
		channels = append(channels, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("channels for person: %w", err)
	}
	return channels, nil
}

// addRoleTx is the SCD2 close-and-open pattern from AGENTS.md "SCD2",
// runnable against any pgxExecer so it can be composed into a larger
// caller-owned transaction (see InviteStore.Consume in invite.go).
// grantedByPersonID is written on the new row's granted_by_person_id
// (FR34) -- the Person who performed this grant. addRoleTx and
// RoleStore.RemoveRole are the only two write paths that ever touch
// channel_person (NFR6).
func addRoleTx(ctx context.Context, exec pgxExecer, channelID, personID uuid.UUID, role Role, grantedByPersonID uuid.UUID) error {
	if _, err := exec.Exec(ctx, `
		UPDATE channel_person SET valid_to = NOW()
		WHERE channel_id = $1 AND person_id = $2 AND valid_to IS NULL
	`, channelID, personID); err != nil {
		return fmt.Errorf("close existing role: %w", err)
	}

	if _, err := exec.Exec(ctx, `
		INSERT INTO channel_person (channel_id, person_id, role, granted_by_person_id) VALUES ($1, $2, $3, $4)
	`, channelID, personID, role, grantedByPersonID); err != nil {
		return fmt.Errorf("insert role: %w", err)
	}
	return nil
}
