package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
//
// Exactly one of Model and ModelDefinitionID is set (migration 006's
// agent_definition_model_xor_model_definition CHECK constraint;
// config.Validate enforces the identical rule pre-seed). When
// ModelDefinitionID is set, it is preferred: the effective model and
// OpenRouter provider-routing preferences are resolved from the
// referenced model_definition row (worker/activities.go's
// ResolveAgentDefinition), not from Model, which is NULL in that case.
type AgentDefinition struct {
	AgentID           string
	Version           int
	Model             *string
	ModelDefinitionID *uuid.UUID
	ToolSet           []ToolServerRef
	MaxTurns          int
	MaxCostUSD        float64
	RequiredRole      *string
	CreatedAt         time.Time
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

const agentDefinitionColumns = `agent_id, version, model, model_definition_id, tool_set, max_turns, max_cost_usd, required_role, created_at`

func scanAgentDefinition(row pgx.Row) (*AgentDefinition, error) {
	var def AgentDefinition
	var toolSet json.RawMessage
	if err := row.Scan(
		&def.AgentID, &def.Version, &def.Model, &def.ModelDefinitionID, &toolSet,
		&def.MaxTurns, &def.MaxCostUSD, &def.RequiredRole, &def.CreatedAt,
	); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(toolSet, &def.ToolSet); err != nil {
		return nil, fmt.Errorf("unmarshal tool_set: %w", err)
	}
	return &def, nil
}

// GetLatest returns nil (not an error) when agentID has no rows.
func (s agentDefinitionStore) GetLatest(ctx context.Context, agentID string) (*AgentDefinition, error) {
	def, err := scanAgentDefinition(s.pool.QueryRow(ctx, `
		SELECT `+agentDefinitionColumns+`
		FROM agent_definition
		WHERE agent_id = $1
		ORDER BY version DESC
		LIMIT 1
	`, agentID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get latest agent definition: %w", err)
	}
	return def, nil
}

// GetVersion returns nil (not an error) when (agentID, version) does not
// exist.
func (s agentDefinitionStore) GetVersion(ctx context.Context, agentID string, version int) (*AgentDefinition, error) {
	def, err := scanAgentDefinition(s.pool.QueryRow(ctx, `
		SELECT `+agentDefinitionColumns+`
		FROM agent_definition
		WHERE agent_id = $1 AND version = $2
	`, agentID, version))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get agent definition version: %w", err)
	}
	return def, nil
}

// Upsert inserts (AgentID, Version) or, on conflict, replaces every column
// except created_at (a replace of an already-seeded definition keeps its
// original creation time rather than bumping it). Fills in CreatedAt on
// def either way.
func (s agentDefinitionStore) Upsert(ctx context.Context, def *AgentDefinition) error {
	toolSet, err := json.Marshal(def.ToolSet)
	if err != nil {
		return fmt.Errorf("marshal tool_set: %w", err)
	}

	err = s.pool.QueryRow(ctx, `
		INSERT INTO agent_definition (agent_id, version, model, model_definition_id, tool_set, max_turns, max_cost_usd, required_role)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (agent_id, version) DO UPDATE SET
			model = EXCLUDED.model,
			model_definition_id = EXCLUDED.model_definition_id,
			tool_set = EXCLUDED.tool_set,
			max_turns = EXCLUDED.max_turns,
			max_cost_usd = EXCLUDED.max_cost_usd,
			required_role = EXCLUDED.required_role
		RETURNING created_at
	`, def.AgentID, def.Version, def.Model, def.ModelDefinitionID, toolSet, def.MaxTurns, def.MaxCostUSD, def.RequiredRole).Scan(&def.CreatedAt)
	if err != nil {
		return fmt.Errorf("upsert agent definition: %w", err)
	}
	return nil
}

// AssignToSession writes the SCD2 close-and-open pair in one transaction
// (AGENTS.md "SCD2"): closes sessionID's currently-open session_agent row
// (if any), then opens a new one for (agentID, version). The partial
// unique index on (session_id) WHERE valid_to IS NULL (migration 001)
// rejects two open rows for the same session even if this ever races.
func (s agentDefinitionStore) AssignToSession(ctx context.Context, sessionID uuid.UUID, agentID string, version int) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
		UPDATE session_agent SET valid_to = NOW()
		WHERE session_id = $1 AND valid_to IS NULL
	`, sessionID); err != nil {
		return fmt.Errorf("close current session_agent assignment: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO session_agent (session_id, agent_id, agent_version)
		VALUES ($1, $2, $3)
	`, sessionID, agentID, version); err != nil {
		return fmt.Errorf("open new session_agent assignment: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// CurrentAssignment returns nil (not an error) when sessionID has no open
// session_agent row.
func (s agentDefinitionStore) CurrentAssignment(ctx context.Context, sessionID uuid.UUID) (*SessionAgent, error) {
	var sa SessionAgent
	err := s.pool.QueryRow(ctx, `
		SELECT session_id, agent_id, agent_version, valid_from, valid_to
		FROM session_agent
		WHERE session_id = $1 AND valid_to IS NULL
	`, sessionID).Scan(&sa.SessionID, &sa.AgentID, &sa.AgentVersion, &sa.ValidFrom, &sa.ValidTo)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get current session_agent assignment: %w", err)
	}
	return &sa, nil
}
