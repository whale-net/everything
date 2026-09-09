// Package session is whagent-net's Postgres-backed store over the schema
// `whagent_net/migrate/migrations/001_initial_schema.up.sql` creates (see
// issue #2109 and whagent_net/ARCHITECTURE.md "Three nouns: session,
// transcript, context" / "Component map"). Store is the single entry
// point, built over //libs/go/db's *pgxpool.Pool -- same shape as
// audience_score_system/store.Store: Sessions/Transcript/
// AgentDefinitions/Usage/Idempotency hand back the per-concern store
// implementations (sessions.go, transcript.go, agentdef.go, usage.go,
// idempotency.go), kept as separate concrete types rather than all
// methods on Store itself.
//
// Implementation phase (#2109): every store below is a real Postgres
// implementation against migration 001's tables. UpdateStatus and
// AssignToSession are the two compare-and-swap/SCD2 write paths worth
// reading first (sessions.go, agentdef.go) -- everything else is a
// straightforward INSERT/SELECT/UPDATE.
package session

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/whagent_net/events"
)

// Store is the pgx-backed repository over `sessions`, `transcript_event`,
// `turn_context`, `turn_usage`, `agent_definition`, `session_agent`,
// `tool_call_idempotency` (migration 001, issue #2109), and
// `transcript_archive` (migration 002, issue #2240).
type Store struct {
	pool *pgxpool.Pool
	pub  events.PublisherInterface
}

// New returns a Store backed by pool (see //libs/go/db.NewPool). pub is the
// whagent_net/events publisher the Transcript store's Append publishes
// committed events to after each Postgres commit (NFR2); pass nil to
// disable publishing entirely (e.g. no RABBITMQ_URL configured) -- Append
// still works, it simply skips the publish step.
func New(pool *pgxpool.Pool, pub events.PublisherInterface) *Store {
	return &Store{pool: pool, pub: pub}
}

// Sessions returns the SessionStore implementation (sessions.go, LB2/NFR3).
func (s *Store) Sessions() SessionStore { return sessionStore{pool: s.pool} }

// Transcript returns the TranscriptStore implementation (transcript.go,
// LB1).
func (s *Store) Transcript() TranscriptStore { return transcriptStore{pool: s.pool, pub: s.pub} }

// AgentDefinitions returns the AgentDefinitionStore implementation
// (agentdef.go, LB5/NFR6).
func (s *Store) AgentDefinitions() AgentDefinitionStore { return agentDefinitionStore{pool: s.pool} }

// Usage returns the UsageStore implementation (usage.go, LB6).
func (s *Store) Usage() UsageStore { return usageStore{pool: s.pool} }

// Idempotency returns the IdempotencyLedger implementation
// (idempotency.go, LB4).
func (s *Store) Idempotency() IdempotencyLedger { return idempotencyLedgerStore{pool: s.pool} }

// Archive returns the ArchiveStore implementation (archive.go, issue
// #2240, FR8/LB1): the `transcript_archive` cold-tier index FR7's archiver
// writes and Transcript()'s tier-transparent Read consults.
func (s *Store) Archive() ArchiveStore { return archiveStore{pool: s.pool} }
