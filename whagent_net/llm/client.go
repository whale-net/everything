// Package llm is whagent-net's OpenRouter model client (issue #2112;
// ARCHITECTURE.md "Guardrails" and "Language and stack"; PRODUCT.md LB6):
// a single OpenAI-wire client pointed at OpenRouter's base URL, a model
// catalogue check (FR5, catalog.go), and per-turn cost accounting
// (FR7/LB6, pricing.go and cost.go). Multi-provider abstraction is a
// standing product non-goal (ARCHITECTURE.md "Open items" -- "one
// provider (OpenRouter) and a base-URL swap covers the foreseeable
// need"): there is exactly one Client type here, never a provider
// interface.
//
// Scaffold phase (#2112): types and signatures are final per the issue's
// Implementation-phase column contract; every method body in this
// package is a stub (errNotImplemented) that compiles but does no real
// work. Real HTTP calls and response parsing land in the Implementation
// phase.
package llm

import (
	"context"
	"errors"

	openai "github.com/openai/openai-go/v2"
	"github.com/openai/openai-go/v2/option"
)

// Role is a chat message's role, mirroring the OpenAI wire protocol that
// OpenRouter also speaks.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is one context message, as assembled by the worker's per-turn
// context build (ARCHITECTURE.md "Three nouns: session, transcript,
// context"). ToolCallID binds a RoleTool message to the ToolCall that
// produced it; ToolCalls is set on a RoleAssistant message that requested
// tool calls.
type Message struct {
	Role       Role
	Content    string
	ToolCallID string
	ToolCalls  []ToolCall
}

// ToolDefinition is one tool the model may call, in the shape an agent
// definition's allowed tool set produces (ARCHITECTURE.md "Domain-owned
// MCP servers and the tool contract"). Parameters is the tool's JSON
// Schema.
type ToolDefinition struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// ToolCall is one tool invocation the model requested. Arguments is the
// raw JSON arguments blob, exactly as the model returned it -- the
// worker's tool dispatch activity (ARCHITECTURE.md "Session workflow")
// decodes it, this package does not.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// Request is the assembled context for one turn's LLM call.
type Request struct {
	Model    string
	Messages []Message
	Tools    []ToolDefinition
}

// Response is one turn's LLM call result: the model's message, any tool
// calls it requested, and the raw usage block ResolveCost (cost.go) turns
// into a committed TurnUsage row (LB6).
type Response struct {
	Message   Message
	ToolCalls []ToolCall
	Usage     UsageReport
}

// Client wraps the OpenAI-wire client against OpenRouter's base URL. One
// Client per process is the expected shape -- see the package doc.
type Client struct {
	oa openai.Client
}

// NewClient builds a Client against baseURL (OpenRouter's
// OpenAI-compatible endpoint -- see whagent_net/ENV.md's
// OPENROUTER_BASE_URL), authenticating with apiKey (OPENROUTER_API_KEY).
func NewClient(apiKey, baseURL string) *Client {
	return &Client{
		oa: openai.NewClient(
			option.WithAPIKey(apiKey),
			option.WithBaseURL(baseURL),
		),
	}
}

// Complete makes one chat-completion call. It always requests provider
// usage reporting (usage.include=true on OpenRouter) per LB6 -- the
// worker must ask for it on every call, not opportunistically -- and
// returns the model message, any tool calls, and the usage block
// (including, when the provider supplies them, its own reported cost and
// generation id).
func (c *Client) Complete(ctx context.Context, req Request) (Response, error) {
	return Response{}, errNotImplemented
}

// errNotImplemented is returned by every scaffold-phase stub method in
// this package (whagent_net/llm, issue #2112). Real logic lands in the
// Implementation phase.
var errNotImplemented = errors.New("not implemented: whagent_net/llm scaffold phase (#2112)")
