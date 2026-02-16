package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/whale-net/everything/manmanv2"
)

type ServerPortRepository struct {
	db *pgxpool.Pool
}

func NewServerPortRepository(db *pgxpool.Pool) *ServerPortRepository {
	return &ServerPortRepository{db: db}
}

// AllocatePort allocates a port on a server for a specific ServerGameConfig
func (r *ServerPortRepository) AllocatePort(ctx context.Context, serverID int64, port int, protocol string, sessionID int64) error {
	// Validate inputs
	if err := validatePort(port); err != nil {
		return err
	}
	if err := validateProtocol(protocol); err != nil {
		return err
	}
	if sessionID <= 0 {
		return &InvalidSessionIDError{SessionID: sessionID}
	}

	query := `
		INSERT INTO server_ports (server_id, port, protocol, session_id, allocated_at)
		VALUES ($1, $2, $3, $4, $5)
	`

	_, err := r.db.Exec(ctx, query, serverID, port, protocol, sessionID, time.Now())
	if err != nil {
		// Check for unique constraint violation (port conflict)
		if isPgUniqueViolation(err) {
			// Get existing allocation to provide better error message
			existing, _ := r.GetPortAllocation(ctx, serverID, port, protocol)
			existingSGC := int64(0)
			if existing != nil && existing.SessionID != nil {
				existingSGC = *existing.SessionID
			}
			return &PortConflictError{
				ServerID:     serverID,
				Port:         port,
				Protocol:     protocol,
				ExistingSGC:  existingSGC,
				RequestedSGC: sessionID,
			}
		}
		return fmt.Errorf("failed to allocate port: %w", err)
	}

	return nil
}

// DeallocatePort removes a port allocation
func (r *ServerPortRepository) DeallocatePort(ctx context.Context, serverID int64, port int, protocol string) error {
	query := `
		DELETE FROM server_ports
		WHERE server_id = $1 AND port = $2 AND protocol = $3
	`

	_, err := r.db.Exec(ctx, query, serverID, port, protocol)
	return err
}

// IsPortAvailable checks if a port is available for allocation
func (r *ServerPortRepository) IsPortAvailable(ctx context.Context, serverID int64, port int, protocol string) (bool, error) {
	query := `
		SELECT EXISTS(
			SELECT 1 FROM server_ports
			WHERE server_id = $1 AND port = $2 AND protocol = $3
		)
	`

	var exists bool
	err := r.db.QueryRow(ctx, query, serverID, port, protocol).Scan(&exists)
	if err != nil {
		return false, err
	}

	return !exists, nil
}

// GetPortAllocation retrieves the allocation details for a specific port
func (r *ServerPortRepository) GetPortAllocation(ctx context.Context, serverID int64, port int, protocol string) (*manman.ServerPort, error) {
	query := `
		SELECT server_id, port, protocol, sgc_id, session_id, allocated_at
		FROM server_ports
		WHERE server_id = $1 AND port = $2 AND protocol = $3
	`

	allocation := &manman.ServerPort{}
	err := r.db.QueryRow(ctx, query, serverID, port, protocol).Scan(
		&allocation.ServerID,
		&allocation.Port,
		&allocation.Protocol,
		&allocation.SGCID,
		&allocation.SessionID,
		&allocation.AllocatedAt,
	)

	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, &PortNotFoundError{
				ServerID: serverID,
				Port:     port,
				Protocol: protocol,
			}
		}
		return nil, err
	}

	return allocation, nil
}

// ListAllocatedPorts lists all port allocations for a server
func (r *ServerPortRepository) ListAllocatedPorts(ctx context.Context, serverID int64) ([]*manman.ServerPort, error) {
	query := `
		SELECT server_id, port, protocol, sgc_id, session_id, allocated_at
		FROM server_ports
		WHERE server_id = $1
		ORDER BY port, protocol
	`

	rows, err := r.db.Query(ctx, query, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ports []*manman.ServerPort
	for rows.Next() {
		port := &manman.ServerPort{}
		err := rows.Scan(
			&port.ServerID,
			&port.Port,
			&port.Protocol,
			&port.SGCID,
			&port.SessionID,
			&port.AllocatedAt,
		)
		if err != nil {
			return nil, err
		}
		ports = append(ports, port)
	}

	return ports, rows.Err()
}

// ListPortsBySessionID lists all port allocations for a specific ServerGameConfig
func (r *ServerPortRepository) ListPortsBySessionID(ctx context.Context, sessionID int64) ([]*manman.ServerPort, error) {
	query := `
		SELECT server_id, port, protocol, sgc_id, session_id, allocated_at
		FROM server_ports
		WHERE session_id = $1
		ORDER BY server_id, port, protocol
	`

	rows, err := r.db.Query(ctx, query, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ports []*manman.ServerPort
	for rows.Next() {
		port := &manman.ServerPort{}
		err := rows.Scan(
			&port.ServerID,
			&port.Port,
			&port.Protocol,
			&port.SGCID,
			&port.SessionID,
			&port.AllocatedAt,
		)
		if err != nil {
			return nil, err
		}
		ports = append(ports, port)
	}

	return ports, rows.Err()
}

// DeallocatePortsBySessionID deallocates all ports for a ServerGameConfig
func (r *ServerPortRepository) DeallocatePortsBySessionID(ctx context.Context, sessionID int64) error {
	query := `
		DELETE FROM server_ports
		WHERE session_id = $1
	`

	_, err := r.db.Exec(ctx, query, sessionID)
	return err
}

// AllocateMultiplePorts allocates multiple ports in a transaction
func (r *ServerPortRepository) AllocateMultiplePorts(ctx context.Context, serverID int64, portBindings []*manman.PortBinding, sessionID int64) error {
	// Start transaction
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// Check all ports for availability first
	for _, binding := range portBindings {
		checkQuery := `
			SELECT EXISTS(
				SELECT 1 FROM server_ports
				WHERE server_id = $1 AND port = $2 AND protocol = $3
			)
		`
		var exists bool
		err := tx.QueryRow(ctx, checkQuery, serverID, binding.HostPort, binding.Protocol).Scan(&exists)
		if err != nil {
			return err
		}
		if exists {
			return &PortConflictError{
				ServerID: serverID,
				Port:     int(binding.HostPort),
				Protocol: binding.Protocol,
			}
		}
	}

	// Allocate all ports
	insertQuery := `
		INSERT INTO server_ports (server_id, port, protocol, session_id, allocated_at)
		VALUES ($1, $2, $3, $4, $5)
	`

	for _, binding := range portBindings {
		// Validate port and protocol
		if err := validatePort(int(binding.HostPort)); err != nil {
			return err
		}
		if err := validateProtocol(binding.Protocol); err != nil {
			return err
		}

		_, err := tx.Exec(ctx, insertQuery, serverID, binding.HostPort, binding.Protocol, sessionID, time.Now())
		if err != nil {
			return err
		}
	}

	// Commit transaction
	return tx.Commit(ctx)
}

// GetAvailablePortsInRange finds available ports in a specified range
func (r *ServerPortRepository) GetAvailablePortsInRange(ctx context.Context, serverID int64, protocol string, startPort, endPort, limit int) ([]int, error) {
	if err := validateProtocol(protocol); err != nil {
		return nil, err
	}

	// Generate series of port numbers and filter out allocated ones
	query := `
		SELECT port_num
		FROM generate_series($1, $2) AS port_num
		WHERE NOT EXISTS (
			SELECT 1 FROM server_ports
			WHERE server_id = $3
			  AND port = port_num
			  AND protocol = $4
		)
		LIMIT $5
	`

	rows, err := r.db.Query(ctx, query, startPort, endPort, serverID, protocol, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var availablePorts []int
	for rows.Next() {
		var port int
		if err := rows.Scan(&port); err != nil {
			return nil, err
		}
		availablePorts = append(availablePorts, port)
	}

	return availablePorts, rows.Err()
}

// Validation helpers

func validatePort(port int) error {
	if port < 1 || port > 65535 {
		return &InvalidPortError{Port: port}
	}
	return nil
}

func validateProtocol(protocol string) error {
	if protocol != "TCP" && protocol != "UDP" {
		return &InvalidProtocolError{Protocol: protocol}
	}
	return nil
}

func isPgUniqueViolation(err error) bool {
	// Check if error is a PostgreSQL unique constraint violation
	// Error code 23505 is unique_violation
	return err != nil && (
		// pgx v5 error codes
		err.Error() == "ERROR: duplicate key value violates unique constraint (SQLSTATE 23505)" ||
		// Or contains the error code
		contains(err.Error(), "23505") ||
		contains(err.Error(), "unique constraint") ||
		contains(err.Error(), "duplicate key"))
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && (
		s[:len(substr)] == substr ||
		s[len(s)-len(substr):] == substr ||
		containsHelper(s, substr)))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// Error types (defined in server_port_test.go, duplicated here for implementation)

type PortConflictError struct {
	ServerID     int64
	Port         int
	Protocol     string
	ExistingSGC  int64
	RequestedSGC int64
}

func (e *PortConflictError) Error() string {
	return fmt.Sprintf("port %d/%s on server %d is already allocated", e.Port, e.Protocol, e.ServerID)
}

func IsPortConflictError(err error) bool {
	_, ok := err.(*PortConflictError)
	return ok
}

type InvalidPortError struct {
	Port int
}

func (e *InvalidPortError) Error() string {
	return fmt.Sprintf("invalid port number: %d (must be 1-65535)", e.Port)
}

type InvalidProtocolError struct {
	Protocol string
}

func (e *InvalidProtocolError) Error() string {
	return fmt.Sprintf("invalid protocol: %s (must be TCP or UDP)", e.Protocol)
}

type InvalidSessionIDError struct {
	SessionID int64
}

func (e *InvalidSessionIDError) Error() string {
	return fmt.Sprintf("invalid SessionID: %d (must be > 0)", e.SessionID)
}

type InvalidSGCIDError struct {
	SGCID int64
}

func (e *InvalidSGCIDError) Error() string {
	return fmt.Sprintf("invalid SGCID: %d (must be > 0)", e.SGCID)
}

type PortNotFoundError struct {
	ServerID int64
	Port     int
	Protocol string
}

func (e *PortNotFoundError) Error() string {
	return fmt.Sprintf("port %d/%s not found on server %d", e.Port, e.Protocol, e.ServerID)
}
