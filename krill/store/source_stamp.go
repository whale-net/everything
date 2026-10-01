package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ProductSourceTime returns the most recent change time (an SCD2 open or
// close) across a Product's spec entities: the Product itself, its
// FeatureSets, Features, Requirements, LoadBearingDecisions, Personas and
// NonGoals. It is the "source as of" time a rendered doc set is stamped
// with. A Product with no rows returns the zero time.
func (s *Store) ProductSourceTime(ctx context.Context, productID uuid.UUID) (time.Time, error) {
	var t *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT MAX(GREATEST(valid_from, COALESCE(valid_to, valid_from))) FROM (
			SELECT valid_from, valid_to FROM product WHERE id = $1
			UNION ALL SELECT valid_from, valid_to FROM persona WHERE product_id = $1
			UNION ALL SELECT valid_from, valid_to FROM non_goal WHERE product_id = $1
			UNION ALL SELECT valid_from, valid_to FROM feature_set WHERE product_id = $1
			UNION ALL SELECT f.valid_from, f.valid_to FROM feature f
				JOIN feature_set fs ON fs.id = f.feature_set_id WHERE fs.product_id = $1
			UNION ALL SELECT r.valid_from, r.valid_to FROM requirement r
				JOIN feature f ON f.id = r.feature_id
				JOIN feature_set fs ON fs.id = f.feature_set_id WHERE fs.product_id = $1
			UNION ALL SELECT d.valid_from, d.valid_to FROM load_bearing_decision d
				JOIN feature_set fs ON fs.id = d.feature_set_id WHERE fs.product_id = $1
		) x
	`, productID).Scan(&t)
	if err != nil {
		return time.Time{}, fmt.Errorf("product source time: %w", err)
	}
	if t == nil {
		return time.Time{}, nil
	}
	return t.UTC(), nil
}
