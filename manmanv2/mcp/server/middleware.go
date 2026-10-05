package server

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Middleware gates the MCP protocol surface by persona: tools/list shows only
// callable tools, tools/call is refused (no handler, hence no backend call)
// below a tool's minimum persona, and every call is audited.
func Middleware(reg *Registry, audit Auditor) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			var caller *Caller
			if extra := req.GetExtra(); extra != nil {
				caller = callerFromExtra(extra.TokenInfo)
			}
			if caller == nil {
				caller = CallerFromContext(ctx)
			}
			switch method {
			case "tools/call":
				return handleCall(ctx, reg, audit, caller, next, method, req)
			case "tools/list":
				if caller == nil {
					return nil, ErrUnauthenticated
				}
				res, err := next(ContextWithCaller(ctx, caller), method, req)
				if err != nil {
					return res, err
				}
				if lr, ok := res.(*mcp.ListToolsResult); ok {
					visible := map[string]bool{}
					for _, t := range reg.Visible(caller.Persona) {
						visible[t.Name] = true
					}
					kept := lr.Tools[:0:0]
					for _, t := range lr.Tools {
						if visible[t.Name] {
							kept = append(kept, t)
						}
					}
					lr.Tools = kept
				}
				return res, nil
			default:
				if caller == nil {
					// A verified whagent identity is resolved only for tool methods.
					if extra := req.GetExtra(); extra != nil && extra.TokenInfo != nil {
						if _, ok := extra.TokenInfo.Extra[whagentClaimExtraKey]; ok {
							return next(ctx, method, req)
						}
					}
					return nil, ErrUnauthenticated
				}
				return next(ContextWithCaller(ctx, caller), method, req)
			}
		}
	}
}

func handleCall(ctx context.Context, reg *Registry, audit Auditor, caller *Caller, next mcp.MethodHandler, method string, req mcp.Request) (mcp.Result, error) {
	var name string
	var args json.RawMessage
	if p, ok := req.GetParams().(*mcp.CallToolParamsRaw); ok && p != nil {
		name, args = p.Name, p.Arguments
	}
	rec := AuditRecord{Tool: name, TargetID: targetID(reg, name, args)}
	if caller == nil {
		rec.Outcome, rec.Reason = OutcomeRefused, "unauthenticated"
		audit.Record(ctx, rec)
		return nil, ErrUnauthenticated
	}
	rec.Subject, rec.Persona, rec.Agent = caller.Subject, caller.Persona, caller.Agent

	if err := reg.Authorize(caller.Persona, name); err != nil {
		rec.Outcome, rec.Reason = OutcomeRefused, err.Error()
		audit.Record(ctx, rec)
		return nil, fmt.Errorf("%w: tool %q requires a higher persona than %s", err, name, caller.Persona)
	}

	ctx = ContextWithCaller(ctx, caller)
	if tool, _ := reg.Lookup(name); tool.Snapshot != nil {
		snap, err := tool.Snapshot(CallContext{Context: ctx, Caller: caller}, args)
		if err != nil {
			rec.Reason = "snapshot failed: " + err.Error()
		}
		rec.Snapshot = snap
	}

	res, err := next(ctx, method, req)
	rec.Outcome = OutcomeAllowed
	if err != nil {
		rec.Outcome, rec.Reason = OutcomeError, err.Error()
	} else if cr, ok := res.(*mcp.CallToolResult); ok && cr != nil && cr.IsError {
		rec.Outcome, rec.Reason = OutcomeError, "tool returned error result"
	}
	audit.Record(ctx, rec)
	return res, err
}

func targetID(reg *Registry, name string, args json.RawMessage) string {
	t, ok := reg.Lookup(name)
	if !ok || t.TargetArg == "" || len(args) == 0 {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(args, &m) != nil {
		return ""
	}
	if v, ok := m[t.TargetArg]; ok && v != nil {
		return fmt.Sprint(v)
	}
	return ""
}

// ToolCallCaller is the mcpobs caller attribute: the persona of a caller
// authenticated at the HTTP layer, if any.
func ToolCallCaller(req mcp.Request) (string, string, bool) {
	extra := req.GetExtra()
	if extra == nil {
		return "", "", false
	}
	if c := callerFromExtra(extra.TokenInfo); c != nil {
		return "persona", c.Persona.String(), true
	}
	return "", "", false
}
