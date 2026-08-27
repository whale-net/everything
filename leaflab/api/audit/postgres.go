package audit

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
)

// Execer is the minimal subset of *pgxpool.Pool and pgx.Tx that
// PostgresAuditor needs. Both satisfy it with no adapter -- Exec has the
// same signature on the pool and on a transaction.
//
// Accepting this instead of *pgxpool.Pool directly is what makes
// transactional participation possible: a caller constructs a
// PostgresAuditor over a pgx.Tx it is already holding open for the write
// being audited (see leaflab/api/repository.go's auditedWrite), so the
// audit INSERT and the write it records commit or roll back together
// (NFR6.2's "a rolled-back write leaves no audit row").
type Execer interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// PostgresAuditor is the production Auditor: a single INSERT into
// audit_log, run over whatever Execer the caller supplies. It never
// UPDATEs or DELETEs -- append-only enforcement beyond that is the
// database's job (016_audit_log.up.sql's trigger and REVOKE), not this
// type's.
type PostgresAuditor struct {
	exec Execer
}

// NewPostgresAuditor returns a PostgresAuditor that writes over exec.
// Pass a pgx.Tx to make the audit write participate in the same
// transaction as the write it records; passing a *pgxpool.Pool directly
// writes outside any caller transaction and should only be done for an
// audited action that genuinely has no accompanying DB write of its own
// (e.g. an audited read under an elevated/granted identity, per FR8.1).
func NewPostgresAuditor(exec Execer) *PostgresAuditor {
	return &PostgresAuditor{exec: exec}
}

// Record inserts entry as a new audit_log row. occurred_at is stamped by
// the column default (NOW()), not by Entry -- see Entry's doc comment.
func (a *PostgresAuditor) Record(ctx context.Context, entry Entry) error {
	_, err := a.exec.Exec(ctx, `
		INSERT INTO audit_log
			(actor_subject, actor_kind, target_household_id, action, entity_kind, entity_id, reason, correlation_id)
		VALUES
			($1, $2, $3, $4, $5, $6, $7, $8)
	`,
		entry.ActorSubject,
		string(entry.ActorKind),
		entry.TargetHouseholdID,
		entry.Action,
		entry.EntityKind,
		entry.EntityID,
		entry.Reason,
		entry.CorrelationID,
	)
	if err != nil {
		return fmt.Errorf("record audit entry (action=%s entity_kind=%s): %w", entry.Action, entry.EntityKind, err)
	}
	return nil
}
