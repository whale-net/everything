// Package tools is whagent-net worker's tool-dispatch package (issue
// #2118; ARCHITECTURE.md "Session workflow" step 4, "Domain-owned MCP
// servers and the tool contract", "Identity and auth chaining",
// "Idempotency"): it fills in the no-op hook workflow.go's processTurn
// left at the tool-call dispatch step (issue #2114), routing each of a
// turn's model-requested tool calls (llm.ToolCall) to the agent
// definition's domain-owned MCP server, carrying a per-server whagent-net
// persona credential (FR10) and a deterministic idempotency key (FR11) on
// every call.
//
// # Package layout
//
//   - client.go -- the MCP client transport half: Connect (a streamable-
//     HTTP mcp.ClientSession authenticated with a bearer credential),
//     ListToolNames (FR8's server-exposed tool set), and CallTool.
//   - keys.go -- the two "keys" every dispatched call carries:
//     idempotencyKey (FR11, wrapping whagent.DeriveIdempotencyKey) and
//     mintCredential (FR10, wrapping persona.Issuer.Issue).
//   - dispatch.go (this file) -- Dispatcher/Dispatch, the orchestration
//     seam activities.go's follow-up ExecuteActivity call (workflow.go's
//     processTurn tool-dispatch step, under a
//     workflow.GetVersion("session-workflow-tool-dispatch", ...) gate per
//     that file's NFR1 doc comment) drives per tool call.
//
// # Tool selection (FR8)
//
// An agent definition's tool_set (session.ToolServerRef) is
// {server_url, allowed_tools}. For M1 the session sees exactly the subset
// of tools a server chooses to expose to it -- enforced server-side via a
// pre-filtered endpoint (e.g. a scoped MCP path). session.ToolServerRef's
// AllowedTools field exists per LB5, but whagent-side enforcement of it is
// explicitly out of scope (C22, Later) -- Dispatch must never filter a
// server's exposed tool list itself, and a nil AllowedTools is not a bug.
//
// # Persona credential (FR10, NFR4)
//
// Every call carries a whagent-net-signed, short-lived credential minted
// per #2115 with aud == the one target server the call is dispatched to,
// sub/sub_iss == the session's on_behalf_of subject, act == the acting
// subject plus agent_id, and whagent_session_id == the session. Minted
// fresh per target server (keys.go's mintCredential); never reused across
// servers, and the caller's own Keycloak token is never forwarded.
//
// # Idempotency (FR11, LB4)
//
// Every mutating call carries idempotency_key
// (whagent.IdempotencyKeyArgument) derived via keys.go's idempotencyKey --
// deterministic and never regenerated on Temporal's at-least-once activity
// retry. Dispatch reserves the key in session.IdempotencyLedger before
// calling out, and records the outcome after, so a retried activity
// returns the recorded result instead of re-executing the mutation. The
// domain server's own idempotency guard (scoped on (tool, resolved
// identity, key), whagent.IdempotencyGuardScope) is independent by design.
//
// # isError is not a whagent-net failure (FR2)
//
// A tool result the domain server returns with its own IsError flag set
// is an ordinary tool-result event -- Dispatch/Result must never
// reinterpret it as a retryable/non-retryable session failure. That
// classification is whagent-net's own judgement about the session's
// terminal outcome, made elsewhere (a follow-up cap-enforcement/terminal-
// classification task), never originated by a domain server's response.
//
// Scaffold phase (this task): the file layout, BUILD deps
// (//libs/go/whagent, whagent_net/api/persona), and Dispatcher/
// DispatchInput/Result's fixed shapes are in place; Dispatch itself is a
// stub. Implementation phase wires client.go/keys.go together into a real
// Dispatch body per this doc comment.
package tools

import (
	"context"
	"fmt"

	"github.com/whale-net/everything/whagent_net/api/persona"
	"github.com/whale-net/everything/whagent_net/llm"
	"github.com/whale-net/everything/whagent_net/session"
)

// Result is one tool call's dispatch outcome -- the content FR2's
// tool-result transcript event is built from. IsError mirrors the domain
// server's own mcp.CallToolResult.IsError verbatim (see this file's
// package doc comment, "isError is not a whagent-net failure") -- it is
// never whagent-net's own failure classification.
type Result struct {
	// ToolCallID is the model-assigned ID (llm.ToolCall.ID) this result
	// answers -- carried through so a committed tool-result transcript
	// event can bind back to its tool-call event the same way an
	// llm.Message with RoleTool binds via ToolCallID (context.go).
	ToolCallID string
	// Name is the dispatched tool's name (llm.ToolCall.Name).
	Name string
	// Content is the domain server's raw tool result content, exactly as
	// returned -- Implementation phase decides the exact encoding (e.g.
	// concatenated text content blocks) once CallTool's *mcp.CallToolResult
	// is available to convert.
	Content string
	// IsError mirrors the domain server's own isError flag on this result
	// (FR2) -- an ordinary property of the result, not a whagent-net
	// failure signal.
	IsError bool
}

// DispatchInput is Dispatch's argument: everything needed to route,
// authenticate, and idempotency-guard one tool call.
type DispatchInput struct {
	// Session is the whagent-net session this call belongs to -- its
	// OnBehalfOf/Subject fields are what keys.go's mintCredential turns
	// into the persona credential's sub/sub_iss/act (FR10).
	Session *session.Session
	// AgentID is the current agent definition's ID for this turn (per
	// persona.Issuer.Issue's doc comment: passed explicitly rather than
	// read from Session, since a session's assignment can drift, SCD2).
	AgentID string
	// ToolSet is the current agent definition's tool_set (LB5) --
	// Dispatch resolves which entry exposes Call.Name (FR8) rather than
	// receiving the target server pre-selected.
	ToolSet []session.ToolServerRef
	// Turn and CallIndex, together with Session.SessionID, are
	// idempotencyKey's three derivation inputs (FR11) -- CallIndex is the
	// tool call's position within Turn's response (0-based), stable
	// across an activity retry of the same call.
	Turn      int
	CallIndex int
	// Call is the model-requested tool call (llm.ToolCall) to dispatch.
	Call llm.ToolCall
}

// Dispatcher dispatches one turn's tool calls to the agent definition's
// domain-owned MCP server(s) (see this package's doc comment).
type Dispatcher struct {
	// Issuer mints the per-target-server persona credential every call
	// carries (FR10) -- worker's own in-process *persona.Issuer (see
	// keys.go's mintCredential doc comment).
	Issuer *persona.Issuer
	// Ledger reserves/records this call's idempotency outcome
	// (session.IdempotencyLedger, LB4/FR11) before/after dispatch.
	Ledger session.IdempotencyLedger
}

// Dispatch routes in.Call to whichever entry of in.ToolSet exposes it
// (FR8), mints a persona credential scoped to that one server (FR10),
// derives and reserves in.Call's idempotency key (FR11) when the call
// mutates state, and returns the domain server's result verbatim (FR2).
//
// Not implemented in this Scaffold-phase task (issue #2118) -- see this
// file's package doc comment for the full contract Implementation phase
// wires client.go and keys.go together to satisfy.
func (d *Dispatcher) Dispatch(ctx context.Context, in DispatchInput) (Result, error) {
	return Result{}, fmt.Errorf("tools: Dispatch not implemented (issue #2118 Implementation phase)")
}
