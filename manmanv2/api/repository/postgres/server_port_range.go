package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	manman "github.com/whale-net/everything/manmanv2/models"
)

// ServerPortRangeRepository stores a server's allowed host-port ranges
// (FR12, migration 038 / task #2095). Replace is replace-all semantics; an
// empty slice clears the set, which leaves host-port assignment
// unconstrained (SB-1.2). Save-time validation stays out of scope per
// root-plan Decision 7.
type ServerPortRangeRepository struct {
	db *pgxpool.Pool
}

func NewServerPortRangeRepository(db *pgxpool.Pool) *ServerPortRangeRepository {
	return &ServerPortRangeRepository{db: db}
}

// List returns the allowed host-port ranges for a server, ordered for
// stable display (protocol, then start port).
func (r *ServerPortRangeRepository) List(ctx context.Context, serverID int64) ([]*manman.ServerAllowedPortRange, error) {
	query := `
		SELECT range_id, server_id, start_port, end_port, protocol
		FROM server_allowed_port_ranges
		WHERE server_id = $1
		ORDER BY protocol ASC, start_port ASC, end_port ASC, range_id ASC
	`

	rows, err := r.db.Query(ctx, query, serverID)
	if err != nil {
		return nil, fmt.Errorf("failed to list server allowed port ranges: %w", err)
	}
	defer rows.Close()

	ranges := []*manman.ServerAllowedPortRange{}
	for rows.Next() {
		var pr manman.ServerAllowedPortRange
		if err := rows.Scan(&pr.RangeID, &pr.ServerID, &pr.StartPort, &pr.EndPort, &pr.Protocol); err != nil {
			return nil, fmt.Errorf("failed to scan server allowed port range: %w", err)
		}
		ranges = append(ranges, &pr)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate server allowed port ranges: %w", err)
	}

	return ranges, nil
}

// Replace atomically swaps the server's full allowed-range set and returns
// the stored rows (with range IDs) in List order. The delete+insert runs in
// one transaction so a concurrent reader never observes a partial set.
//
// The return values are named so every return path — including the
// mid-loop port-order validation error — populates err for the deferred
// rollback. An unnamed return skipped that assignment and leaked the
// transaction's pooled connection (Pool.Close then blocked forever in
// tests; Task #2095 hang).
func (r *ServerPortRangeRepository) Replace(ctx context.Context, serverID int64, ranges []*manman.ServerAllowedPortRange) (stored []*manman.ServerAllowedPortRange, err error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(ctx)
		}
	}()

	if _, err = tx.Exec(ctx, `DELETE FROM server_allowed_port_ranges WHERE server_id = $1`, serverID); err != nil {
		return nil, fmt.Errorf("failed to clear server allowed port ranges: %w", err)
	}

	for _, pr := range ranges {
		if pr == nil {
			continue
		}
		if err = validateProtocol(pr.Protocol); err != nil {
			return nil, err
		}
		if pr.StartPort <= 0 || pr.EndPort < pr.StartPort {
			err = fmt.Errorf("invalid port range %d-%d: require start >= 1 and end >= start", pr.StartPort, pr.EndPort)
			return nil, err
		}
		if _, err = tx.Exec(ctx, `
			INSERT INTO server_allowed_port_ranges (server_id, start_port, end_port, protocol)
			VALUES ($1, $2, $3, $4)
		`, serverID, pr.StartPort, pr.EndPort, pr.Protocol); err != nil {
			return nil, fmt.Errorf("failed to insert server allowed port range: %w", err)
		}
	}

	if err = tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("failed to commit server allowed port ranges: %w", err)
	}

	return r.List(ctx, serverID)
}
