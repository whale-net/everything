package session

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ToolServerRef is one entry of an agent definition's tool_set JSONB array
// (LB5, ARCHITECTURE.md "Domain-owned MCP servers and the tool contract"):
// an MCP endpoint plus the tool names an agent using this definition may
// see there. A nil AllowedTools means "whatever the server exposes" --
// initial tool selection is enforced server-side via a pre-filtered
// endpoint (e.g. `/mcp/research`), not by whagent-side filtering.
type ToolServerRef struct {
	ServerURL    string   `json:"server_url"`
	AllowedTools []string `json:"allowed_tools,omitempty"`
}

// AgentDefinition is an `agent_definition` row (LB5/NFR6): a named,
// role-shaped tool set plus the model and guardrail defaults a session
// inherits unless overridden (FR5/FR6/FR7, FR9's required_role).
// (AgentID, Version) is the primary key -- versions are never mutated in
// place, only inserted.
type AgentDefinition struct {
	AgentID      string
	Version      int
	Model        string
	ToolSet      []ToolServerRef
	MaxTurns     int
	MaxCostUSD   float64
	RequiredRole *string
	CreatedAt    time.Time
}

// SessionAgent is a `session_agent` row (LB5/NFR6): the SCD2 history of
// which AgentDefinition version a session is currently assigned to, per
// AGENTS.md's SCD2 convention (valid_from/valid_to exactly). Exactly one
// row per session has ValidTo nil (enforced by the partial unique index
// migration 001 creates).
type SessionAgent struct {
	SessionID    uuid.UUID
	AgentID      string
	AgentVersion int
	ValidFrom    time.Time
	ValidTo      *time.Time
}

// AgentDefinitionStore is the `agent_definition` and `session_agent`
// tables' repository interface.
type AgentDefinitionStore interface {
	// GetLatest returns the highest-Version AgentDefinition row for
	// agentID.
	GetLatest(ctx context.Context, agentID string) (*AgentDefinition, error)
	// GetVersion returns the exact (agentID, version) AgentDefinition row.
	GetVersion(ctx context.Context, agentID string, version int) (*AgentDefinition, error)
	// Upsert inserts or replaces the (AgentID, Version) row -- M1 seeds
	// agent_definition from config via this method (see #2109's issue
	// body).
	Upsert(ctx context.Context, def *AgentDefinition) error
	// AssignToSession writes the SCD2 close-and-open pair: closes the
	// session's currently-open session_agent row (if any) and opens a new
	// one for (agentID, version).
	AssignToSession(ctx context.Context, sessionID uuid.UUID, agentID string, version int) error
	// CurrentAssignment returns the session's open (ValidTo nil)
	// session_agent row.
	CurrentAssignment(ctx context.Context, sessionID uuid.UUID) (*SessionAgent, error)
}

// agentDefinitionStore is the Postgres-backed AgentDefinitionStore
// implementation.
type agentDefinitionStore struct{ pool *pgxpool.Pool }

var _ AgentDefinitionStore = agentDefinitionStore{}

func (s agentDefinitionStore) GetLatest(ctx context.Context, agentID string) (*AgentDefinition, error) {
	return nil, errNotImplemented
}

func (s agentDefinitionStore) GetVersion(ctx context.Context, agentID string, version int) (*AgentDefinition, error) {
	return nil, errNotImplemented
}

func (s agentDefinitionStore) Upsert(ctx context.Context, def *AgentDefinition) error {
	return errNotImplemented
}

func (s agentDefinitionStore) AssignToSession(ctx context.Context, sessionID uuid.UUID, agentID string, version int) error {
	return errNotImplemented
}

func (s agentDefinitionStore) CurrentAssignment(ctx context.Context, sessionID uuid.UUID) (*SessionAgent, error) {
	return nil, errNotImplemented
}
