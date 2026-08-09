package postgres

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

// Postgres SQLSTATE codes for the integrity-constraint violation classes
// this package translates. See
// https://www.postgresql.org/docs/current/errcodes-appendix.html.
const (
	sqlStateUniqueViolation     = "23505"
	sqlStateCheckViolation      = "23514"
	sqlStateForeignKeyViolation = "23503"
)

// translatePgError is the single place a Postgres integrity-constraint
// violation becomes a repository domain error. Every write path in this
// package must route constraint-violation errors through it (rather than
// returning the raw pgx error) so:
//
//   - handlers map to the correct gRPC code via errors.Is on the sentinel,
//     never by string-matching pgx's error text or SQLSTATE — see
//     handlers/errors.go's mapRepoErr.
//   - AR-5's AllocateVersion can distinguish "someone else took this
//     version, retry" (ErrAlreadyExists -> codes.AlreadyExists, a caller
//     should retry) from a genuine server bug (codes.Internal, retrying is
//     pointless).
//   - clients never see an index name or a SQLSTATE; msg is the only text
//     that reaches them, and it must describe the conflict in domain terms.
//
// It uses errors.As against *pgconn.PgError, not string matching, so it
// keeps working regardless of how pgx wraps the underlying error.
//
// ok is false when err is not one of the three violation classes this
// package maps; callers should fall back to their normal generic wrap in
// that case (translatePgError deliberately does not swallow errors it
// doesn't recognize).
func translatePgError(err error, msg string) (domainErr error, ok bool) {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return nil, false
	}
	switch pgErr.Code {
	case sqlStateUniqueViolation:
		return fmt.Errorf("%w: %s", repository.ErrAlreadyExists, msg), true
	case sqlStateCheckViolation:
		return fmt.Errorf("%w: %s", repository.ErrInvalidArgument, msg), true
	case sqlStateForeignKeyViolation:
		return fmt.Errorf("%w: %s", repository.ErrFailedPrecondition, msg), true
	default:
		return nil, false
	}
}
