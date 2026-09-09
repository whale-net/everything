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
// Implementation phase (issue #2118) complete: Dispatch below wires
// client.go and keys.go together into the real body this file's doc
// comment documents -- resolve target server (FR8), mint a per-server
// credential (FR10), reserve/short-circuit on the idempotency key (FR11),
// call out, and return the domain server's result verbatim (FR2). Wiring
// Dispatch into SessionWorkflow's processTurn was deferred to issue #2118's
// Scope note and closed out by issue #2121's Implementation phase --
// whagent_net/worker/activities.go's DispatchTool activity is the
// activity-boundary wrapper, called once per tool call from
// whagent_net/worker/workflow.go's processTurn, under that file's
// workflow.GetVersion("session-workflow-tool-dispatch", ...) gate.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/libs/go/whagent"
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
// derives and reserves in.Call's idempotency key (FR11) before calling
// out, and returns the domain server's result verbatim (FR2) -- see this
// file's package doc comment for the full contract.
//
// Order of operations mirrors this doc comment exactly: resolve the target
// server and mint its credential (resolveTarget), reserve the idempotency
// key -- short-circuiting to the ledger's recorded outcome on a retry
// instead of re-dispatching the mutation -- then call out and record the
// outcome. A transport failure (server unreachable, timeout) is returned
// as a plain error with nothing recorded in the ledger, so a Temporal
// retry of the same call re-derives the identical key (idempotencyKey is a
// pure function of session/turn/call-index) and genuinely retries the
// call; a tool result with the domain server's own IsError set is *not* an
// error here -- it is returned as an ordinary Result, per FR2.
func (d *Dispatcher) Dispatch(ctx context.Context, in DispatchInput) (Result, error) {
	if in.Session == nil {
		return Result{}, fmt.Errorf("tools: DispatchInput.Session is nil")
	}
	if d.Issuer == nil {
		return Result{}, fmt.Errorf("tools: Dispatcher.Issuer is nil")
	}

	serverURL, cs, err := resolveTarget(ctx, d.Issuer, in)
	if err != nil {
		return Result{}, err
	}
	defer cs.Close()

	key := idempotencyKey(in.Session.SessionID.String(), in.Turn, in.CallIndex)

	if d.Ledger != nil {
		reservation, err := d.Ledger.Reserve(ctx, key, session.ToolCallReservation{
			SessionID: in.Session.SessionID,
			Turn:      in.Turn,
			CallIndex: in.CallIndex,
			Tool:      in.Call.Name,
			ServerURL: serverURL,
		})
		if err != nil {
			return Result{}, fmt.Errorf("tools: reserve idempotency key for call %q: %w", in.Call.Name, err)
		}
		if len(reservation.Outcome) > 0 {
			// A prior attempt already dispatched and recorded this exact
			// (session, turn, call_index) -- return its outcome verbatim
			// rather than re-executing the mutation (FR11).
			var cached Result
			if err := json.Unmarshal(reservation.Outcome, &cached); err != nil {
				return Result{}, fmt.Errorf("tools: decode cached outcome for call %q: %w", in.Call.Name, err)
			}
			return cached, nil
		}
	}

	args, err := decodeArguments(in.Call.Arguments)
	if err != nil {
		return Result{}, fmt.Errorf("tools: decode arguments for call %q: %w", in.Call.Name, err)
	}
	// Every dispatched call carries idempotency_key as a top-level argument
	// (FR11, LB4) -- Dispatch has no principled way to know which of a
	// server's tools mutate state (that classification is the domain
	// server's own, via whagent.IdempotencyKeyed), so this is attached
	// unconditionally; a read-only tool's handler simply never looks at it.
	args[whagent.IdempotencyKeyArgument] = key

	callRes, err := CallTool(ctx, cs, in.Call.Name, args)
	if err != nil {
		// A transport failure -- distinct from a tool-level IsError result,
		// which CallTool never turns into a Go error. Nothing was recorded
		// in the ledger above, so a retry of this same call re-dispatches.
		return Result{}, err
	}

	result := Result{
		ToolCallID: in.Call.ID,
		Name:       in.Call.Name,
		Content:    resultContent(callRes),
		IsError:    callRes.IsError,
	}

	if d.Ledger != nil {
		outcome, err := json.Marshal(result)
		if err != nil {
			return Result{}, fmt.Errorf("tools: marshal outcome for call %q: %w", in.Call.Name, err)
		}
		if err := d.Ledger.RecordOutcome(ctx, key, outcome); err != nil {
			return Result{}, fmt.Errorf("tools: record outcome for call %q: %w", in.Call.Name, err)
		}
	}

	return result, nil
}

// resolveTarget finds which entry of in.ToolSet exposes in.Call.Name
// (FR8), mints a persona credential scoped to exactly that one server
// (FR10, keys.go's mintCredential), and returns an authenticated,
// connected *mcp.ClientSession to it -- the caller owns the returned
// session's lifecycle (Close). A call naming a tool no configured server
// exposes returns an error before any CallTool is ever issued against any
// server -- this is where FR8's "a tool the server does not expose is not
// callable" is actually enforced. A credential is minted (and a
// connection opened) for each server tried, in in.ToolSet order, and is
// never reused across servers.
func resolveTarget(ctx context.Context, issuer *persona.Issuer, in DispatchInput) (string, *mcp.ClientSession, error) {
	for _, ref := range in.ToolSet {
		token, err := mintCredential(ctx, issuer, in.Session, in.AgentID, ref.ServerURL)
		if err != nil {
			return "", nil, fmt.Errorf("tools: mint credential for %s: %w", ref.ServerURL, err)
		}

		cs, err := Connect(ctx, ref.ServerURL, token)
		if err != nil {
			return "", nil, fmt.Errorf("tools: connect to %s: %w", ref.ServerURL, err)
		}

		names, err := ListToolNames(ctx, cs)
		if err != nil {
			cs.Close()
			return "", nil, fmt.Errorf("tools: list tools on %s: %w", ref.ServerURL, err)
		}

		if _, ok := names[in.Call.Name]; ok {
			return ref.ServerURL, cs, nil
		}
		cs.Close()
	}
	return "", nil, fmt.Errorf("tools: call %q: no configured server exposes this tool", in.Call.Name)
}

// decodeArguments decodes a tool call's raw JSON arguments (llm.ToolCall.
// Arguments) into the map CallTool's Arguments parameter expects, treating
// an empty string as "no arguments" rather than a decode error -- a model
// may request a zero-argument tool call.
func decodeArguments(raw string) (map[string]any, error) {
	args := map[string]any{}
	if raw == "" {
		return args, nil
	}
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return nil, err
	}
	if args == nil {
		args = map[string]any{}
	}
	return args, nil
}

// resultContent flattens res's content blocks into Result.Content: M1's
// domain servers (audience_score_system/mcp) return text content, so
// joining every TextContent block's text (newline-separated) is enough for
// now -- non-text content blocks (image/audio/embedded-resource) are
// silently skipped rather than failing the whole call, since there is
// nothing in Result's string-shaped Content field to put them in yet.
func resultContent(res *mcp.CallToolResult) string {
	if res == nil {
		return ""
	}
	parts := make([]string, 0, len(res.Content))
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n")
}
