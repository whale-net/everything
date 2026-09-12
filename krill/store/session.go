// Package store is krill's Postgres-backed data layer, built over
// //libs/go/db's *pgxpool.Pool (same shape as whagent_net/session.Store
// and audience_score_system/store.Store).
//
// This file (issue #2489, FR3) is the `init` session primitive: it mints a
// krill-native session id and records the two subjects LB4 requires on
// every mutating call. It is the one work-axis-adjacent primitive this
// milestone ships -- claim/heartbeat/complete/abandon/note are M4 and do
// not belong here.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SubjectKind distinguishes a human caller from a service account (LB4,
// mirrors whagent_net's LB2 verbatim) -- both are treated uniformly
// wherever a Subject appears.
type SubjectKind string

const (
	SubjectKindHuman   SubjectKind = "human"
	SubjectKindService SubjectKind = "service"
)

// Subject is an (issuer, subject, kind) identity triple -- kept as three
// real columns rather than one opaque string so a non-Keycloak issuer is a
// row, not a migration (mirrors whagent_net/session.Subject and
// libs/go/whagent's Claim sub/sub_iss/act shape).
type Subject struct {
	Iss  string
	Sub  string
	Kind SubjectKind
}

// SessionID is krill's own session identifier (migrations/
// 003_session.up.sql: krill_session.id, a surrogate PK minted by
// Postgres). It is never equal to, and never derived from, a
// libs/go/whagent Claim.WhagentSessionID -- see InitSession's doc comment.
type SessionID uuid.UUID

// String renders id in its canonical UUID form.
func (id SessionID) String() string { return uuid.UUID(id).String() }

// Session is a krill_session row as read back by the write gate
// (api/handlers/gate.go): the two subjects and scope InitSession recorded,
// resolved from a krill-native session id so a write handler never has to
// re-derive attribution itself.
type Session struct {
	ID               SessionID
	ScopeID          uuid.UUID
	Acting           Subject
	OnBehalfOf       Subject
	WhagentSessionID *string
	CreatedAt        time.Time
}

// ErrSessionNotFound is returned by SessionStore.GetSession when id does not
// name a krill_session row -- the write gate (api/handlers/gate.go) turns
// this into a rejection rather than a 500, since an unknown session id is a
// caller error (an expired or fabricated id), not a store failure.
var ErrSessionNotFound = errors.New("krill session not found")

// SessionStore is the store surface FR3's `init` primitive and the write
// gate (api/handlers/gate.go) depend on.
type SessionStore interface {
	// InitSession mints a new krill-native session id and records it
	// against scopeID (LB1) with both required subjects (LB4): acting is
	// who is making this call, onBehalfOf is who the call is attributed
	// to. Callers must pass onBehalfOf == acting when a caller is acting
	// for itself -- InitSession does not infer or default this.
	//
	// whagentSessionID is the inbound libs/go/whagent Claim.WhagentSessionID
	// when the call arrived through the whagent-net verifier, recorded on
	// the row purely as a correlation field (see krill_session.
	// whagent_session_id's migration comment). Pass nil for a caller with
	// no whagent claim (e.g. a human/OAuth2 caller) -- InitSession still
	// mints a krill session in that case.
	InitSession(ctx context.Context, scopeID uuid.UUID, acting, onBehalfOf Subject, whagentSessionID *string) (SessionID, error)

	// GetSession returns the krill_session row for id -- the write gate's
	// (api/handlers/gate.go) sole read path, resolving a caller-presented
	// session id into the two subjects and scope InitSession wrote so a
	// write handler can attribute its mutation without re-authenticating.
	// Returns ErrSessionNotFound if id names no row -- never a nil, nil
	// success -- so the gate can tell "invalid session id" apart from a
	// genuine store error.
	GetSession(ctx context.Context, id SessionID) (Session, error)
}

// sessionStore is the pgx-backed SessionStore implementation.
type sessionStore struct {
	pool *pgxpool.Pool
}

// NewSessionStore returns a SessionStore backed by pool (see
// //libs/go/db.NewPool).
func NewSessionStore(pool *pgxpool.Pool) SessionStore {
	return sessionStore{pool: pool}
}

// InitSession implements SessionStore.
func (s sessionStore) InitSession(ctx context.Context, scopeID uuid.UUID, acting, onBehalfOf Subject, whagentSessionID *string) (SessionID, error) {
	var id uuid.UUID
	err := s.pool.QueryRow(ctx, `
		INSERT INTO krill_session (
			scope_id,
			acting_iss, acting_sub, acting_kind,
			on_behalf_of_iss, on_behalf_of_sub, on_behalf_of_kind,
			whagent_session_id
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id
	`,
		scopeID,
		acting.Iss, acting.Sub, string(acting.Kind),
		onBehalfOf.Iss, onBehalfOf.Sub, string(onBehalfOf.Kind),
		whagentSessionID,
	).Scan(&id)
	if err != nil {
		return SessionID{}, fmt.Errorf("insert krill_session: %w", err)
	}
	return SessionID(id), nil
}

// GetSession implements SessionStore.
func (s sessionStore) GetSession(ctx context.Context, id SessionID) (Session, error) {
	var sess Session
	var actingKind, onBehalfOfKind string
	err := s.pool.QueryRow(ctx, `
		SELECT
			id,
			scope_id,
			acting_iss, acting_sub, acting_kind,
			on_behalf_of_iss, on_behalf_of_sub, on_behalf_of_kind,
			whagent_session_id,
			created_at
		FROM krill_session
		WHERE id = $1
	`, uuid.UUID(id)).Scan(
		(*uuid.UUID)(&sess.ID),
		&sess.ScopeID,
		&sess.Acting.Iss, &sess.Acting.Sub, &actingKind,
		&sess.OnBehalfOf.Iss, &sess.OnBehalfOf.Sub, &onBehalfOfKind,
		&sess.WhagentSessionID,
		&sess.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrSessionNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("select krill_session: %w", err)
	}
	sess.Acting.Kind = SubjectKind(actingKind)
	sess.OnBehalfOf.Kind = SubjectKind(onBehalfOfKind)
	return sess, nil
}
