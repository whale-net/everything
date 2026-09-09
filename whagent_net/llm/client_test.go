package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/openai/openai-go/v2/option"
)

// stubTransport is an http.RoundTripper stub -- no live OpenRouter call in
// any test in this package (issue #2112 Testing section). It captures the
// last request it served (including its decoded JSON body, for assertions
// on what Complete/Catalog sent) and returns a canned response.
type stubTransport struct {
	status int
	body   string

	lastRequest *http.Request
	lastBody    map[string]any
}

func (s *stubTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	s.lastRequest = req
	if req.Body != nil {
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &s.lastBody); err != nil {
				return nil, err
			}
		}
	}

	status := s.status
	if status == 0 {
		status = http.StatusOK
	}
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(bytes.NewReader([]byte(s.body))),
		Header:     header,
	}, nil
}

// newStubClient returns a Client whose HTTP transport is stub, so Complete
// and Catalog's requests never leave the process. maxRetries=0 keeps
// error-path tests fast and deterministic (the default client retries
// transient-looking failures with backoff).
func newStubClient(stub *stubTransport) *Client {
	return NewClient("test-api-key", "https://stub.invalid/api/v1",
		option.WithHTTPClient(&http.Client{Transport: stub}),
		option.WithMaxRetries(0),
	)
}

const completeFixture = `{
  "id": "gen-abc123",
  "object": "chat.completion",
  "created": 1700000000,
  "model": "openai/gpt-4o",
  "choices": [
    {
      "index": 0,
      "finish_reason": "tool_calls",
      "logprobs": null,
      "message": {
        "role": "assistant",
        "content": "checking the weather",
        "refusal": null,
        "tool_calls": [
          {
            "id": "call_1",
            "type": "function",
            "function": {
              "name": "get_weather",
              "arguments": "{\"city\":\"nyc\"}"
            }
          }
        ]
      }
    }
  ],
  "usage": {
    "prompt_tokens": 100,
    "completion_tokens": 20,
    "total_tokens": 120,
    "cost": 0.000123
  }
}`

func TestComplete_RequestsUsageIncludeOnEveryCall(t *testing.T) {
	stub := &stubTransport{body: completeFixture}
	client := newStubClient(stub)

	req := Request{
		Model:    "openai/gpt-4o",
		Messages: []Message{{Role: RoleUser, Content: "what is the weather"}},
	}
	if _, err := client.Complete(context.Background(), req); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	usage, ok := stub.lastBody["usage"].(map[string]any)
	if !ok {
		t.Fatalf("request body had no \"usage\" object: %#v", stub.lastBody)
	}
	include, ok := usage["include"].(bool)
	if !ok || !include {
		t.Fatalf("request body usage.include = %#v, want true", usage["include"])
	}
}

func TestComplete_RequestsUsageIncludeRegardlessOfCallerRequest(t *testing.T) {
	// LB6: the worker asks for provider usage reporting unconditionally,
	// not opportunistically -- this is not gated on anything the caller
	// supplied in Request. Tools present or absent must not change it.
	for name, req := range map[string]Request{
		"no tools": {
			Model:    "openai/gpt-4o",
			Messages: []Message{{Role: RoleSystem, Content: "be helpful"}},
		},
		"with tools": {
			Model:    "openai/gpt-4o",
			Messages: []Message{{Role: RoleUser, Content: "hi"}},
			Tools: []ToolDefinition{{
				Name:       "get_weather",
				Parameters: map[string]any{"type": "object"},
			}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			stub := &stubTransport{body: completeFixture}
			client := newStubClient(stub)

			if _, err := client.Complete(context.Background(), req); err != nil {
				t.Fatalf("Complete: %v", err)
			}

			usage, ok := stub.lastBody["usage"].(map[string]any)
			if !ok || usage["include"] != true {
				t.Fatalf("usage.include not set to true: body=%#v", stub.lastBody)
			}
		})
	}
}

func TestComplete_ParsesResponse(t *testing.T) {
	stub := &stubTransport{body: completeFixture}
	client := newStubClient(stub)

	resp, err := client.Complete(context.Background(), Request{
		Model:    "openai/gpt-4o",
		Messages: []Message{{Role: RoleUser, Content: "what is the weather"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if resp.Message.Role != RoleAssistant {
		t.Errorf("Message.Role = %q, want %q", resp.Message.Role, RoleAssistant)
	}
	if resp.Message.Content != "checking the weather" {
		t.Errorf("Message.Content = %q, want %q", resp.Message.Content, "checking the weather")
	}

	if len(resp.ToolCalls) != 1 {
		t.Fatalf("len(ToolCalls) = %d, want 1", len(resp.ToolCalls))
	}
	tc := resp.ToolCalls[0]
	if tc.ID != "call_1" || tc.Name != "get_weather" || tc.Arguments != `{"city":"nyc"}` {
		t.Errorf("ToolCalls[0] = %+v, want {ID:call_1 Name:get_weather Arguments:{\"city\":\"nyc\"}}", tc)
	}
	// Message.ToolCalls must carry the same tool calls Response.ToolCalls
	// does -- assistantMessage (used when this Message round-trips back
	// as context on a later turn) reads Message.ToolCalls.
	if len(resp.Message.ToolCalls) != 1 || resp.Message.ToolCalls[0] != tc {
		t.Errorf("Message.ToolCalls = %+v, want [%+v]", resp.Message.ToolCalls, tc)
	}

	if resp.Usage.PromptTokens != 100 {
		t.Errorf("Usage.PromptTokens = %d, want 100", resp.Usage.PromptTokens)
	}
	if resp.Usage.CompletionTokens != 20 {
		t.Errorf("Usage.CompletionTokens = %d, want 20", resp.Usage.CompletionTokens)
	}
	if resp.Usage.GenerationID != "gen-abc123" {
		t.Errorf("Usage.GenerationID = %q, want %q", resp.Usage.GenerationID, "gen-abc123")
	}
	if resp.Usage.ProviderCostUSD == nil {
		t.Fatalf("Usage.ProviderCostUSD = nil, want non-nil")
	}
	// 0.000123 USD == 123 micro-dollars.
	if *resp.Usage.ProviderCostUSD != CostUSD(123) {
		t.Errorf("Usage.ProviderCostUSD = %d, want 123", *resp.Usage.ProviderCostUSD)
	}
}

func TestComplete_NoChoices_IsError(t *testing.T) {
	stub := &stubTransport{body: `{
		"id": "gen-empty",
		"object": "chat.completion",
		"created": 1700000000,
		"model": "openai/gpt-4o",
		"choices": [],
		"usage": {"prompt_tokens": 1, "completion_tokens": 0, "total_tokens": 1}
	}`}
	client := newStubClient(stub)

	if _, err := client.Complete(context.Background(), Request{Model: "openai/gpt-4o"}); err == nil {
		t.Fatalf("Complete: got nil error for a response with no choices")
	}
}

func TestToWireMessages(t *testing.T) {
	messages := []Message{
		{Role: RoleSystem, Content: "be helpful"},
		{Role: RoleUser, Content: "hi"},
		{Role: RoleAssistant, Content: "hello", ToolCalls: []ToolCall{
			{ID: "call_1", Name: "get_weather", Arguments: `{"city":"nyc"}`},
		}},
		{Role: RoleTool, Content: "72F", ToolCallID: "call_1"},
	}
	out := toWireMessages(messages)
	if len(out) != len(messages) {
		t.Fatalf("len(out) = %d, want %d", len(out), len(messages))
	}

	if out[0].OfSystem == nil || out[0].OfSystem.Content.OfString.Value != "be helpful" {
		t.Errorf("out[0] did not encode as a system message with content %q: %+v", "be helpful", out[0])
	}
	if out[1].OfUser == nil || out[1].OfUser.Content.OfString.Value != "hi" {
		t.Errorf("out[1] did not encode as a user message with content %q: %+v", "hi", out[1])
	}
	if out[2].OfAssistant == nil {
		t.Fatalf("out[2] did not encode as an assistant message: %+v", out[2])
	}
	if out[2].OfAssistant.Content.OfString.Value != "hello" {
		t.Errorf("out[2] assistant content = %q, want %q", out[2].OfAssistant.Content.OfString.Value, "hello")
	}
	if len(out[2].OfAssistant.ToolCalls) != 1 {
		t.Fatalf("out[2] assistant tool calls = %d, want 1", len(out[2].OfAssistant.ToolCalls))
	}
	call := out[2].OfAssistant.ToolCalls[0].OfFunction
	if call == nil || call.ID != "call_1" || call.Function.Name != "get_weather" || call.Function.Arguments != `{"city":"nyc"}` {
		t.Errorf("out[2] assistant tool call = %+v, want {ID:call_1 Function.Name:get_weather Function.Arguments:{\"city\":\"nyc\"}}", call)
	}
	if out[3].OfTool == nil || out[3].OfTool.ToolCallID != "call_1" || out[3].OfTool.Content.OfString.Value != "72F" {
		t.Errorf("out[3] did not encode as a tool message bound to call_1 with content %q: %+v", "72F", out[3])
	}
}

func TestToWireMessages_AssistantWithNoContent(t *testing.T) {
	// A tool-calling turn commonly has an empty Content -- assistantMessage
	// must omit Content in that case rather than sending an empty string,
	// matching how the wire protocol distinguishes "no content" from "".
	out := toWireMessages([]Message{{
		Role:      RoleAssistant,
		ToolCalls: []ToolCall{{ID: "call_1", Name: "f", Arguments: "{}"}},
	}})
	if len(out) != 1 || out[0].OfAssistant == nil {
		t.Fatalf("out = %+v, want one assistant message", out)
	}
	if out[0].OfAssistant.Content.OfString.Valid() {
		t.Errorf("assistant Content.OfString is set for an empty-content message: %+v", out[0].OfAssistant.Content)
	}
}

func TestToWireTools(t *testing.T) {
	if got := toWireTools(nil); got != nil {
		t.Errorf("toWireTools(nil) = %#v, want nil", got)
	}

	tools := []ToolDefinition{
		{
			Name:        "get_weather",
			Description: "look up the weather",
			Parameters:  map[string]any{"type": "object"},
		},
		{
			Name:       "no_description",
			Parameters: map[string]any{"type": "object"},
		},
	}
	out := toWireTools(tools)
	if len(out) != 2 {
		t.Fatalf("len(out) = %d, want 2", len(out))
	}

	first := out[0].OfFunction
	if first == nil || first.Function.Name != "get_weather" {
		t.Fatalf("out[0] = %+v, want a function tool named get_weather", out[0])
	}
	if !first.Function.Description.Valid() || first.Function.Description.Value != "look up the weather" {
		t.Errorf("out[0] Description = %+v, want %q", first.Function.Description, "look up the weather")
	}

	second := out[1].OfFunction
	if second == nil || second.Function.Name != "no_description" {
		t.Fatalf("out[1] = %+v, want a function tool named no_description", out[1])
	}
	if second.Function.Description.Valid() {
		t.Errorf("out[1] Description = %+v, want unset", second.Function.Description)
	}
}

func TestFromWireUsage_NoCostField(t *testing.T) {
	stub := &stubTransport{body: `{
		"id": "gen-nocost",
		"object": "chat.completion",
		"created": 1700000000,
		"model": "openai/gpt-4o",
		"choices": [{
			"index": 0,
			"finish_reason": "stop",
			"logprobs": null,
			"message": {"role": "assistant", "content": "hi", "refusal": null}
		}],
		"usage": {"prompt_tokens": 5, "completion_tokens": 3, "total_tokens": 8}
	}`}
	client := newStubClient(stub)

	resp, err := client.Complete(context.Background(), Request{Model: "openai/gpt-4o"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	// No "cost" extra field on usage -- ResolveCost (cost.go) must treat
	// this as "must estimate", never as "free", so ProviderCostUSD stays
	// nil rather than defaulting to a zero CostUSD.
	if resp.Usage.ProviderCostUSD != nil {
		t.Errorf("Usage.ProviderCostUSD = %v, want nil when the provider omits cost", *resp.Usage.ProviderCostUSD)
	}
}
