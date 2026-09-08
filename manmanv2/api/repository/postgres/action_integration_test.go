//go:build integration

// Real-Postgres coverage for the FR6 edit round-trip (task #2091, plan
// #2080): ActionRepository.Update must persist option lists through a
// rename-only edit (the legacy red case: update silently wiped
// select/radio option lists) and apply deliberate option edits exactly.
// Same precedent as pending_restart_integration_test.go: build-tagged
// `integration`, self-contained DDL, run explicitly with Docker.
//
// Run it explicitly (requires a working Docker daemon):
//   bazel test //manmanv2/api/repository/postgres:action_integration_test --test_output=all
package postgres

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/libs/go/dbtest"
	manman "github.com/whale-net/everything/manmanv2/models"
)

// actionSchema is self-contained DDL for the action tables the repository
// touches, in their post-020 level-based shape (definition_level/entity_id,
// UNIQUE (definition_level, entity_id, name)), minus views/triggers the
// repository never reads.
const actionSchema = `
	CREATE OR REPLACE FUNCTION update_updated_at_column() RETURNS trigger AS $$
	BEGIN NEW.updated_at = NOW(); RETURN NEW; END;
	$$ LANGUAGE plpgsql;

	CREATE TABLE action_definitions (
		action_id BIGSERIAL PRIMARY KEY,
		definition_level VARCHAR(50) NOT NULL,
		entity_id BIGINT NOT NULL,
		name VARCHAR(100) NOT NULL,
		label VARCHAR(200) NOT NULL,
		description TEXT,
		command_template TEXT NOT NULL,
		display_order INT NOT NULL DEFAULT 0,
		group_name VARCHAR(100),
		button_style VARCHAR(50) DEFAULT 'primary',
		icon VARCHAR(100),
		requires_confirmation BOOLEAN DEFAULT false,
		confirmation_message TEXT,
		enabled BOOLEAN DEFAULT true,
		created_at TIMESTAMPTZ DEFAULT NOW(),
		updated_at TIMESTAMPTZ DEFAULT NOW(),
		UNIQUE (definition_level, entity_id, name),
		CHECK (name ~ '^[a-z0-9_]+$'),
		CHECK (definition_level IN ('game', 'game_config', 'server_game_config')),
		CHECK (button_style IN ('primary', 'secondary', 'success', 'danger', 'warning', 'info', 'light', 'dark'))
	);

	CREATE TABLE action_input_fields (
		field_id BIGSERIAL PRIMARY KEY,
		action_id BIGINT NOT NULL REFERENCES action_definitions(action_id) ON DELETE CASCADE,
		name VARCHAR(100) NOT NULL,
		label VARCHAR(200) NOT NULL,
		field_type VARCHAR(50) NOT NULL,
		required BOOLEAN DEFAULT false,
		placeholder TEXT,
		help_text TEXT,
		default_value TEXT,
		display_order INT NOT NULL DEFAULT 0,
		pattern VARCHAR(500),
		min_value NUMERIC,
		max_value NUMERIC,
		min_length INT,
		max_length INT,
		created_at TIMESTAMPTZ DEFAULT NOW(),
		updated_at TIMESTAMPTZ DEFAULT NOW(),
		UNIQUE(action_id, name),
		CHECK (name ~ '^[a-z0-9_]+$'),
		CHECK (field_type IN ('text', 'number', 'select', 'textarea', 'checkbox', 'radio', 'email', 'url'))
	);

	CREATE TABLE action_input_options (
		option_id BIGSERIAL PRIMARY KEY,
		field_id BIGINT NOT NULL REFERENCES action_input_fields(field_id) ON DELETE CASCADE,
		value TEXT NOT NULL,
		label VARCHAR(200) NOT NULL,
		display_order INT NOT NULL DEFAULT 0,
		is_default BOOLEAN DEFAULT false,
		created_at TIMESTAMPTZ DEFAULT NOW(),
		updated_at TIMESTAMPTZ DEFAULT NOW(),
		UNIQUE(field_id, value)
	);
`

func newActionTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	db := dbtest.NewPostgres(context.Background(), t, dbtest.Options{Schema: actionSchema})
	return db.Pool
}

func fieldByName(t *testing.T, fields []*ActionInputFieldWithOptions, name string) *ActionInputFieldWithOptions {
	t.Helper()
	for _, f := range fields {
		if f.Field.Name == name {
			return f
		}
	}
	t.Fatalf("field %q not found among %d fields", name, len(fields))
	return nil
}

// gameLevelAction is the shared shape: a two-field action -- one text field
// and one select field with an option list -- matching what the UI edit form
// round-trips and the seed scripts create (option.FieldID indexes the fields
// slice).
func gameLevelAction(label string) (*manman.ActionDefinition, []*manman.ActionInputField, []*manman.ActionInputOption) {
	action := &manman.ActionDefinition{
		DefinitionLevel: manman.ActionLevelGame,
		EntityID:        1,
		Name:            "change_map",
		Label:           label,
		CommandTemplate: "changelevel {{.map}}",
		ButtonStyle:     "primary",
		Enabled:         true,
	}
	fields := []*manman.ActionInputField{
		{Name: "reason", Label: "Reason", FieldType: "text", Required: false, DisplayOrder: 0},
		{Name: "map", Label: "Select Map", FieldType: "select", Required: true, DisplayOrder: 1},
	}
	options := []*manman.ActionInputOption{
		{FieldID: 1, Value: "de_dust2", Label: "Dust II", DisplayOrder: 0, IsDefault: true},
		{FieldID: 1, Value: "de_mirage", Label: "Mirage", DisplayOrder: 1},
		{FieldID: 1, Value: "de_inferno", Label: "Inferno", DisplayOrder: 2},
	}
	return action, fields, options
}

func optionValues(t *testing.T, fwo *ActionInputFieldWithOptions) []string {
	t.Helper()
	values := make([]string, 0, len(fwo.Options))
	for _, o := range fwo.Options {
		values = append(values, o.Value)
	}
	return values
}

// TestActionUpdate_OptionsSurviveUnrelatedEdit is the FR6 red/green: a
// rename-only edit through the update path (same fields, same options,
// referenced by field position) must leave the select field's option list
// intact -- not wiped, not duplicated into the sibling field.
func TestActionUpdate_OptionsSurviveUnrelatedEdit(t *testing.T) {
	pool := newActionTestDB(t)
	ctx := context.Background()
	repo := NewActionRepository(pool)

	action, fields, options := gameLevelAction("Change Map")
	actionID, err := repo.Create(ctx, action, fields, options)
	if err != nil {
		t.Fatalf("create action: %v", err)
	}

	got, gotFields, err := repo.Get(ctx, actionID)
	if err != nil {
		t.Fatalf("get action: %v", err)
	}
	if got.Label != "Change Map" {
		t.Errorf("unexpected label after create: %q", got.Label)
	}
	mapField := fieldByName(t, gotFields, "map")
	if vals := optionValues(t, mapField); len(vals) != 3 || vals[0] != "de_dust2" || vals[1] != "de_mirage" || vals[2] != "de_inferno" {
		t.Fatalf("expected 3 seeded options after create, got %v", vals)
	}
	reasonField := fieldByName(t, gotFields, "reason")
	if len(reasonField.Options) != 0 {
		t.Fatalf("text field must have no options after create, got %v", optionValues(t, reasonField))
	}

	// Rename only; fields and options round-trip unchanged (the UI edit
	// form submits them all back, options keyed by field index).
	got.Label = "Change Map (renamed)"
	if err := repo.Update(ctx, got, fields, options); err != nil {
		t.Fatalf("update action: %v", err)
	}

	_, gotFields, err = repo.Get(ctx, actionID)
	if err != nil {
		t.Fatalf("get action after update: %v", err)
	}
	mapField = fieldByName(t, gotFields, "map")
	if vals := optionValues(t, mapField); len(vals) != 3 || vals[0] != "de_dust2" || vals[1] != "de_mirage" || vals[2] != "de_inferno" {
		t.Errorf("options must survive a rename-only edit unchanged, got %v", vals)
	}
	reasonField = fieldByName(t, gotFields, "reason")
	if len(reasonField.Options) != 0 {
		t.Errorf("options must not leak into the sibling text field, got %v", optionValues(t, reasonField))
	}
}

// TestActionUpdate_DeliberateOptionEditPersists: editing an option list on
// purpose relabels/extends it with exactly the intended changes.
func TestActionUpdate_DeliberateOptionEditPersists(t *testing.T) {
	pool := newActionTestDB(t)
	ctx := context.Background()
	repo := NewActionRepository(pool)

	action, fields, options := gameLevelAction("Change Map")
	actionID, err := repo.Create(ctx, action, fields, options)
	if err != nil {
		t.Fatalf("create action: %v", err)
	}

	// Deliberate edit: relabel one option, drop another, add a new one.
	action.ActionID = actionID
	newOptions := []*manman.ActionInputOption{
		{FieldID: 1, Value: "de_dust2", Label: "Dust II (classic)", DisplayOrder: 0, IsDefault: true},
		{FieldID: 1, Value: "de_inferno", Label: "Inferno", DisplayOrder: 2},
		{FieldID: 1, Value: "de_ancient", Label: "Ancient", DisplayOrder: 3},
	}
	if err := repo.Update(ctx, action, fields, newOptions); err != nil {
		t.Fatalf("update action: %v", err)
	}

	_, gotFields, err := repo.Get(ctx, actionID)
	if err != nil {
		t.Fatalf("get action after update: %v", err)
	}
	mapField := fieldByName(t, gotFields, "map")
	vals := optionValues(t, mapField)
	if len(vals) != 3 || vals[0] != "de_dust2" || vals[1] != "de_inferno" || vals[2] != "de_ancient" {
		t.Fatalf("expected exactly the intended option set after deliberate edit, got %v", vals)
	}
	if mapField.Options[0].Label != "Dust II (classic)" {
		t.Errorf("expected relabeled option to persist, got %q", mapField.Options[0].Label)
	}
}

// TestActionCreate_SeedScriptShape: options sent without a field reference
// (FieldID 0, how seed_actions.sh/seed_*_actions.sh build their payloads)
// attach to the single field of a single-field action -- the parity case
// FR7 requires to keep seed re-runs idempotent.
func TestActionCreate_SeedScriptShape(t *testing.T) {
	pool := newActionTestDB(t)
	ctx := context.Background()
	repo := NewActionRepository(pool)

	action, fields, options := gameLevelAction("Seed Shape")
	fields = fields[1:] // single select field only
	action.Name = "seed_shape"
	options = []*manman.ActionInputOption{
		{Value: "de_dust2", Label: "Dust II", DisplayOrder: 0, IsDefault: true},
		{Value: "de_mirage", Label: "Mirage", DisplayOrder: 1},
	}

	actionID, err := repo.Create(ctx, action, fields, options)
	if err != nil {
		t.Fatalf("create action: %v", err)
	}

	_, gotFields, err := repo.Get(ctx, actionID)
	if err != nil {
		t.Fatalf("get action: %v", err)
	}
	if len(gotFields) != 1 {
		t.Fatalf("expected 1 field, got %d", len(gotFields))
	}
	if vals := optionValues(t, gotFields[0]); len(vals) != 2 {
		t.Errorf("expected both options attached to the single field, got %v", vals)
	}

	// Idempotent re-run: same payload again must not duplicate options.
	if _, err := repo.Create(ctx, action, fields, options); err != nil {
		t.Fatalf("idempotent re-create: %v", err)
	}
	_, gotFields, err = repo.Get(ctx, actionID)
	if err != nil {
		t.Fatalf("get action after re-create: %v", err)
	}
	if vals := optionValues(t, gotFields[0]); len(vals) != 2 {
		t.Errorf("seed re-run must remain idempotent, got %v", vals)
	}
}
