// This file (FR a6cd917f) is the shared narrowing every M5 console queue
// read takes: ConsoleFilter names the product and/or the delivery
// container a caller wants rows for, Filters() renders that narrowing as
// the FilterSet a continuation token binds (paging.go), and the guard
// refuses a narrowing that crosses a scope or a product boundary before
// any row is read. task_console.go (claimed, cancelled, escalated) and
// task_note_console.go (open notes) all embed it, so one rule governs all
// four rather than each query re-deriving "is this mine to see?".
package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// The filter-set keys each read's token binds. Distinct per filter so two
// requests that set different filters never collide into one canonical
// encoding (paging.go's FilterSet contract).
const (
	filterKeyProductID        = "product_id"
	filterKeyMilestoneID      = "milestone_id"
	filterKeyEscalationReason = "escalation_reason"
)

// ConsoleFilter narrows a console queue read to one slice of its scope.
// Both fields are optional and independent: a ProductID keeps only rows
// whose task's delivery container belongs to that Product; a MilestoneID
// names a milestone or a milepebble and keeps only that container's rows,
// a milestone including its milepebbles' tasks. Neither set leaves the
// read's result exactly as it was before the filters existed.
type ConsoleFilter struct {
	// ProductID restricts to one Product's milestones and milepebbles.
	ProductID *uuid.UUID

	// MilestoneID restricts to one delivery container -- a milestone_ref
	// row of either Kind (FR calls both "a milestone or milepebble").
	MilestoneID *uuid.UUID
}

// IsZero reports whether f applies no narrowing at all -- the case whose
// reads must stay exactly what they were before ConsoleFilter existed,
// continuation token included.
func (f ConsoleFilter) IsZero() bool {
	return f.ProductID == nil && f.MilestoneID == nil
}

// Filters renders f as the FilterSet a token issued by this read binds
// (paging.go). An un-narrowed read yields an empty set, which
// EncodeFilteredContinuationToken omits entirely, so its token stays
// exactly the scope-only token EncodeContinuationToken produces today.
func (f ConsoleFilter) Filters() FilterSet {
	var filters FilterSet
	if f.ProductID != nil {
		filters = filters.WithUUID(filterKeyProductID, *f.ProductID)
	}
	if f.MilestoneID != nil {
		filters = filters.WithUUID(filterKeyMilestoneID, *f.MilestoneID)
	}
	return filters
}

// sqlPredicate renders f as the SQL fragment a console read appends after
// it has joined `milestone_ref` under alias refAlias, binding each set
// filter to the next free parameter after nextParam and returning those
// args in the same order. A milestone filter names one container and
// keeps its milepebbles' rows too, which is why it matches either the
// container's own id or the parent a milepebble is cut from.
//
// An un-narrowed read returns an empty fragment and no args, so its
// query is byte-for-byte what it was before ConsoleFilter existed.
func (f ConsoleFilter) sqlPredicate(refAlias string, nextParam int) (string, []any) {
	var clauses []string
	var args []any
	if f.ProductID != nil {
		clauses = append(clauses, fmt.Sprintf("AND %s.product_id = $%d", refAlias, nextParam))
		args = append(args, *f.ProductID)
		nextParam++
	}
	if f.MilestoneID != nil {
		clauses = append(clauses, fmt.Sprintf("AND (%s.id = $%d OR %s.parent_milestone_id = $%d)", refAlias, nextParam, refAlias, nextParam))
		args = append(args, *f.MilestoneID)
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return "\n\t\t\t" + strings.Join(clauses, "\n\t\t\t"), args
}

// guardConsoleFilter refuses a narrowing that names a Product or delivery
// container outside scopeID, or a container that is not one of the named
// Product's, so a console read can never answer with another scope's rows
// or with an empty page where the caller asked a question outside its
// boundary (LB1).
//
// The refusal is the store's own parent-shape rejection, errParentNotFound
// (ErrNotFound) -- the same one every write path's parent-existence guard
// returns (currentRowExists, errors.go), since a filter names a parent the
// caller's scope does not hold in exactly the way a write's parent column
// does. A container belonging to another Product is reported against the
// container, which is the id the caller got wrong.
func (s taskStore) guardConsoleFilter(ctx context.Context, scopeID uuid.UUID, f ConsoleFilter) error {
	if f.IsZero() {
		// An un-narrowed read has nothing to check, and keeps costing
		// exactly the round trips it did before ConsoleFilter existed.
		return nil
	}
	if f.ProductID != nil {
		exists, err := currentRowExists(ctx, s.pool, "product", *f.ProductID, scopeID)
		if err != nil {
			return err
		}
		if !exists {
			return errParentNotFound("product", *f.ProductID)
		}
	}
	if f.MilestoneID != nil {
		// deliversTarget reads the container's own product in one
		// query, and already refuses a container outside the scope.
		_, productID, err := deliversTarget(ctx, s.pool, scopeID, *f.MilestoneID)
		if err != nil {
			return err
		}
		if f.ProductID != nil && productID != *f.ProductID {
			return errParentNotFound("milestone_ref", *f.MilestoneID)
		}
	}
	return nil
}
