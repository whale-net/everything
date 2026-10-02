package server

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"
)

// IdemKey identifies one idempotent call: the caller, the tool, and the
// caller-chosen key.
type IdemKey struct {
	Issuer, Subject, Tool, Key string
}

// IdemState is the outcome of reserving a key.
type IdemState int

const (
	// IdemReserved: this caller owns the key and must run the backend call,
	// then Complete or Release.
	IdemReserved IdemState = iota
	// IdemReplay: a completed call exists with the same args; Result holds it.
	IdemReplay
	// IdemConflict: the key was used with different args.
	IdemConflict
	// IdemInFlight: another call with the same key and args is still running.
	IdemInFlight
)

// IdemReservation is the result of IdempotencyStore.Reserve.
type IdemReservation struct {
	State  IdemState
	Result []byte
}

// IdempotencyStore persists idempotency records. Reserve must be atomic so
// that of any number of concurrent identical calls exactly one is Reserved.
type IdempotencyStore interface {
	Reserve(ctx context.Context, k IdemKey, argsHash string) (IdemReservation, error)
	// Complete stores the result of a Reserved call.
	Complete(ctx context.Context, k IdemKey, result []byte) error
	// Release drops a Reserved call's reservation (backend failed) so a retry may run.
	Release(ctx context.Context, k IdemKey) error
}

// pendingResult marks a reserved-but-unfinished row in the result column.
const pendingResult = "null"

// reservationTTL is how long a pending reservation blocks retries before a
// crashed holder is presumed dead and the key may be taken over.
const reservationTTL = 2 * time.Minute

// SQLIdempotencyStore stores records in mcp_idempotency_record (Postgres), so
// replay works across restarts and replicas.
type SQLIdempotencyStore struct{ DB *sql.DB }

func (s SQLIdempotencyStore) Reserve(ctx context.Context, k IdemKey, argsHash string) (IdemReservation, error) {
	res, err := s.DB.ExecContext(ctx, `
INSERT INTO mcp_idempotency_record (caller_issuer, caller_subject, tool_name, idempotency_key, args_hash, result)
VALUES ($1, $2, $3, $4, $5, 'null'::jsonb)
ON CONFLICT DO NOTHING`, k.Issuer, k.Subject, k.Tool, k.Key, argsHash)
	if err != nil {
		return IdemReservation{}, err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return IdemReservation{State: IdemReserved}, nil
	}
	var storedHash string
	var result []byte
	var age float64
	err = s.DB.QueryRowContext(ctx, `
SELECT args_hash, result, EXTRACT(EPOCH FROM (NOW() - created_at))
FROM mcp_idempotency_record
WHERE caller_issuer=$1 AND caller_subject=$2 AND tool_name=$3 AND idempotency_key=$4`,
		k.Issuer, k.Subject, k.Tool, k.Key).Scan(&storedHash, &result, &age)
	if errors.Is(err, sql.ErrNoRows) {
		// Released between the insert and the select; the caller may retry.
		return IdemReservation{State: IdemInFlight}, nil
	}
	if err != nil {
		return IdemReservation{}, err
	}
	if storedHash != argsHash {
		return IdemReservation{State: IdemConflict}, nil
	}
	if string(result) != pendingResult {
		return IdemReservation{State: IdemReplay, Result: result}, nil
	}
	if time.Duration(age*float64(time.Second)) < reservationTTL {
		return IdemReservation{State: IdemInFlight}, nil
	}
	// Stale pending row: take it over atomically.
	res, err = s.DB.ExecContext(ctx, `
UPDATE mcp_idempotency_record SET created_at = NOW()
WHERE caller_issuer=$1 AND caller_subject=$2 AND tool_name=$3 AND idempotency_key=$4
  AND args_hash=$5 AND result='null'::jsonb AND created_at < NOW() - make_interval(secs => $6)`,
		k.Issuer, k.Subject, k.Tool, k.Key, argsHash, reservationTTL.Seconds())
	if err != nil {
		return IdemReservation{}, err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return IdemReservation{State: IdemReserved}, nil
	}
	return IdemReservation{State: IdemInFlight}, nil
}

func (s SQLIdempotencyStore) Complete(ctx context.Context, k IdemKey, result []byte) error {
	_, err := s.DB.ExecContext(ctx, `
UPDATE mcp_idempotency_record SET result=$5::jsonb
WHERE caller_issuer=$1 AND caller_subject=$2 AND tool_name=$3 AND idempotency_key=$4`,
		k.Issuer, k.Subject, k.Tool, k.Key, string(result))
	return err
}

func (s SQLIdempotencyStore) Release(ctx context.Context, k IdemKey) error {
	_, err := s.DB.ExecContext(ctx, `
DELETE FROM mcp_idempotency_record
WHERE caller_issuer=$1 AND caller_subject=$2 AND tool_name=$3 AND idempotency_key=$4
  AND result='null'::jsonb`, k.Issuer, k.Subject, k.Tool, k.Key)
	return err
}

// MemIdempotencyStore is an in-process store for tests and single-replica dev.
type MemIdempotencyStore struct {
	mu   sync.Mutex
	rows map[IdemKey]*memRow
}

type memRow struct {
	hash   string
	result []byte
	done   bool
}

func (m *MemIdempotencyStore) Reserve(_ context.Context, k IdemKey, argsHash string) (IdemReservation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rows == nil {
		m.rows = map[IdemKey]*memRow{}
	}
	r, ok := m.rows[k]
	switch {
	case !ok:
		m.rows[k] = &memRow{hash: argsHash}
		return IdemReservation{State: IdemReserved}, nil
	case r.hash != argsHash:
		return IdemReservation{State: IdemConflict}, nil
	case r.done:
		return IdemReservation{State: IdemReplay, Result: r.result}, nil
	}
	return IdemReservation{State: IdemInFlight}, nil
}

func (m *MemIdempotencyStore) Complete(_ context.Context, k IdemKey, result []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r := m.rows[k]; r != nil {
		r.result, r.done = result, true
	}
	return nil
}

func (m *MemIdempotencyStore) Release(_ context.Context, k IdemKey) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r := m.rows[k]; r != nil && !r.done {
		delete(m.rows, k)
	}
	return nil
}
