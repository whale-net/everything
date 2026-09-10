package session

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
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

// SessionFilter is ListSessions' (FR3/C15) filter set, mirroring
// ListSessionsRequest's optional fields one-for-one. A nil field means "no
// filter on this column"; multiple set fields combine with AND. There is
// no free-text or multi-column-sort field here -- deliberately out of
// scope for M2 (issue #2241).
type SessionFilter struct {
	AgentID *string
	State   *Status
	// StartedByKind filters on the session's own subject_kind column
	// (LB2/NFR3) -- there is no separate identity/display-name table to
	// join against, in this task or anywhere in M2.
	StartedByKind *SubjectKind
	// StartedAfter is an inclusive lower bound on CreatedAt.
	StartedAfter *time.Time
	// StartedBefore is an exclusive upper bound on CreatedAt.
	StartedBefore *time.Time
}

// SessionPage is List's paging input: PageSize (0 means "server default",
// mirroring defaultTranscriptLimit/maxTranscriptLimit's shape in
// api/handlers/session.go) and PageToken, an opaque cursor previously
// returned as PageInfo.NextPageToken or PageInfo.PrevPageToken. An empty
// PageToken starts from the first page.
type SessionPage struct {
	PageSize  int
	PageToken string
}

// PageInfo is List's paging output: opaque cursors for the next/previous
// page, each encoding a position in the (created_at DESC, session_id DESC)
// keyset List orders by. Empty means there is no such page.
type PageInfo struct {
	NextPageToken string
	PrevPageToken string
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
	// List returns sessions matching filter, in (created_at DESC,
	// session_id DESC) order, keyset-paginated per page (FR3/C15) --
	// sessions from any subject, not just the caller's own (that
	// visibility rule is enforced by the caller, api/handlers/session.go,
	// not here).
	List(ctx context.Context, filter SessionFilter, page SessionPage) ([]*Session, PageInfo, error)
	// ListArchiveEligible is FR7's archiver selection query (issue #2244):
	// up to limit session IDs, ordered by updated_at ascending (oldest
	// first), that are either (a) terminal (Status.IsTerminal()) with
	// updated_at -- the compare-and-swap terminal write UpdateStatus
	// performs, used here as the terminal timestamp -- older than
	// olderThan and no `transcript_archive` row yet, or (b) already have a
	// `transcript_archive` row whose hot_trimmed_at is still NULL, i.e. a
	// prior archiver attempt uploaded and committed the index row but
	// crashed before trimming. (b) is included regardless of olderThan so
	// a crashed-mid-archive session is picked back up on the very next
	// scan rather than waiting out the TTL again; RunArchiveBatch
	// (whagent_net/worker/archive.go) tells the two cases apart via
	// ArchiveStore.Get and does not re-upload in case (b).
	ListArchiveEligible(ctx context.Context, olderThan time.Time, limit int) ([]uuid.UUID, error)
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

// ListArchiveEligible implements SessionStore.ListArchiveEligible (see
// that doc comment for the two cases this query unions): a LEFT JOIN
// against transcript_archive distinguishes "never archived" (ta.session_id
// IS NULL, gated on updated_at < olderThan) from "archived but not yet
// trimmed" (ta.session_id IS NOT NULL AND ta.hot_trimmed_at IS NULL,
// ungated on olderThan -- a crash-recovery resume, not a fresh selection).
func (s sessionStore) ListArchiveEligible(ctx context.Context, olderThan time.Time, limit int) ([]uuid.UUID, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT s.session_id
		FROM sessions s
		LEFT JOIN transcript_archive ta ON ta.session_id = s.session_id
		WHERE s.status IN (`+terminalStatusList+`)
		  AND (
		        (ta.session_id IS NULL AND s.updated_at < $1)
		     OR (ta.session_id IS NOT NULL AND ta.hot_trimmed_at IS NULL)
		      )
		ORDER BY s.updated_at ASC
		LIMIT $2
	`, olderThan, limit)
	if err != nil {
		return nil, fmt.Errorf("list archive-eligible sessions: %w", err)
	}
	defer rows.Close()

	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("list archive-eligible sessions: scan: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list archive-eligible sessions: %w", err)
	}
	return ids, nil
}

// defaultSessionListPageSize and maxSessionListPageSize bound List's page
// size (FR3/C15's pagination), the same shape as api/handlers/session.go's
// defaultTranscriptLimit/maxTranscriptLimit: 0 in SessionPage.PageSize
// means "server default", and no caller can force an unbounded single-page
// read.
const (
	defaultSessionListPageSize = 50
	maxSessionListPageSize     = 200
)

// ErrInvalidPageToken is returned by List when SessionPage.PageToken is
// malformed or tampered with -- callers (api/handlers/session.go) map this
// to codes.InvalidArgument, never a panic or a silent full-list fallback.
var ErrInvalidPageToken = errors.New("invalid page_token")

// sessionCursorDirection distinguishes List's two paging directions, both
// encoded into the same opaque page_token/prev_page_token string (see
// encodeSessionCursor) so SessionPage's single PageToken field can drive
// either direction.
type sessionCursorDirection byte

const (
	// sessionCursorNext resumes strictly AFTER (created_at, session_id) in
	// the canonical (created_at DESC, session_id DESC) order -- i.e. it
	// walks toward older sessions.
	sessionCursorNext sessionCursorDirection = 'n'
	// sessionCursorPrev resumes strictly BEFORE (created_at, session_id) in
	// the canonical order -- i.e. it walks back toward newer sessions.
	sessionCursorPrev sessionCursorDirection = 'p'
)

// encodeSessionCursor is List's opaque page_token/prev_page_token encoding:
// direction plus the (created_at, session_id) keyset position of the row
// the next call should resume from or before. Mirrors
// tools/app_registry/server/repository/postgres/keyset_cursor.go's
// base64(ts|id) shape, with a leading direction byte since List's cursor
// must carry both directions through a single PageToken field.
func encodeSessionCursor(dir sessionCursorDirection, createdAt time.Time, id uuid.UUID) string {
	raw := fmt.Sprintf("%c|%d|%s", dir, createdAt.UnixNano(), id.String())
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodeSessionCursor is encodeSessionCursor's inverse. Any malformed or
// tampered token -- bad base64, wrong field count, unparseable timestamp/
// UUID, or an unrecognized direction byte -- is reported as
// ErrInvalidPageToken, never a panic.
func decodeSessionCursor(token string) (dir sessionCursorDirection, createdAt time.Time, id uuid.UUID, err error) {
	raw, decErr := base64.RawURLEncoding.DecodeString(token)
	if decErr != nil {
		return 0, time.Time{}, uuid.UUID{}, fmt.Errorf("decode session cursor: %w", ErrInvalidPageToken)
	}
	parts := strings.SplitN(string(raw), "|", 3)
	if len(parts) != 3 || len(parts[0]) != 1 {
		return 0, time.Time{}, uuid.UUID{}, fmt.Errorf("decode session cursor: malformed cursor: %w", ErrInvalidPageToken)
	}
	switch sessionCursorDirection(parts[0][0]) {
	case sessionCursorNext, sessionCursorPrev:
		dir = sessionCursorDirection(parts[0][0])
	default:
		return 0, time.Time{}, uuid.UUID{}, fmt.Errorf("decode session cursor: unrecognized direction: %w", ErrInvalidPageToken)
	}
	nanos, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, time.Time{}, uuid.UUID{}, fmt.Errorf("decode session cursor: %w", ErrInvalidPageToken)
	}
	id, err = uuid.Parse(parts[2])
	if err != nil {
		return 0, time.Time{}, uuid.UUID{}, fmt.Errorf("decode session cursor: %w", ErrInvalidPageToken)
	}
	return dir, time.Unix(0, nanos), id, nil
}

// List applies filter in SQL -- never an unbounded fetch filtered in Go --
// keyset-paginated on (created_at DESC, session_id DESC) per SessionPage/
// PageInfo's doc comments (FR3/C15, issue #2241). It fetches one row past
// page's size to decide whether a further page exists in the query's own
// direction without a separate COUNT(*) query.
//
// A backward (PrevPageToken-driven) page is queried in ascending order so
// the LIMIT finds the rows immediately preceding the cursor, then reversed
// in Go back into the canonical descending order before being returned --
// callers never see the query's internal direction.
func (s sessionStore) List(ctx context.Context, filter SessionFilter, page SessionPage) ([]*Session, PageInfo, error) {
	pageSize := page.PageSize
	switch {
	case pageSize <= 0:
		pageSize = defaultSessionListPageSize
	case pageSize > maxSessionListPageSize:
		pageSize = maxSessionListPageSize
	}

	var (
		hasCursor bool
		backward  bool
		cursorTS  time.Time
		cursorID  uuid.UUID
	)
	if page.PageToken != "" {
		dir, ts, id, err := decodeSessionCursor(page.PageToken)
		if err != nil {
			return nil, PageInfo{}, err
		}
		hasCursor = true
		backward = dir == sessionCursorPrev
		cursorTS, cursorID = ts, id
	}

	query := `SELECT ` + sessionColumns + ` FROM sessions WHERE 1=1`
	var args []any
	if filter.AgentID != nil {
		args = append(args, *filter.AgentID)
		query += fmt.Sprintf(" AND agent_id = $%d", len(args))
	}
	if filter.State != nil {
		args = append(args, string(*filter.State))
		query += fmt.Sprintf(" AND status = $%d", len(args))
	}
	if filter.StartedByKind != nil {
		args = append(args, string(*filter.StartedByKind))
		query += fmt.Sprintf(" AND subject_kind = $%d", len(args))
	}
	if filter.StartedAfter != nil {
		args = append(args, *filter.StartedAfter)
		query += fmt.Sprintf(" AND created_at >= $%d", len(args))
	}
	if filter.StartedBefore != nil {
		args = append(args, *filter.StartedBefore)
		query += fmt.Sprintf(" AND created_at < $%d", len(args))
	}
	if hasCursor {
		args = append(args, cursorTS, cursorID)
		if backward {
			query += fmt.Sprintf(" AND (created_at, session_id) > ($%d::timestamptz, $%d::uuid)", len(args)-1, len(args))
		} else {
			query += fmt.Sprintf(" AND (created_at, session_id) < ($%d::timestamptz, $%d::uuid)", len(args)-1, len(args))
		}
	}
	if backward {
		query += " ORDER BY created_at ASC, session_id ASC"
	} else {
		query += " ORDER BY created_at DESC, session_id DESC"
	}
	args = append(args, pageSize+1)
	query += fmt.Sprintf(" LIMIT $%d", len(args))

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, PageInfo{}, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	var out []*Session
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, PageInfo{}, fmt.Errorf("list sessions: scan: %w", err)
		}
		out = append(out, sess)
	}
	if err := rows.Err(); err != nil {
		return nil, PageInfo{}, fmt.Errorf("list sessions: %w", err)
	}

	hasMore := len(out) > pageSize
	if hasMore {
		out = out[:pageSize]
	}
	if backward {
		// The query ran ascending to find the rows immediately preceding
		// the cursor; reverse back into the canonical descending order.
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}

	var info PageInfo
	if len(out) > 0 {
		first, last := out[0], out[len(out)-1]

		// NextPageToken: a row exists after `last` in canonical order. A
		// forward query's own extra-row fetch (hasMore) says so directly;
		// a backward query always has a next page -- the one its cursor
		// navigated back from.
		if (!backward && hasMore) || (backward && hasCursor) {
			info.NextPageToken = encodeSessionCursor(sessionCursorNext, last.CreatedAt, last.SessionID)
		}
		// PrevPageToken: a row exists before `first` in canonical order. A
		// backward query's own extra-row fetch (hasMore) says so directly;
		// a forward query has a previous page whenever a cursor drove it
		// at all (an empty token is only ever the very first page).
		if (backward && hasMore) || (!backward && hasCursor) {
			info.PrevPageToken = encodeSessionCursor(sessionCursorPrev, first.CreatedAt, first.SessionID)
		}
	}

	return out, info, nil
}
