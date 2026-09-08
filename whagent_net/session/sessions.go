package session

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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

const sessionColumns = `
	session_id, subject_iss, subject_sub, subject_kind,
	on_behalf_of_iss, on_behalf_of_sub, on_behalf_of_kind,
	parent_session_id, agent_id, model, model_override,
	status, cap_kind, error_category, error_detail,
	created_at, updated_at
`

// scanSession scans one sessionColumns row. The enum-shaped columns
// (subject_kind/on_behalf_of_kind/status/cap_kind/error_category) are
// scanned into plain strings first, then converted to their named types --
// matching tools/app_registry's scanApp convention rather than relying on
// pgx to scan TEXT directly into a named string type. cap_kind/
// error_category are only ever non-NULL for a terminal session (FR3), but
// are scanned as plain nullable fields regardless of status.
func scanSession(row pgx.Row) (*Session, error) {
	var sess Session
	var subjectKind, onBehalfOfKind, status string
	var capKind, errorCategory *string
	if err := row.Scan(
		&sess.SessionID, &sess.Subject.Iss, &sess.Subject.Sub, &subjectKind,
		&sess.OnBehalfOf.Iss, &sess.OnBehalfOf.Sub, &onBehalfOfKind,
		&sess.ParentSessionID, &sess.AgentID, &sess.Model, &sess.ModelOverride,
		&status, &capKind, &errorCategory, &sess.ErrorDetail,
		&sess.CreatedAt, &sess.UpdatedAt,
	); err != nil {
		return nil, err
	}
	sess.Subject.Kind = SubjectKind(subjectKind)
	sess.OnBehalfOf.Kind = SubjectKind(onBehalfOfKind)
	sess.Status = Status(status)
	if capKind != nil {
		ck := CapKind(*capKind)
		sess.CapKind = &ck
	}
	if errorCategory != nil {
		ec := ErrorCategory(*errorCategory)
		sess.ErrorCategory = &ec
	}
	return &sess, nil
}

// Create inserts sess and fills in the DB-assigned CreatedAt/UpdatedAt
// (both DEFAULT NOW()) on the passed-in pointer.
func (s sessionStore) Create(ctx context.Context, sess *Session) error {
	err := s.pool.QueryRow(ctx, `
		INSERT INTO sessions (
			session_id, subject_iss, subject_sub, subject_kind,
			on_behalf_of_iss, on_behalf_of_sub, on_behalf_of_kind,
			parent_session_id, agent_id, model, model_override, status
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		RETURNING created_at, updated_at
	`,
		sess.SessionID, sess.Subject.Iss, sess.Subject.Sub, string(sess.Subject.Kind),
		sess.OnBehalfOf.Iss, sess.OnBehalfOf.Sub, string(sess.OnBehalfOf.Kind),
		sess.ParentSessionID, sess.AgentID, sess.Model, sess.ModelOverride, string(sess.Status),
	).Scan(&sess.CreatedAt, &sess.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert session: %w", err)
	}
	return nil
}

// GetByID returns nil (not an error) when no session with id exists.
func (s sessionStore) GetByID(ctx context.Context, id uuid.UUID) (*Session, error) {
	sess, err := scanSession(s.pool.QueryRow(ctx, `
		SELECT `+sessionColumns+`
		FROM sessions
		WHERE session_id = $1
	`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get session by id: %w", err)
	}
	return sess, nil
}

// ErrTerminalStatus is returned by UpdateStatus when id's session has
// already reached a terminal status -- the compare-and-swap this method
// implements never lets any later write (terminal or not) overwrite a
// terminal status once committed (same guarantee as manmanv2 control-api/
// event-processor's UpdateSessionEndIfStatus, see #2063).
var ErrTerminalStatus = errors.New("session status is already terminal")

// UpdateStatus is a single UPDATE ... WHERE guarded on the row's current
// status never already being terminal -- the compare-and-swap. A
// zero-row UPDATE (session already terminal, or id does not exist) is
// reported as ErrTerminalStatus/pgx.ErrNoRows respectively rather than
// silently succeeding, so a caller racing a terminal write finds out its
// write was dropped.
func (s sessionStore) UpdateStatus(ctx context.Context, id uuid.UUID, status Status, terminal *TerminalReason) error {
	var capKind, errorCategory, errorDetail *string
	if terminal != nil {
		if terminal.CapKind != nil {
			ck := string(*terminal.CapKind)
			capKind = &ck
		}
		if terminal.ErrorCategory != nil {
			ec := string(*terminal.ErrorCategory)
			errorCategory = &ec
		}
		errorDetail = terminal.ErrorDetail
	}

	tag, err := s.pool.Exec(ctx, `
		UPDATE sessions
		SET status = $2, cap_kind = $3, error_category = $4, error_detail = $5, updated_at = NOW()
		WHERE session_id = $1
		  AND status NOT IN (`+terminalStatusList+`)
	`, id, string(status), capKind, errorCategory, errorDetail)
	if err != nil {
		return fmt.Errorf("update session status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Distinguish "session doesn't exist" from "session is terminal"
		// so callers get an accurate error.
		existing, getErr := s.GetByID(ctx, id)
		if getErr != nil {
			return getErr
		}
		if existing == nil {
			return pgx.ErrNoRows
		}
		return ErrTerminalStatus
	}
	return nil
}

// terminalStatusList is the SQL-literal IN-list of Status.IsTerminal's
// terminal values, kept in one place so UpdateStatus's CAS predicate
// cannot drift from IsTerminal's Go-side definition.
const terminalStatusList = `'done', 'stopped', 'failed', 'capped'`
