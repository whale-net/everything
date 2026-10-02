package server

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"
)

// ConfirmationTTL is how long a confirmation token stays valid.
const ConfirmationTTL = 5 * time.Minute

// ConfirmationToken is a persisted, single-use grant to run one gated call.
type ConfirmationToken struct {
	ID          string
	Issuer      string
	Subject     string
	Tool        string
	ArgsHash    string
	Fingerprint string
	ExpiresAt   time.Time
}

// ErrTokenNotConsumable means the token is unknown, expired, already used, or
// bound to a different caller, tool, or arguments.
var ErrTokenNotConsumable = errors.New("confirmation token is invalid, expired, or already used")

// ConfirmationStore persists tokens. Consume must be atomic and match the
// binding (caller, tool, args hash) in the same conditional step, so a
// mismatched attempt never burns the legitimate holder's token.
type ConfirmationStore interface {
	Issue(ctx context.Context, t ConfirmationToken) error
	// Consume marks the token used and returns the fingerprint captured at preview.
	Consume(ctx context.Context, id string, bind ConfirmationToken) (fingerprint string, err error)
	// ArgsMismatch reports whether id is a live token for the same caller and
	// tool but different args (diagnostics only; does not mutate).
	ArgsMismatch(ctx context.Context, id string, bind ConfirmationToken) bool
}

// SQLConfirmationStore stores tokens in mcp_confirmation_token; consume is a
// conditional UPDATE so single-use holds across replicas and restarts.
type SQLConfirmationStore struct{ DB *sql.DB }

func (s SQLConfirmationStore) Issue(ctx context.Context, t ConfirmationToken) error {
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO mcp_confirmation_token
		   (token_id, caller_issuer, caller_subject, tool_name, args_hash, entity_fingerprint, expires_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		t.ID, t.Issuer, t.Subject, t.Tool, t.ArgsHash, t.Fingerprint, t.ExpiresAt)
	return err
}

func (s SQLConfirmationStore) Consume(ctx context.Context, id string, b ConfirmationToken) (string, error) {
	var fp string
	err := s.DB.QueryRowContext(ctx,
		`UPDATE mcp_confirmation_token SET consumed_at = NOW()
		 WHERE token_id = $1 AND consumed_at IS NULL AND expires_at > NOW()
		   AND caller_issuer = $2 AND caller_subject = $3 AND tool_name = $4 AND args_hash = $5
		 RETURNING entity_fingerprint`,
		id, b.Issuer, b.Subject, b.Tool, b.ArgsHash).Scan(&fp)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrTokenNotConsumable
	}
	return fp, err
}

func (s SQLConfirmationStore) ArgsMismatch(ctx context.Context, id string, b ConfirmationToken) bool {
	var ok bool
	err := s.DB.QueryRowContext(ctx,
		`SELECT TRUE FROM mcp_confirmation_token
		 WHERE token_id = $1 AND consumed_at IS NULL AND expires_at > NOW()
		   AND caller_issuer = $2 AND caller_subject = $3 AND tool_name = $4 AND args_hash <> $5`,
		id, b.Issuer, b.Subject, b.Tool, b.ArgsHash).Scan(&ok)
	return err == nil && ok
}

// MemoryConfirmationStore is an in-process store for tests and local runs;
// it is not shared across replicas.
type MemoryConfirmationStore struct {
	Now  func() time.Time
	mu   sync.Mutex
	toks map[string]*memTok
}

type memTok struct {
	ConfirmationToken
	used bool
}

func (m *MemoryConfirmationStore) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *MemoryConfirmationStore) Issue(_ context.Context, t ConfirmationToken) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.toks == nil {
		m.toks = map[string]*memTok{}
	}
	m.toks[t.ID] = &memTok{ConfirmationToken: t}
	return nil
}

func (m *MemoryConfirmationStore) live(id string) *memTok {
	t := m.toks[id]
	if t == nil || t.used || !m.now().Before(t.ExpiresAt) {
		return nil
	}
	return t
}

func (m *MemoryConfirmationStore) Consume(_ context.Context, id string, b ConfirmationToken) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.live(id)
	if t == nil || t.Issuer != b.Issuer || t.Subject != b.Subject || t.Tool != b.Tool || t.ArgsHash != b.ArgsHash {
		return "", ErrTokenNotConsumable
	}
	t.used = true
	return t.Fingerprint, nil
}

func (m *MemoryConfirmationStore) ArgsMismatch(_ context.Context, id string, b ConfirmationToken) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.live(id)
	return t != nil && t.Issuer == b.Issuer && t.Subject == b.Subject && t.Tool == b.Tool && t.ArgsHash != b.ArgsHash
}
