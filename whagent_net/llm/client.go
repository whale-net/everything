// Package llm is whagent-net's OpenRouter model client (issue #2112;
// ARCHITECTURE.md "Guardrails" and "Language and stack"; PRODUCT.md LB6):
// a single OpenAI-wire client pointed at OpenRouter's base URL, a model
// catalogue check (FR5, catalog.go), and per-turn cost accounting
// (FR7/LB6, pricing.go and cost.go). Multi-provider abstraction is a
// standing product non-goal (ARCHITECTURE.md "Open items" -- "one
// provider (OpenRouter) and a base-URL swap covers the foreseeable
// need"): there is exactly one Client type here, never a provider
// interface. Request.Provider (OpenRouter's own upstream-provider
// routing, session.ModelDefinition's `provider` column resolved by
// worker/activities.go) does not revisit that non-goal -- it restricts
// which of OpenRouter's upstream inference vendors may serve a call,
// OpenRouter itself remains the only LLM provider this package speaks
// to.
package llm

import (
	"context"
	"encoding/json"
	"fmt"

	openai "github.com/openai/openai-go/v2"
	"github.com/openai/openai-go/v2/option"
	"github.com/openai/openai-go/v2/packages/param"
	"github.com/openai/openai-go/v2/shared"
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
	// Provider restricts which of OpenRouter's upstream inference
	// providers may serve this call, per OpenRouter's provider-routing
	// API (https://openrouter.ai/docs/features/provider-routing). Nil
	// (the common case) leaves OpenRouter's default full-pool routing in
	// place. This is unrelated to the client.go package doc's "provider
	// abstraction" non-goal: OpenRouter itself remains the only LLM
	// provider whagent-net talks to -- Only names OpenRouter's own
	// upstream inference vendors (e.g. "Together", "Fireworks") for the
	// requested model, not a second LLM provider.
	Provider *ProviderPreferences
}

// ProviderPreferences is OpenRouter's per-request "provider" object
// (https://openrouter.ai/docs/features/provider-routing), carried through
// verbatim field-for-field. See Request's Provider doc comment for why
// this is not the multi-provider abstraction ARCHITECTURE.md's "Open
// items" lists as a non-goal. whagent_net/session.ProviderPreferences is
// the identical shape for `model_definition`'s stored routing
// preferences (session package doc comment on why it's not just this
// type reused); worker/activities.go converts one into the other when
// resolving an agent definition's effective model.
type ProviderPreferences struct {
	// Only restricts routing to exactly these OpenRouter provider slugs
	// (e.g. "together", "fireworks") instead of OpenRouter's default
	// behavior of routing across its entire pool for the requested
	// model.
	Only []string
	// Ignore excludes these provider slugs from the routing pool.
	Ignore []string
	// Order ranks providers to try, in order, before falling back to the
	// rest of the (possibly Only/Ignore-restricted) pool -- see
	// AllowFallbacks to disable that fallback entirely.
	Order []string
	// Quantizations restricts routing to providers serving one of these
	// quantization levels (e.g. "fp8", "int4").
	Quantizations []string
	// Sort is OpenRouter's routing sort strategy: "price", "throughput",
	// or "latency". Empty leaves OpenRouter's default.
	Sort string
	// AllowFallbacks, when non-nil, overrides OpenRouter's default of
	// true: false means fail the call rather than fall back outside
	// Order/Only once those providers are unavailable.
	AllowFallbacks *bool
	// RequireParameters, when non-nil true, restricts routing to
	// providers that support every parameter this request sets.
	RequireParameters *bool
	// DataCollection is "allow" or "deny", controlling whether OpenRouter
	// may route to providers that may log/train on the request. Empty
	// leaves OpenRouter's default.
	DataCollection string
}

// toWire converts p into the JSON object OpenRouter's "provider" request
// field expects, omitting every unset field so an empty ProviderPreferences
// produces an empty (and thus omitted, see Complete) object rather than a
// wire payload full of empty arrays/strings.
func (p *ProviderPreferences) toWire() map[string]any {
	out := make(map[string]any, 8)
	if len(p.Only) > 0 {
		out["only"] = p.Only
	}
	if len(p.Ignore) > 0 {
		out["ignore"] = p.Ignore
	}
	if len(p.Order) > 0 {
		out["order"] = p.Order
	}
	if len(p.Quantizations) > 0 {
		out["quantizations"] = p.Quantizations
	}
	if p.Sort != "" {
		out["sort"] = p.Sort
	}
	if p.AllowFallbacks != nil {
		out["allow_fallbacks"] = *p.AllowFallbacks
	}
	if p.RequireParameters != nil {
		out["require_parameters"] = *p.RequireParameters
	}
	if p.DataCollection != "" {
		out["data_collection"] = p.DataCollection
	}
	return out
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
// Extra opts are appended after the api-key/base-URL options, so a
// caller (in particular a test) can override the underlying HTTP
// transport via option.WithHTTPClient without a live OpenRouter call.
func NewClient(apiKey, baseURL string, opts ...option.RequestOption) *Client {
	all := append([]option.RequestOption{
		option.WithAPIKey(apiKey),
		option.WithBaseURL(baseURL),
	}, opts...)
	return &Client{oa: openai.NewClient(all...)}
}

// Complete makes one chat-completion call. It always requests provider
// usage reporting (usage.include=true on OpenRouter) per LB6 -- the
// worker must ask for it on every call, not opportunistically -- and
// returns the model message, any tool calls, and the usage block
// (including, when the provider supplies them, its own reported cost and
// generation id).
func (c *Client) Complete(ctx context.Context, req Request) (Response, error) {
	params := openai.ChatCompletionNewParams{
		Model:    shared.ChatModel(req.Model),
		Messages: toWireMessages(req.Messages),
		Tools:    toWireTools(req.Tools),
	}

	// usage.include=true unconditionally, on every call (LB6/FR7): the
	// worker asks for provider usage/cost reporting opportunistically
	// never suffices, so this is not gated on anything caller-supplied.
	opts := []option.RequestOption{option.WithJSONSet("usage.include", true)}
	if req.Provider != nil {
		if wire := req.Provider.toWire(); len(wire) > 0 {
			opts = append(opts, option.WithJSONSet("provider", wire))
		}
	}

	resp, err := c.oa.Chat.Completions.New(ctx, params, opts...)
	if err != nil {
		return Response{}, fmt.Errorf("llm: chat completion: %w", err)
	}
	if len(resp.Choices) == 0 {
		return Response{}, fmt.Errorf("llm: chat completion: response had no choices")
	}

	choice := resp.Choices[0]
	message, toolCalls := fromWireMessage(choice.Message)

	return Response{
		Message:   message,
		ToolCalls: toolCalls,
		Usage:     fromWireUsage(resp.ID, resp.Usage),
	}, nil
}

// toWireMessages converts the assembled context (Message, in this
// package's provider-agnostic shape) into the OpenAI-wire message params
// Complete sends.
func toWireMessages(messages []Message) []openai.ChatCompletionMessageParamUnion {
	out := make([]openai.ChatCompletionMessageParamUnion, 0, len(messages))
	for _, m := range messages {
		switch m.Role {
		case RoleSystem:
			out = append(out, openai.SystemMessage(m.Content))
		case RoleUser:
			out = append(out, openai.UserMessage(m.Content))
		case RoleTool:
			out = append(out, openai.ToolMessage(m.Content, m.ToolCallID))
		case RoleAssistant:
			out = append(out, assistantMessage(m))
		default:
			// Unknown roles are sent as-is via the user role rather than
			// silently dropped -- the worker's context build is the only
			// producer of Role today and always uses one of the four
			// constants above, but a dropped message would be a silent
			// context-loss bug if that ever changed.
			out = append(out, openai.UserMessage(m.Content))
		}
	}
	return out
}

// assistantMessage builds the assistant-message param for m, including
// any tool calls it requested -- openai.AssistantMessage's helper does
// not accept tool calls, so this constructs the param struct directly.
func assistantMessage(m Message) openai.ChatCompletionMessageParamUnion {
	asst := openai.ChatCompletionAssistantMessageParam{}
	if m.Content != "" {
		asst.Content = openai.ChatCompletionAssistantMessageParamContentUnion{
			OfString: param.NewOpt(m.Content),
		}
	}
	if len(m.ToolCalls) > 0 {
		calls := make([]openai.ChatCompletionMessageToolCallUnionParam, 0, len(m.ToolCalls))
		for _, tc := range m.ToolCalls {
			calls = append(calls, openai.ChatCompletionMessageToolCallUnionParam{
				OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
					ID:   tc.ID,
					Type: "function",
					Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
						Name:      tc.Name,
						Arguments: tc.Arguments,
					},
				},
			})
		}
		asst.ToolCalls = calls
	}
	return openai.ChatCompletionMessageParamUnion{OfAssistant: &asst}
}

// toWireTools converts the agent definition's allowed tool set into the
// OpenAI-wire function-tool params Complete sends.
func toWireTools(tools []ToolDefinition) []openai.ChatCompletionToolUnionParam {
	if len(tools) == 0 {
		return nil
	}
	out := make([]openai.ChatCompletionToolUnionParam, 0, len(tools))
	for _, t := range tools {
		fn := shared.FunctionDefinitionParam{
			Name:       t.Name,
			Parameters: shared.FunctionParameters(t.Parameters),
		}
		if t.Description != "" {
			fn.Description = param.NewOpt(t.Description)
		}
		out = append(out, openai.ChatCompletionFunctionTool(fn))
	}
	return out
}

// fromWireMessage converts a provider response's message into this
// package's Message plus its tool calls. Arguments is passed through
// verbatim -- the worker's tool dispatch activity decodes it, this
// package does not.
func fromWireMessage(msg openai.ChatCompletionMessage) (Message, []ToolCall) {
	toolCalls := make([]ToolCall, 0, len(msg.ToolCalls))
	for _, tc := range msg.ToolCalls {
		toolCalls = append(toolCalls, ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
		})
	}
	return Message{
		Role:      RoleAssistant,
		Content:   msg.Content,
		ToolCalls: toolCalls,
	}, toolCalls
}

// fromWireUsage converts a provider response's id and usage block into a
// UsageReport. generationID is the provider's response id -- OpenRouter
// returns its generation id as the top-level chat-completion "id" (LB6:
// recorded even though M1 has no reader of it). ProviderCostUSD is
// populated from usage's "cost" extra field -- OpenRouter's
// usage.include=true extension to the OpenAI-compatible usage block,
// which the openai-go response types have no typed field for -- and left
// nil when the provider omitted it, which ResolveCost (cost.go) treats
// as "must estimate", never as "free".
func fromWireUsage(generationID string, usage openai.CompletionUsage) UsageReport {
	report := UsageReport{
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		GenerationID:     generationID,
	}
	// Deliberately does not gate on field.Valid(): openai-go's apijson
	// decoder only marks an extra field "valid" when the containing
	// struct declares a typed extra-value map to decode into.
	// openai.CompletionUsage has none -- it only carries the untyped
	// JSON.ExtraFields metadata map -- so every extra field it sees,
	// including OpenRouter's "cost", is unconditionally decoded with
	// status "invalid" even though the raw JSON value is present and
	// well-formed. field.Valid() is therefore always false here and
	// cannot be used to detect presence; map membership (ok) plus a
	// check that the raw value isn't the JSON literal "null" (an
	// explicit null, as opposed to an omitted field) is what actually
	// distinguishes "provider supplied a cost" from "it didn't".
	if field, ok := usage.JSON.ExtraFields["cost"]; ok && field.Raw() != "" && field.Raw() != "null" {
		var costUSD float64
		if err := json.Unmarshal([]byte(field.Raw()), &costUSD); err == nil {
			cost := usdToMicros(costUSD)
			report.ProviderCostUSD = &cost
		}
	}
	return report
}
