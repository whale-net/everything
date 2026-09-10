// Package config is whagent-net's checked-in agent-definition seed
// source (issue #2121, LB5/NFR6; ARCHITECTURE.md "Domain-owned MCP
// servers and the tool contract"): agents.yaml (embedded below, so the
// binary carries its own seed data rather than reading a mounted path at
// runtime -- mirrors firmware/sensor/catalog's chips.yaml precedent) plus
// the Go shape Load decodes it into. whagent_net/migrate/seed consumes
// this package's Load to build the migrate.Seeder libs/go/migrate.
// WithSeeder registers (whagent_net/migrate/main.go).
//
// # Scaffold status (this task)
//
// Load and Validate below are real, not stubs: parsing/shape-checking a
// config file is pure and I/O-free, the same reasoning
// whagent_net/worker/caps.go's checkCaps documents for why a pure
// function ships whole in Scaffold even while the I/O-touching seeder
// this package feeds (whagent_net/migrate/seed.Seeder) stays a stub until
// Implementation. Validate's model-catalogue check (an unserved model
// must fail the seeder loudly per this task's Testing section) is
// deliberately NOT done here -- checking a model against
// llm.Catalog.Supports is a network call, so that half of validation
// belongs in the seeder (an I/O step), not this pure package.
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
// ID/CreatedAt, which the seeder derives (Upsert resolves ID by Name; see
// whagent_net/migrate/seed's package doc comment). A named, reusable
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
// the seeder derives (see agents.yaml's own doc comment and
// whagent_net/migrate/seed's package doc comment for the version-diff
// rule).
//
// Domain is required (Validate rejects a missing or empty value, issue
// #2424 FR1): the one domain this agent definition belongs to. Every
// tool_set entry below is understood to belong to that same domain, by
// construction -- ToolServerRefConfig carries no domain field of its own,
// and there is no "spans more than one domain" rule to enforce, because
// there is only ever one Domain per definition to begin with. Domain is
// the sole input whagent_net/grantkey.ForDomain may derive a
// delegated-grant key from (FR4).
//
// Exactly one of Model and ModelDefinition is set (Validate enforces
// this): Model names an OpenRouter model id directly, with OpenRouter's
// default full-pool routing; ModelDefinition instead names a
// model_definitions entry above by its Name, and is preferred when set --
// the effective model and provider-routing preferences come from that
// entry, not from Model (which stays empty in that case).
type AgentDefinitionConfig struct {
	AgentID         string                `yaml:"agent_id"`
	Domain          string                `yaml:"domain"`
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
// itself, before whagent_net/migrate/seed.Seeder ever opens a database
// transaction (this task's Testing section: "fails the seeder loudly
// rather than writing a half-row").
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
// comment -- and is whagent_net/migrate/seed.Seeder's job (Implementation
// phase).
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

		// FR1 (issue #2424): domain is required on every entry -- it is
		// the sole input whagent_net/grantkey.ForDomain may derive a
		// delegated-grant key from, so an unset domain must fail here,
		// as a config error, rather than surface later as a seeded row
		// with no usable grant key.
		if a.Domain == "" {
			return fmt.Errorf("agent %q: domain is required", a.AgentID)
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
