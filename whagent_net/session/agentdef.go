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
// see there. A nil/empty AllowedTools means "whatever the server exposes."
// Tool selection is the intersection of two filters: server-side, via a
// pre-filtered endpoint (e.g. `/mcp/research`), and, when AllowedTools is
// non-empty, whagent-side narrowing (C22) enforced by
// whagent_net/worker/tools' ListToolDefinitions/Dispatch.
type ToolServerRef struct {
	ServerURL    string   `json:"server_url"`
	AllowedTools []string `json:"allowed_tools,omitempty"`
}

// ToolLoadingMode is an agent_definition row's tool_loading_mode column
// (FR1): whether a session using that definition loads its full ToolSet up
// front ("bulk") or discovers tools by search at call time ("search", M4's
// opt-in). The zero value ("") means "bulk" -- callers must not treat an
// empty ToolLoadingMode as invalid or distinct from ToolLoadingModeBulk.
// FR2 guarantees a "bulk" (or unset) definition's behavior stays
// byte-for-byte unchanged by anything this milestone adds.
type ToolLoadingMode string

const (
	ToolLoadingModeBulk   ToolLoadingMode = "bulk"
	ToolLoadingModeSearch ToolLoadingMode = "search"
)

// AgentDefinition is an `agent_definition` row (LB5/NFR6): a named,
// role-shaped tool set plus the model and guardrail defaults a session
// inherits unless overridden (FR5/FR6/FR7, FR9's required_role).
// ID is the surrogate primary key (migration 009): a single stable handle
// for one row, so callers, logs, and any future FK never need to repeat
// both AgentID and a validity window to name one. AgentID remains the stable,
// human-authored business key (agents.yaml, MCP tool inputs, UI filters --
// LB5/NFR6). Rows are never mutated in place, only inserted.
//
// Exactly one of Model and ModelDefinitionID is set (migration 006's
// agent_definition_model_xor_model_definition CHECK constraint;
// config.Validate enforces the identical rule pre-seed). When
// ModelDefinitionID is set, it is preferred: the effective model and
// OpenRouter provider-routing preferences are resolved from the
// referenced model_definition row (worker/activities.go's
// ResolveAgentDefinition), not from Model, which is NULL in that case.
// Scope is optional (migration 009): when set, it is the one grant-scope
// this agent definition belongs to, and the sole input
// whagent_net/grantkey.ForScope may derive a delegated-grant key from
// (FR4) -- never agent_id, required_role, or a tool_set[].server_url.
// Every tool_set entry is understood to belong to this same scope, by
// construction; there is no per-entry scope field to reconcile against
// it. A nil Scope means the agent runs with no delegated-grant scoping at
// all -- it still gets whatever ToolSet is configured for it, just without
// a cross-domain grant key derived or checked.
type AgentDefinition struct {
	ID                uuid.UUID
	AgentID           string
	Scope             *string
	Model             *string
	ModelDefinitionID *uuid.UUID
	ToolSet           []ToolServerRef
	MaxTurns          int
	MaxCostUSD        float64
	// MaxToolIterations bounds the inner tool-call loop worker/workflow.go's
	// processTurn runs within a single external turn (issue: "add the inner
	// tool loop"): the maximum number of model calls one turn may make
	// while the model keeps requesting tool calls before the session ends
	// capped (session.CapKindToolIterations) instead of looping without
	// bound. Zero-valued (unset) falls back to worker/caps.go's
	// defaultMaxToolIterations, the same convention MaxTurns/MaxCostUSD
	// already follow.
	MaxToolIterations int
	RequiredRole      *string
	ToolLoadingMode   ToolLoadingMode
	// SystemPrompt is an optional system-role instruction text for a
	// session using this definition (migration 015). Nil means no system
	// prompt is set -- the historical, still-default behavior. Forwarded
	// into CallModelInput.SystemPrompt (worker/activities.go) and prepended
	// to the request as a RoleSystem message when set.
	SystemPrompt *string
	CreatedAt    time.Time
	// ValidFrom/ValidTo are the SCD2 window; ValidTo nil marks the current row.
	ValidFrom time.Time
	ValidTo   *time.Time
}

// SessionAgent is a `session_agent` row (LB5/NFR6): the SCD2 history of
// which AgentDefinition version a session is currently assigned to, per
// AGENTS.md's SCD2 convention (valid_from/valid_to exactly). Exactly one
// row per session has ValidTo nil (enforced by the partial unique index
// migration 001 creates).
type SessionAgent struct {
	SessionID         uuid.UUID
	AgentID           string
	AgentDefinitionID uuid.UUID
	ValidFrom         time.Time
	ValidTo           *time.Time
}

// AgentDefinitionStore is the `agent_definition` and `session_agent`
// tables' repository interface.
type AgentDefinitionStore interface {
	// GetCurrent returns agentID's open (valid_to IS NULL) AgentDefinition
	// row, or nil, nil when agentID has none.
	GetCurrent(ctx context.Context, agentID string) (*AgentDefinition, error)
	// GetByID returns the AgentDefinition row with surrogate id, open or
	// superseded, or nil, nil when absent.
	GetByID(ctx context.Context, id uuid.UUID) (*AgentDefinition, error)
	// Upsert inserts def as agentID's current row (fixture writer; it does
	// not replace an existing row in place). Fills in ID, CreatedAt and
	// ValidFrom.
	Upsert(ctx context.Context, def *AgentDefinition) error
	// Register inserts def as agentID's current row only when agentID has
	// none; created reports whether it did. It never updates a row, and
	// concurrent registrations of one agent_id leave exactly one current
	// row with no error. Fills in ID, CreatedAt and ValidFrom when created.
	Register(ctx context.Context, def *AgentDefinition) (created bool, err error)
	// AssignToSession writes the SCD2 close-and-open pair: closes the
	// session's currently-open session_agent row (if any) and opens a new
	// one pinned to agentDefinitionID.
	AssignToSession(ctx context.Context, sessionID uuid.UUID, agentDefinitionID uuid.UUID) error
	// CurrentAssignment returns the session's open (ValidTo nil)
	// session_agent row.
	CurrentAssignment(ctx context.Context, sessionID uuid.UUID) (*SessionAgent, error)
	// ListScopes returns every distinct non-null Scope value across all
	// agent_definition rows (every agent_id and version), sorted
	// alphabetically -- the full set of grant-scopes an operator could
	// ever need to consent to (whagent_net/grantkey.ForScope's input),
	// not scoped to any one agent_id, version, or session. Backs `api`'s
	// ListAgentDefinitionScopes RPC, which `ui`'s self-service /grants
	// page (issue #2432) uses to offer a clickable consent link instead
	// of requiring a hand-typed /mcp/consent?scope=<s> URL.
	ListScopes(ctx context.Context) ([]string, error)
}

// agentDefinitionStore is the Postgres-backed AgentDefinitionStore
// implementation.
type agentDefinitionStore struct{ pool *pgxpool.Pool }

var _ AgentDefinitionStore = agentDefinitionStore{}

const agentDefinitionColumns = `id, agent_id, scope, model, model_definition_id, tool_set, max_turns, max_cost_usd, max_tool_iterations, required_role, tool_loading_mode, system_prompt, created_at, valid_from, valid_to`

func scanAgentDefinition(row pgx.Row) (*AgentDefinition, error) {
	var def AgentDefinition
	var toolSet json.RawMessage
	if err := row.Scan(
		&def.ID, &def.AgentID, &def.Scope, &def.Model, &def.ModelDefinitionID, &toolSet,
		&def.MaxTurns, &def.MaxCostUSD, &def.MaxToolIterations, &def.RequiredRole, &def.ToolLoadingMode, &def.SystemPrompt, &def.CreatedAt, &def.ValidFrom, &def.ValidTo,
	); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(toolSet, &def.ToolSet); err != nil {
		return nil, fmt.Errorf("unmarshal tool_set: %w", err)
	}
	return &def, nil
}

// GetCurrent returns nil (not an error) when agentID has no open row.
func (s agentDefinitionStore) GetCurrent(ctx context.Context, agentID string) (*AgentDefinition, error) {
	def, err := scanAgentDefinition(s.pool.QueryRow(ctx, `
		SELECT `+agentDefinitionColumns+`
		FROM agent_definition
		WHERE agent_id = $1 AND valid_to IS NULL
	`, agentID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get current agent definition: %w", err)
	}
	return def, nil
}

// ListScopes returns every distinct non-null scope value, sorted
// alphabetically -- see the interface doc comment above.
func (s agentDefinitionStore) ListScopes(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT scope FROM agent_definition WHERE scope IS NOT NULL ORDER BY scope
	`)
	if err != nil {
		return nil, fmt.Errorf("list agent definition scopes: %w", err)
	}
	defer rows.Close()

	var scopes []string
	for rows.Next() {
		var scope string
		if err := rows.Scan(&scope); err != nil {
			return nil, fmt.Errorf("scan agent definition scope: %w", err)
		}
		scopes = append(scopes, scope)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list agent definition scopes: %w", err)
	}
	return scopes, nil
}

// GetByID returns nil (not an error) when id does not exist.
func (s agentDefinitionStore) GetByID(ctx context.Context, id uuid.UUID) (*AgentDefinition, error) {
	def, err := scanAgentDefinition(s.pool.QueryRow(ctx, `
		SELECT `+agentDefinitionColumns+`
		FROM agent_definition
		WHERE id = $1
	`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get agent definition by id: %w", err)
	}
	return def, nil
}

// Upsert inserts def as agentID's new current row. It never rewrites an
// existing row in place; an already-open row for AgentID is a unique-index
// violation. Fills in ID, CreatedAt and ValidFrom.
//
// A zero-valued def.ToolLoadingMode is normalized to ToolLoadingModeBulk
// before writing: the column is NOT NULL, and this explicit-column
// INSERT bypasses the schema default that would otherwise apply.
func (s agentDefinitionStore) Upsert(ctx context.Context, def *AgentDefinition) error {
	toolSet, err := json.Marshal(def.ToolSet)
	if err != nil {
		return fmt.Errorf("marshal tool_set: %w", err)
	}

	toolLoadingMode := def.ToolLoadingMode
	if toolLoadingMode == "" {
		toolLoadingMode = ToolLoadingModeBulk
	}

	err = s.pool.QueryRow(ctx, `
		INSERT INTO agent_definition (agent_id, scope, model, model_definition_id, tool_set, max_turns, max_cost_usd, max_tool_iterations, required_role, tool_loading_mode, system_prompt)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING id, created_at, valid_from
	`, def.AgentID, def.Scope, def.Model, def.ModelDefinitionID, toolSet, def.MaxTurns, def.MaxCostUSD, def.MaxToolIterations, def.RequiredRole, toolLoadingMode, def.SystemPrompt).Scan(&def.ID, &def.CreatedAt, &def.ValidFrom)
	if err != nil {
		return fmt.Errorf("insert agent definition: %w", err)
	}
	def.ToolLoadingMode = toolLoadingMode
	return nil
}

// AssignToSession writes the SCD2 close-and-open pair in one transaction
// (AGENTS.md "SCD2"): closes sessionID's currently-open session_agent row
// (if any), then opens a new one pinned to agentDefinitionID. The partial
// unique index on (session_id) WHERE valid_to IS NULL (migration 001)
// rejects two open rows for the same session even if this ever races.
func (s agentDefinitionStore) AssignToSession(ctx context.Context, sessionID uuid.UUID, agentDefinitionID uuid.UUID) error {
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
		INSERT INTO session_agent (session_id, agent_id, agent_definition_id)
		SELECT $1, agent_id, id FROM agent_definition WHERE id = $2
	`, sessionID, agentDefinitionID); err != nil {
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
		SELECT session_id, agent_id, agent_definition_id, valid_from, valid_to
		FROM session_agent
		WHERE session_id = $1 AND valid_to IS NULL
	`, sessionID).Scan(&sa.SessionID, &sa.AgentID, &sa.AgentDefinitionID, &sa.ValidFrom, &sa.ValidTo)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get current session_agent assignment: %w", err)
	}
	return &sa, nil
}

// Querier is the QueryRow surface shared by *pgxpool.Pool and *pgx.Conn, so
// registration can run from either the store or the migrate job's
// database/sql connection.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Register implements AgentDefinitionStore.
func (s agentDefinitionStore) Register(ctx context.Context, def *AgentDefinition) (bool, error) {
	return RegisterAgentDefinition(ctx, s.pool, def)
}

// RegisterAgentDefinition inserts def only if its agent_id has no current
// row. A single statement guarded by idx_agent_definition_current, so racing
// callers leave one current row and none errors.
func RegisterAgentDefinition(ctx context.Context, q Querier, def *AgentDefinition) (bool, error) {
	toolSet, err := json.Marshal(def.ToolSet)
	if err != nil {
		return false, fmt.Errorf("marshal tool_set: %w", err)
	}
	toolLoadingMode := def.ToolLoadingMode
	if toolLoadingMode == "" {
		toolLoadingMode = ToolLoadingModeBulk
	}

	err = q.QueryRow(ctx, `
		INSERT INTO agent_definition (agent_id, scope, model, model_definition_id, tool_set, max_turns, max_cost_usd, max_tool_iterations, required_role, tool_loading_mode, system_prompt)
		SELECT $1::text, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11
		WHERE NOT EXISTS (SELECT 1 FROM agent_definition WHERE agent_id = $1::text AND valid_to IS NULL)
		ON CONFLICT (agent_id) WHERE valid_to IS NULL DO NOTHING
		RETURNING id, created_at, valid_from
	`, def.AgentID, def.Scope, def.Model, def.ModelDefinitionID, toolSet, def.MaxTurns, def.MaxCostUSD, def.MaxToolIterations, def.RequiredRole, string(toolLoadingMode), def.SystemPrompt).Scan(&def.ID, &def.CreatedAt, &def.ValidFrom)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("register agent definition: %w", err)
	}
	def.ToolLoadingMode = toolLoadingMode
	return true, nil
}
