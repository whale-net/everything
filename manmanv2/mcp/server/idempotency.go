package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// IdempotencyKeyArg is the optional argument write tools accept.
const IdempotencyKeyArg = "idempotency_key"

// Errors for idempotent write calls. Neither triggers a backend call.
var (
	ErrIdempotencyConflict = errors.New("mcp: idempotency_key reused with different arguments")
	ErrIdempotencyInFlight = errors.New("mcp: a call with this idempotency_key is still in progress; retry shortly")
	ErrIdempotencyRequired = errors.New("mcp: idempotency_key is required for this tool")
)

// Idempotency makes write tools safe to retry: a repeat of a completed call
// (same caller, tool, key and arguments) returns the stored result without a
// backend call; the same key with different arguments is refused; concurrent
// duplicates run the backend once.
//
// Install it so it runs after the persona Middleware (which sets the caller)
// and before any confirmation middleware, so a replay of an already-confirmed
// call returns the stored result ahead of token validation.
func Idempotency(reg *Registry, store IdempotencyStore) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method != "tools/call" {
				return next(ctx, method, req)
			}
			p, ok := req.GetParams().(*mcp.CallToolParamsRaw)
			if !ok || p == nil {
				return next(ctx, method, req)
			}
			tool, ok := reg.Lookup(p.Name)
			caller := CallerFromContext(ctx)
			if !ok || !tool.Write || caller == nil {
				return next(ctx, method, req)
			}
			key, hash, err := splitIdempotency(p.Arguments)
			if err != nil {
				return nil, err
			}
			if key == "" {
				if tool.KeyRequired {
					return nil, ErrIdempotencyRequired
				}
				return next(ctx, method, req)
			}
			k := IdemKey{Issuer: caller.Issuer, Subject: caller.Subject, Tool: p.Name, Key: key}
			rsv, err := store.Reserve(ctx, k, hash)
			if err != nil {
				return nil, fmt.Errorf("mcp: idempotency store: %w", err)
			}
			switch rsv.State {
			case IdemConflict:
				return nil, ErrIdempotencyConflict
			case IdemInFlight:
				return nil, ErrIdempotencyInFlight
			case IdemReplay:
				var cr mcp.CallToolResult
				if err := json.Unmarshal(rsv.Result, &cr); err != nil {
					return nil, fmt.Errorf("mcp: stored idempotent result unreadable: %w", err)
				}
				return &cr, nil
			}

			res, err := next(ctx, method, req)
			cr, _ := res.(*mcp.CallToolResult)
			if err != nil || cr == nil || cr.IsError {
				// Failures aren't recorded: a retry should re-attempt the call.
				_ = store.Release(context.WithoutCancel(ctx), k)
				return res, err
			}
			b, merr := json.Marshal(cr)
			if merr == nil {
				merr = store.Complete(context.WithoutCancel(ctx), k, b)
			}
			if merr != nil {
				return res, fmt.Errorf("mcp: idempotency store: %w", merr)
			}
			return res, nil
		}
	}
}

// splitIdempotency extracts idempotency_key from raw args and hashes the
// remaining arguments canonically (key order independent).
func splitIdempotency(raw json.RawMessage) (key, hash string, err error) {
	m := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &m); err != nil {
			return "", "", fmt.Errorf("mcp: invalid arguments: %w", err)
		}
	}
	if v, ok := m[IdempotencyKeyArg]; ok {
		s, isStr := v.(string)
		if !isStr {
			return "", "", fmt.Errorf("mcp: %s must be a string", IdempotencyKeyArg)
		}
		key = s
		delete(m, IdempotencyKeyArg)
	}
	canon, err := json.Marshal(m) // map keys are sorted
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(canon)
	return key, hex.EncodeToString(sum[:]), nil
}
