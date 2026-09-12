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

// ProviderPreferences is a `model_definition` row's `provider` JSONB
// column: OpenRouter's own provider-routing request object
// (https://openrouter.ai/docs/features/provider-routing), carried
// through whagent-net's storage layer. whagent_net/llm.ProviderPreferences
// is the identical shape for the wire client -- duplicated rather than
// shared because this package must stay import-free of llm
// (ARCHITECTURE.md "Service boundary vs. package boundary": llm is a
// worker-side wire client, session is the shared storage layer both api
// and worker import); worker/activities.go converts between the two at
// the one call site that needs both.
type ProviderPreferences struct {
	Only              []string `json:"only,omitempty"`
	Ignore            []string `json:"ignore,omitempty"`
	Order             []string `json:"order,omitempty"`
	Quantizations     []string `json:"quantizations,omitempty"`
	Sort              string   `json:"sort,omitempty"`
	AllowFallbacks    *bool    `json:"allow_fallbacks,omitempty"`
	RequireParameters *bool    `json:"require_parameters,omitempty"`
	DataCollection    string   `json:"data_collection,omitempty"`
}

// ModelDefinition is a `model_definition` row: a named, reusable model id
// plus OpenRouter provider-routing preferences an agent_definition may
// reference (AgentDefinition.ModelDefinitionID) instead of duplicating
// routing config per agent.
type ModelDefinition struct {
	ID        uuid.UUID
	Name      string
	Model     string
	Provider  ProviderPreferences
	CreatedAt time.Time
}

// ModelDefinitionStore is the `model_definition` table's repository
// interface. Unlike AgentDefinitionStore, model_definition is NOT
// versioned/SCD2 -- it is a plain named lookup table, upserted by Name.
//
// A model_definition already referenced by a seeded agent_definition row
// that needs different routing preferences should get a NEW name (and a
// new agent_definition version pointing at it), never an in-place edit of
// an existing name's row -- editing one retroactively changes routing for
// every agent_definition version that already references it, breaking
// the same "a session sees exactly what it was assigned" guarantee
// agent_definition's own versioning exists for (agentdef.go's doc
// comment). Upsert does not enforce this; it is a seeding/authoring
// discipline documented here, not a database constraint.
type ModelDefinitionStore interface {
	// GetByID returns nil (not an error) when id has no row.
	GetByID(ctx context.Context, id uuid.UUID) (*ModelDefinition, error)
	// GetByName returns nil (not an error) when name has no row.
	GetByName(ctx context.Context, name string) (*ModelDefinition, error)
	// Upsert inserts def or, on a Name conflict, replaces model/provider
	// in place -- see this type's doc comment for why a config author
	// should prefer a new Name over relying on this replace behavior.
	Upsert(ctx context.Context, def *ModelDefinition) error
}

// modelDefinitionStore is the Postgres-backed ModelDefinitionStore
// implementation.
type modelDefinitionStore struct{ pool *pgxpool.Pool }

var _ ModelDefinitionStore = modelDefinitionStore{}

const modelDefinitionColumns = `id, name, model, provider, created_at`

func scanModelDefinition(row pgx.Row) (*ModelDefinition, error) {
	var def ModelDefinition
	var provider json.RawMessage
	if err := row.Scan(&def.ID, &def.Name, &def.Model, &provider, &def.CreatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(provider, &def.Provider); err != nil {
		return nil, fmt.Errorf("unmarshal provider: %w", err)
	}
	return &def, nil
}

func (s modelDefinitionStore) GetByID(ctx context.Context, id uuid.UUID) (*ModelDefinition, error) {
	def, err := scanModelDefinition(s.pool.QueryRow(ctx, `
		SELECT `+modelDefinitionColumns+`
		FROM model_definition
		WHERE id = $1
	`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get model definition by id: %w", err)
	}
	return def, nil
}

func (s modelDefinitionStore) GetByName(ctx context.Context, name string) (*ModelDefinition, error) {
	def, err := scanModelDefinition(s.pool.QueryRow(ctx, `
		SELECT `+modelDefinitionColumns+`
		FROM model_definition
		WHERE name = $1
	`, name))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get model definition by name: %w", err)
	}
	return def, nil
}

func (s modelDefinitionStore) Upsert(ctx context.Context, def *ModelDefinition) error {
	provider, err := json.Marshal(def.Provider)
	if err != nil {
		return fmt.Errorf("marshal provider: %w", err)
	}

	err = s.pool.QueryRow(ctx, `
		INSERT INTO model_definition (name, model, provider)
		VALUES ($1, $2, $3)
		ON CONFLICT (name) DO UPDATE SET
			model = EXCLUDED.model,
			provider = EXCLUDED.provider
		RETURNING id, created_at
	`, def.Name, def.Model, provider).Scan(&def.ID, &def.CreatedAt)
	if err != nil {
		return fmt.Errorf("upsert model definition: %w", err)
	}
	return nil
}
