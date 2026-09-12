// Package config documents whagent-net's agent-definition row shape
// (issue #2121, LB5/NFR6; ARCHITECTURE.md "Domain-owned MCP servers and
// the tool contract"): agents.yaml (embedded below, so the binary carries
// its own copy rather than reading a mounted path at runtime -- mirrors
// firmware/sensor/catalog's chips.yaml precedent) plus the Go shape Load
// decodes it into. There is no automatic seeder consuming Load into the
// database (see whagent_net/README.md "Agent definition config" for the
// manual insert example) -- whagent_net/api/main.go's own use of Load is
// the only production caller today, for RequiredRoles/DevRoles below.
//
// Load and Validate check the config's shape only, no I/O: the
// model-catalogue check (an unserved model, checked against
// llm.Catalog.Supports, a network call) is deliberately not done here --
// there is currently no caller that performs it.
package config

import (
	_ "embed"
	"fmt"

	"gopkg.in/yaml.v3"
)

//go:embed agents.yaml
var agentsYAML []byte

// ToolServerRefConfig is agents.yaml's tool_set entry shape -- decodes
// 1:1 into whagent_net/session.ToolServerRef.
type ToolServerRefConfig struct {
	ServerURL    string   `yaml:"server_url"`
	AllowedTools []string `yaml:"allowed_tools"`
}

// ProviderPreferencesConfig is a model_definitions entry's `provider`
// block -- decodes 1:1 into whagent_net/session.ProviderPreferences
// (OpenRouter's own provider-routing object shape,
// https://openrouter.ai/docs/features/provider-routing).
type ProviderPreferencesConfig struct {
	Only              []string `yaml:"only"`
	Ignore            []string `yaml:"ignore"`
	Order             []string `yaml:"order"`
	Quantizations     []string `yaml:"quantizations"`
	Sort              string   `yaml:"sort"`
	AllowFallbacks    *bool    `yaml:"allow_fallbacks"`
	RequireParameters *bool    `yaml:"require_parameters"`
	DataCollection    string   `yaml:"data_collection"`
}

// ModelDefinitionConfig is agents.yaml's top-level model_definitions entry
// shape -- decodes into whagent_net/session.ModelDefinition minus
// ID/CreatedAt, which whoever inserts the row assigns (see
// whagent_net/README.md "Agent definition config"). A named, reusable
// model + provider-routing bundle an AgentDefinitionConfig can reference
// by name (AgentDefinitionConfig.ModelDefinition) instead of naming a
// model directly, so more than one agent can share identical routing
// preferences without repeating them.
type ModelDefinitionConfig struct {
	Name     string                    `yaml:"name"`
	Model    string                    `yaml:"model"`
	Provider ProviderPreferencesConfig `yaml:"provider"`
}

// AgentDefinitionConfig is agents.yaml's per-agent entry shape -- decodes
// into whagent_net/session.AgentDefinition minus Version/CreatedAt, which
// whoever inserts the row assigns (see whagent_net/README.md "Agent
// definition config" for the version-diff rule a manual insert must
// preserve).
//
// Scope is optional: the one grant-scope this agent definition belongs
// to, when set. Every tool_set entry below is understood to belong to
// that same scope, by construction -- ToolServerRefConfig carries no
// scope field of its own, and there is no "spans more than one scope"
// rule to enforce, because there is only ever one Scope per definition to
// begin with. Scope is the sole input whagent_net/grantkey.ForScope may
// derive a delegated-grant key from (FR4). Left unset, the agent
// definition carries no delegated-grant scoping at all -- it still runs
// with whatever ToolSet is configured below, just without a cross-domain
// grant key derived or checked.
//
// Exactly one of Model and ModelDefinition is set (Validate enforces
// this): Model names an OpenRouter model id directly, with OpenRouter's
// default full-pool routing; ModelDefinition instead names a
// model_definitions entry above by its Name, and is preferred when set --
// the effective model and provider-routing preferences come from that
// entry, not from Model (which stays empty in that case).
type AgentDefinitionConfig struct {
	AgentID         string                `yaml:"agent_id"`
	Scope           *string               `yaml:"scope,omitempty"`
	Model           string                `yaml:"model"`
	ModelDefinition string                `yaml:"model_definition"`
	ToolSet         []ToolServerRefConfig `yaml:"tool_set"`
	MaxTurns        int                   `yaml:"max_turns"`
	MaxCostUSD      float64               `yaml:"max_cost_usd"`
	RequiredRole    string                `yaml:"required_role"`
}

// document is agents.yaml's top-level shape.
type document struct {
	ModelDefinitions []ModelDefinitionConfig `yaml:"model_definitions"`
	Agents           []AgentDefinitionConfig `yaml:"agents"`
}

// Load parses the embedded agents.yaml and validates every entry (see
// Validate) before returning -- a malformed config file fails Load
// itself, before whagent_net/api/main.go's config.Load call (the sole
// production caller today) can derive DevRoles from it.
func Load() ([]ModelDefinitionConfig, []AgentDefinitionConfig, error) {
	var doc document
	if err := yaml.Unmarshal(agentsYAML, &doc); err != nil {
		return nil, nil, fmt.Errorf("config: parse agents.yaml: %w", err)
	}
	if err := Validate(doc.ModelDefinitions, doc.Agents); err != nil {
		return nil, nil, fmt.Errorf("config: agents.yaml: %w", err)
	}
	return doc.ModelDefinitions, doc.Agents, nil
}

// RequiredRoles returns the deduplicated, non-empty RequiredRole values
// across agents, in stable (first-seen) order. whagent_net/api/main.go
// uses this to derive grpcauth.ServerConfig.DevRoles from the seeded
// agent definitions instead of hand-listing service roles (issue #2154,
// FR9) -- so a new agent definition with a new required_role stays
// coverable under GRPC_AUTH_MODE=none without a main.go change. Pure, no
// I/O, same shape as Validate.
func RequiredRoles(agents []AgentDefinitionConfig) []string {
	seen := make(map[string]struct{}, len(agents))
	roles := make([]string, 0, len(agents))
	for _, a := range agents {
		if a.RequiredRole == "" {
			continue
		}
		if _, dup := seen[a.RequiredRole]; dup {
			continue
		}
		seen[a.RequiredRole] = struct{}{}
		roles = append(roles, a.RequiredRole)
	}
	return roles
}

// Validate checks the shape-only rules Load can enforce without I/O: a
// missing agent_id, a missing/ambiguous model reference, a malformed
// model_definitions entry, or a tool_set entry with an empty server_url.
// The model-catalogue check (a model the configured OpenRouter provider
// does not serve) is deliberately not here -- see this package's doc
// comment.
func Validate(modelDefs []ModelDefinitionConfig, agents []AgentDefinitionConfig) error {
	modelDefNames := make(map[string]struct{}, len(modelDefs))
	for i, md := range modelDefs {
		if md.Name == "" {
			return fmt.Errorf("model_definitions[%d]: name is required", i)
		}
		if _, dup := modelDefNames[md.Name]; dup {
			return fmt.Errorf("model_definitions[%d]: duplicate name %q", i, md.Name)
		}
		modelDefNames[md.Name] = struct{}{}
		if md.Model == "" {
			return fmt.Errorf("model_definitions[%d] %q: model is required", i, md.Name)
		}
	}

	seen := make(map[string]struct{}, len(agents))
	for i, a := range agents {
		if a.AgentID == "" {
			return fmt.Errorf("agents[%d]: agent_id is required", i)
		}
		if _, dup := seen[a.AgentID]; dup {
			return fmt.Errorf("agents[%d]: duplicate agent_id %q", i, a.AgentID)
		}
		seen[a.AgentID] = struct{}{}

		// Scope is optional, but when present it must not be blank --
		// it is the sole input whagent_net/grantkey.ForScope may derive
		// a delegated-grant key from, so a set-but-empty scope must fail
		// here, as a config error, rather than surface later as a
		// seeded row with no usable grant key.
		if a.Scope != nil && *a.Scope == "" {
			return fmt.Errorf("agent %q: scope, if set, must not be empty", a.AgentID)
		}

		// Exactly one of model / model_definition -- see
		// AgentDefinitionConfig's doc comment.
		switch {
		case a.Model == "" && a.ModelDefinition == "":
			return fmt.Errorf("agent %q: exactly one of model or model_definition is required", a.AgentID)
		case a.Model != "" && a.ModelDefinition != "":
			return fmt.Errorf("agent %q: model and model_definition are mutually exclusive", a.AgentID)
		case a.ModelDefinition != "":
			if _, ok := modelDefNames[a.ModelDefinition]; !ok {
				return fmt.Errorf("agent %q: model_definition %q is not defined in model_definitions", a.AgentID, a.ModelDefinition)
			}
		}

		if len(a.ToolSet) == 0 {
			return fmt.Errorf("agent %q: tool_set must have at least one entry", a.AgentID)
		}
		for j, ref := range a.ToolSet {
			if ref.ServerURL == "" {
				return fmt.Errorf("agent %q: tool_set[%d]: server_url is required", a.AgentID, j)
			}
		}
	}
	return nil
}
