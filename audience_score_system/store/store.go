// Package store is the pgx-based repository for Audience Score System's
// identity core: person, channel, the Persona<->Channel join table (LB2),
// and channel_invite codes -- migration 001, see
// audience_score_system/migrate/schema/migrations/001_identity.up.sql.
//
// Store is the single entry point, built over //libs/go/db's
// *pgxpool.Pool. Its Persons/Channels/Roles/Invites accessors hand back
// the per-entity Store implementations (PersonStore/ChannelStore/
// RoleStore/InviteStore) -- kept as separate concrete types, not all
// methods on Store itself, because PersonStore.GetByID and
// ChannelStore.GetByID share a method name but return different types; a
// single receiver type cannot implement both.
//
// Every authorization question in M1 is answered ONLY by
// CanApprove/CanInvite/CanReconnect/CanRead/CanWrite (see authz.go) --
// reading `role` off the open (valid_to IS NULL) channel_person row for a
// (channel_id, person_id) pair. No handler, tool, or workflow outside this
// package may reconstruct a role check from raw SQL (NFR5): there is no
// channel.owner_id column, and nothing here assumes a Channel has exactly
// one Creator.
//
// Scaffold only (issue #1568): every method on the per-entity store types
// is a stub returning errors.New("not implemented"). Full implementation
// lands in the Implementation phase.
package store

import "github.com/jackc/pgx/v5/pgxpool"

// Store is the pgx-backed repository over person/channel/channel_person/
// channel_invite (migration 001).
type Store struct {
	pool *pgxpool.Pool
}

// New returns a Store backed by pool (see //libs/go/db.NewPool).
func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Persons returns the PersonStore implementation.
func (s *Store) Persons() PersonStore { return personStore{pool: s.pool} }

// Channels returns the ChannelStore implementation.
func (s *Store) Channels() ChannelStore { return channelStore{pool: s.pool} }

// Roles returns the RoleStore implementation.
func (s *Store) Roles() RoleStore { return roleStore{pool: s.pool} }

// Invites returns the InviteStore implementation.
func (s *Store) Invites() InviteStore { return inviteStore{pool: s.pool} }
