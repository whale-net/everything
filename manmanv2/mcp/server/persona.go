// Package server holds the manmanv2 MCP server's authentication, persona
// authorization, tool registry, and audit pipeline.
package server

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Persona is the effective authorization level of an MCP caller.
type Persona int

const (
	PersonaNone Persona = iota
	PersonaGamer
	PersonaServerManager
	PersonaAdmin
)

func (p Persona) String() string {
	switch p {
	case PersonaGamer:
		return "gamer"
	case PersonaServerManager:
		return "server-manager"
	case PersonaAdmin:
		return "admin"
	default:
		return "none"
	}
}

// Errors returned for refused calls. Neither triggers a backend call.
var (
	ErrUnauthenticated  = errors.New("mcp: unauthenticated: no verified caller credential")
	ErrPermissionDenied = errors.New("mcp: permission denied")
	ErrUnknownTool      = errors.New("mcp: unknown tool")
)

// ResolvePersona maps realm_access.roles to one effective persona
// (admin > server-manager > gamer; otherwise PersonaNone). It is the single
// place persona is decided so delegated-grant narrowing can slot in later.
func ResolvePersona(roles []string) Persona {
	p := PersonaNone
	for _, r := range roles {
		var rp Persona
		switch r {
		case "admin":
			rp = PersonaAdmin
		case "server-manager":
			rp = PersonaServerManager
		case "gamer":
			rp = PersonaGamer
		}
		if rp > p {
			p = rp
		}
	}
	return p
}

// Tool declares one MCP tool and the minimum persona allowed to call it.
type Tool struct {
	Name       string
	MinPersona Persona
	// TargetArg names the JSON argument holding the call's target id, for
	// audit. Empty means the tool has no single target.
	TargetArg string
	// Snapshot, if set, captures pre-call state recorded in the audit entry
	// of an allowed call (edit tools use it for a before-image). A failure
	// is logged but does not block the call.
	Snapshot func(ctx CallContext, args json.RawMessage) (any, error)
}

// Registry is the single declaration of tools and their minimum personas.
type Registry struct {
	tools  []Tool
	byName map[string]Tool
}

// NewRegistry returns a registry over tools. It panics on a duplicate name.
func NewRegistry(tools ...Tool) *Registry {
	r := &Registry{tools: tools, byName: make(map[string]Tool, len(tools))}
	for _, t := range tools {
		if _, dup := r.byName[t.Name]; dup {
			panic(fmt.Sprintf("mcp: duplicate tool %q", t.Name))
		}
		r.byName[t.Name] = t
	}
	return r
}

// Lookup returns the named tool.
func (r *Registry) Lookup(name string) (Tool, bool) {
	t, ok := r.byName[name]
	return t, ok
}

// Visible returns the tools p may call (the tools/list view): exactly those
// Authorize would not refuse for persona reasons.
func (r *Registry) Visible(p Persona) []Tool {
	var out []Tool
	for _, t := range r.tools {
		if r.Authorize(p, t.Name) == nil {
			out = append(out, t)
		}
	}
	return out
}

// Authorize reports whether p may call the named tool.
func (r *Registry) Authorize(p Persona, tool string) error {
	t, ok := r.byName[tool]
	if !ok {
		return ErrUnknownTool
	}
	if p == PersonaNone || p < t.MinPersona {
		return ErrPermissionDenied
	}
	return nil
}
