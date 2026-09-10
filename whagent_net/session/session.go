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

	"github.com/whale-net/everything/libs/go/s3"
	"github.com/whale-net/everything/whagent_net/events"
)

// Store is the pgx-backed repository over `sessions`, `transcript_event`,
// `turn_context`, `turn_usage`, `agent_definition`, `session_agent`,
// `tool_call_idempotency` (migration 001, issue #2109), `transcript_archive`
// (migration 002, issue #2240), and `model_definition` (migration 006).
type Store struct {
	pool *pgxpool.Pool
	pub  events.PublisherInterface
	s3   *s3.Client
}

// Option configures optional Store dependencies not every caller needs --
// New's positional pool/pub parameters cover what every caller needs.
type Option func(*Store)

// WithS3 attaches an S3 client Transcript()'s tier-transparent Read (issue
// #2240, FR8) uses to hydrate a session's archived transcript once
// `transcript_archive` has a row for it. Omit this option (the default) to
// get a hot-only TranscriptStore -- exactly `worker`'s configuration, which
// never needs to hydrate cold objects; `api` is the only caller expected to
// pass it, since it is the only ReadTranscript-serving process.
func WithS3(client *s3.Client) Option {
	return func(s *Store) { s.s3 = client }
}

// New returns a Store backed by pool (see //libs/go/db.NewPool). pub is the
// whagent_net/events publisher the Transcript store's Append publishes
// committed events to after each Postgres commit (NFR2); pass nil to
// disable publishing entirely (e.g. no RABBITMQ_URL configured) -- Append
// still works, it simply skips the publish step. opts are optional
// dependencies (see WithS3).
func New(pool *pgxpool.Pool, pub events.PublisherInterface, opts ...Option) *Store {
	s := &Store{pool: pool, pub: pub}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Sessions returns the SessionStore implementation (sessions.go, LB2/NFR3).
func (s *Store) Sessions() SessionStore { return sessionStore{pool: s.pool} }

// Transcript returns the TranscriptStore implementation (transcript.go,
// LB1). Tier-transparent hydration (FR8) is only active when the Store was
// built WithS3 -- see transcriptStore's doc comment.
func (s *Store) Transcript() TranscriptStore {
	return transcriptStore{pool: s.pool, pub: s.pub, s3: s.s3}
}

// AgentDefinitions returns the AgentDefinitionStore implementation
// (agentdef.go, LB5/NFR6).
func (s *Store) AgentDefinitions() AgentDefinitionStore { return agentDefinitionStore{pool: s.pool} }

// ModelDefinitions returns the ModelDefinitionStore implementation
// (modeldef.go): the `model_definition` table an AgentDefinition may
// reference via ModelDefinitionID instead of naming a model directly.
func (s *Store) ModelDefinitions() ModelDefinitionStore { return modelDefinitionStore{pool: s.pool} }

// Usage returns the UsageStore implementation (usage.go, LB6).
func (s *Store) Usage() UsageStore { return usageStore{pool: s.pool} }

// Idempotency returns the IdempotencyLedger implementation
// (idempotency.go, LB4).
func (s *Store) Idempotency() IdempotencyLedger { return idempotencyLedgerStore{pool: s.pool} }

// Archive returns the ArchiveStore implementation (archive.go, issue
// #2240, FR8/LB1): the `transcript_archive` cold-tier index FR7's archiver
// writes and Transcript()'s tier-transparent Read consults.
func (s *Store) Archive() ArchiveStore { return archiveStore{pool: s.pool} }
