package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PointerArtifactStore covers `pointer_artifact` (migration 005, issue
// #2496, FR20, C9) -- the audit trail of the one thin GitHub issue krill
// mints per Product. Single parent: Product.ID. Not SCD2 (LB3) -- see
// migration 005_pointer_artifact.up.sql's boundary comment; there is no
// "current row" distinction here, same as MilestoneStore.
//
// This store does not talk to GitHub itself -- issueNumber/issueURL are
// supplied by the caller (api/handlers/pointer.go), already the result of
// a krill/forge.Client.CreateIssue call, so Create can be exercised by a
// real-Postgres integration test with no live GitHub call and no forge
// package dependency in this file.
type PointerArtifactStore interface {
	// Create records the pointer_artifact row for a just-created GitHub
	// issue (issueNumber/issueURL) against productID, attributing the
	// write to both acting and onBehalfOf (LB4 -- both subjects always
	// recorded, unlike every other M1 create endpoint). In the same
	// transaction, it mirrors issueNumber onto `scope.pointer_issue_number`
	// (LB1: scope is the forge-coordinate system of record) -- see this
	// file's doc comment and migration 005's LB1 note for why.
	//
	// Returns ErrNotFound if productID has no current `product` row in
	// scopeID (LB2 parentage, same enforcement as every other Create in
	// this package). Fails (a Postgres unique-constraint violation,
	// api/handlers/types.go's writeStoreError maps this to 409) if
	// productID already has a pointer_artifact row --
	// pointer_artifact_product_idx, migration 005: at most one pointer
	// issue is ever minted per Product.
	Create(ctx context.Context, scopeID, productID uuid.UUID, issueNumber int, issueURL string, acting, onBehalfOf Subject) (PointerArtifact, error)

	// ListByProduct returns every pointer_artifact row for productID
	// (today, at most one -- see Create's doc comment), ordered by
	// CreatedAt. This is what FR8's GetProductSlice (krill/slice/query.go)
	// calls to make a created pointer artifact retrievable through the
	// whole-product slice.
	ListByProduct(ctx context.Context, productID uuid.UUID) ([]PointerArtifact, error)
}

type pointerArtifactStore struct{ pool *pgxpool.Pool }

var _ PointerArtifactStore = pointerArtifactStore{}

const pointerArtifactColumns = `id, scope_id, product_id, kind, issue_number, issue_url, ` +
	`created_by_acting_iss, created_by_acting_sub, created_by_acting_kind, ` +
	`created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind, ` +
	`created_at`

func scanPointerArtifact(row pgx.Row) (PointerArtifact, error) {
	var p PointerArtifact
	var actingKind, onBehalfOfKind string
	err := row.Scan(
		&p.ID, &p.ScopeID, &p.ProductID, &p.Kind, &p.IssueNumber, &p.IssueURL,
		&p.CreatedByActing.Iss, &p.CreatedByActing.Sub, &actingKind,
		&p.CreatedByOnBehalfOf.Iss, &p.CreatedByOnBehalfOf.Sub, &onBehalfOfKind,
		&p.CreatedAt,
	)
	if err != nil {
		return PointerArtifact{}, err
	}
	p.CreatedByActing.Kind = SubjectKind(actingKind)
	p.CreatedByOnBehalfOf.Kind = SubjectKind(onBehalfOfKind)
	return p, nil
}

func (s pointerArtifactStore) Create(ctx context.Context, scopeID, productID uuid.UUID, issueNumber int, issueURL string, acting, onBehalfOf Subject) (PointerArtifact, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return PointerArtifact{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	exists, err := currentRowExists(ctx, tx, "product", productID, scopeID)
	if err != nil {
		return PointerArtifact{}, err
	}
	if !exists {
		return PointerArtifact{}, errParentNotFound("product", productID)
	}

	artifact, err := scanPointerArtifact(tx.QueryRow(ctx, `
		INSERT INTO pointer_artifact (
			scope_id, product_id, issue_number, issue_url,
			created_by_acting_iss, created_by_acting_sub, created_by_acting_kind,
			created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING `+pointerArtifactColumns,
		scopeID, productID, issueNumber, issueURL,
		acting.Iss, acting.Sub, string(acting.Kind),
		onBehalfOf.Iss, onBehalfOf.Sub, string(onBehalfOf.Kind)))
	if err != nil {
		return PointerArtifact{}, fmt.Errorf("insert pointer_artifact: %w", err)
	}

	// scope.pointer_issue_number is the forge-coordinate system of record
	// (LB1) -- mirror the just-minted issue number onto it in the same
	// transaction as the audit row above, so the two can never observably
	// diverge. pointer_artifact_product_idx's UNIQUE constraint (migration
	// 005) already guarantees this UPDATE runs at most once per scope in
	// M1's one-Product-per-scope shape.
	if _, err := tx.Exec(ctx, `
		UPDATE scope SET pointer_issue_number = $1, updated_at = NOW() WHERE id = $2
	`, issueNumber, scopeID); err != nil {
		return PointerArtifact{}, fmt.Errorf("update scope.pointer_issue_number: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return PointerArtifact{}, fmt.Errorf("commit: %w", err)
	}
	return artifact, nil
}

func (s pointerArtifactStore) ListByProduct(ctx context.Context, productID uuid.UUID) ([]PointerArtifact, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+pointerArtifactColumns+`
		FROM pointer_artifact
		WHERE product_id = $1
		ORDER BY created_at
	`, productID)
	if err != nil {
		return nil, fmt.Errorf("list pointer_artifact by product: %w", err)
	}
	defer rows.Close()

	var artifacts []PointerArtifact
	for rows.Next() {
		p, err := scanPointerArtifact(rows)
		if err != nil {
			return nil, fmt.Errorf("scan pointer_artifact: %w", err)
		}
		artifacts = append(artifacts, p)
	}
	return artifacts, rows.Err()
}
