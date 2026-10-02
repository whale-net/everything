// Package server holds the manmanv2 MCP server's authentication, persona
// authorization, tool registry, and audit pipeline.
package server

import "errors"

// Persona is the effective authorization level of an MCP caller.
type Persona int

const (
	PersonaNone Persona = iota
	PersonaGamer
	PersonaServerManager
	PersonaAdmin
)

// ErrNotImplemented marks scaffold stubs filled in by the Implementation lane.
var ErrNotImplemented = errors.New("mcp: not implemented")

// ResolvePersona maps realm_access.roles to one effective persona
// (admin > server-manager > gamer; otherwise PersonaNone). It is the single
// place persona is decided so delegated-grant narrowing can slot in later.
func ResolvePersona(roles []string) Persona {
	return PersonaNone
}

// Tool declares one MCP tool and the minimum persona allowed to call it.
type Tool struct {
	Name       string
	MinPersona Persona
}

// Registry is the single declaration of tools and their minimum personas.
type Registry struct {
	tools []Tool
}

// NewRegistry returns a registry over tools.
func NewRegistry(tools ...Tool) *Registry { return &Registry{tools: tools} }

// Visible returns the tools p may call (the tools/list view).
func (r *Registry) Visible(p Persona) []Tool { return nil }

// Authorize reports whether p may call the named tool.
func (r *Registry) Authorize(p Persona, tool string) error { return ErrNotImplemented }

// AuditRecord is one structured per-call audit entry.
type AuditRecord struct {
	Subject  string
	Persona  Persona
	Tool     string
	TargetID string
	Outcome  string
}
