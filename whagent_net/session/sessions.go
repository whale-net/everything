package session

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SubjectKind distinguishes a human caller from a service account (LB2/
// NFR3, ARCHITECTURE.md "Identity and auth chaining") -- both are treated
// uniformly wherever a Subject appears.
type SubjectKind string

const (
	SubjectKindHuman   SubjectKind = "human"
	SubjectKindService SubjectKind = "service"
)

// Status is a session's control-plane lifecycle state
// (ARCHITECTURE.md "Three nouns"). running/awaiting_input are
// non-terminal; done/stopped/failed/capped are terminal and, once
// written, are never overwritten by a later non-terminal write (see
// SessionStore.UpdateStatus).
type Status string

const (
	StatusRunning       Status = "running"
	StatusAwaitingInput Status = "awaiting_input"
	StatusDone          Status = "done"
	StatusStopped       Status = "stopped"
	StatusFailed        Status = "failed"
	StatusCapped        Status = "capped"
)

// IsTerminal reports whether s is one of the terminal statuses that
// UpdateStatus's compare-and-swap protects from being overwritten by a
// later non-terminal write.
func (s Status) IsTerminal() bool {
	switch s {
	case StatusDone, StatusStopped, StatusFailed, StatusCapped:
		return true
	default:
		return false
	}
}

// CapKind names which guardrail ended a session in the capped status
// (FR6/FR7 default caps: 100 turns / $1, ARCHITECTURE.md "Guardrails").
type CapKind string

const (
	CapKindTurns CapKind = "turns"
	CapKindCost  CapKind = "cost"
)

// ErrorCategory classifies a failed session's terminal error as safe to
// retry or not (FR3).
type ErrorCategory string

const (
	ErrorCategoryRetryable    ErrorCategory = "retryable"
	ErrorCategoryNonRetryable ErrorCategory = "non_retryable"
)

// Subject is a (issuer, subject, kind) identity triple -- kept as three
// real columns rather than a single opaque string so a non-Keycloak issuer
// (e.g. an Audience Score System Person keyed on a Google sub) can be a
// subject or on-behalf-of subject later without a schema change (NFR3,
// ARCHITECTURE.md "Identity and auth chaining").
type Subject struct {
	Iss  string
	Sub  string
	Kind SubjectKind
}

// TerminalReason carries the extra columns a terminal UpdateStatus write
// populates: CapKind for a capped session, ErrorCategory/ErrorDetail for a
// capped or failed one. Nil for a non-terminal status transition.
type TerminalReason struct {
	CapKind       *CapKind
	ErrorCategory *ErrorCategory
	ErrorDetail   *string
}

// Session is the `sessions` control-plane row (LB2/NFR3). Session ID
// equals the Temporal workflow ID (LB2).
type Session struct {
	SessionID       uuid.UUID
	Subject         Subject
	OnBehalfOf      Subject
	ParentSessionID *uuid.UUID
	AgentID         string
	Model           string
	ModelOverride   *string
	Status          Status
	CapKind         *CapKind
	ErrorCategory   *ErrorCategory
	ErrorDetail     *string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// SessionStore is the `sessions` table's repository interface.
// UpdateStatus is a compare-and-swap on terminal writes -- the same
// pattern as manmanv2 control-api/event-processor's
// UpdateSessionEndIfStatus (see #2063): once a session's status is
// terminal, a later non-terminal write must never overwrite it.
type SessionStore interface {
	// Create inserts a new session row.
	Create(ctx context.Context, s *Session) error
	// GetByID reads a session by its ID (== Temporal workflow ID).
	GetByID(ctx context.Context, id uuid.UUID) (*Session, error)
	// UpdateStatus transitions a session to status, applying terminal's
	// cap_kind/error_category/error_detail when status is terminal
	// (terminal must be nil for a non-terminal status). Implemented as a
	// compare-and-swap so a stale non-terminal write can never clobber a
	// terminal status that already committed.
	UpdateStatus(ctx context.Context, id uuid.UUID, status Status, terminal *TerminalReason) error
}

// sessionStore is the Postgres-backed SessionStore implementation.
type sessionStore struct{ pool *pgxpool.Pool }

var _ SessionStore = sessionStore{}

// errNotImplemented is returned by every scaffold-phase stub method in
// this package (issue #2109's Scaffold phase). Real SQL lands in the
// Implementation phase.
var errNotImplemented = errors.New("not implemented: whagent_net/session scaffold phase (#2109)")

func (s sessionStore) Create(ctx context.Context, sess *Session) error {
	return errNotImplemented
}

func (s sessionStore) GetByID(ctx context.Context, id uuid.UUID) (*Session, error) {
	return nil, errNotImplemented
}

func (s sessionStore) UpdateStatus(ctx context.Context, id uuid.UUID, status Status, terminal *TerminalReason) error {
	return errNotImplemented
}
