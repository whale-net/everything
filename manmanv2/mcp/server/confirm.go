package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Reserved argument names handled by the protocol, never part of a tool's own args.
const (
	ArgConfirmationToken = "confirmation_token"
	ArgIdempotencyKey    = "idempotency_key"
)

var (
	// ErrArgsChanged: the confirmed call's arguments differ from the previewed ones.
	ErrArgsChanged = errors.New("arguments differ from the previewed call; nothing was changed. Call again without confirmation_token to get a new preview")
	// ErrEntityChanged: the target changed after the preview was issued.
	ErrEntityChanged = errors.New("the target changed since the preview; nothing was changed. Re-preview by calling again without confirmation_token")
)

// GatedTool is what a destructive tool supplies to the confirmation protocol.
type GatedTool struct {
	Name string
	// Preview describes the effect without mutating, and returns a fingerprint
	// of the targeted entity's current state (empty if the tool has none).
	Preview func(ctx context.Context, args json.RawMessage) (preview any, fingerprint string, err error)
	// Apply performs the mutation.
	Apply func(ctx context.Context, args json.RawMessage) (any, error)
}

// Outcome of one gated call.
type Outcome struct {
	// Applied is true when Apply ran; Result holds its return value.
	Applied bool `json:"applied"`
	Result  any  `json:"result,omitempty"`
	// Declined is true when an elicitation was declined or cancelled.
	Declined bool `json:"declined,omitempty"`
	// Preview, ConfirmationToken and ExpiresAt are set on the first call of the two-call path.
	Preview           any        `json:"preview,omitempty"`
	ConfirmationToken string     `json:"confirmation_token,omitempty"`
	ExpiresAt         *time.Time `json:"expires_at,omitempty"`
}

// Elicitor is the client side of in-call confirmation.
type Elicitor interface {
	SupportsElicitation() bool
	Elicit(ctx context.Context, message string) (accepted bool, err error)
}

// SessionElicitor adapts an MCP server session; a nil session supports nothing.
type SessionElicitor struct{ Session *mcp.ServerSession }

func (e SessionElicitor) SupportsElicitation() bool {
	if e.Session == nil {
		return false
	}
	p := e.Session.InitializeParams()
	return p != nil && p.Capabilities != nil && p.Capabilities.Elicitation != nil
}

func (e SessionElicitor) Elicit(ctx context.Context, message string) (bool, error) {
	res, err := e.Session.Elicit(ctx, &mcp.ElicitParams{
		Message:         message,
		RequestedSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	})
	if err != nil {
		return false, err
	}
	return res.Action == "accept", nil
}

// Gate runs gated tools through preview -> confirm.
//
// Compose with the idempotency middleware by placing idempotency OUTSIDE the
// gate: a replayed confirmed call then returns the stored result before the
// (already consumed) token is ever looked at.
type Gate struct {
	Store ConfirmationStore
	TTL   time.Duration
	Now   func() time.Time
}

func (g *Gate) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

// HashArgs canonically hashes args, excluding the protocol-reserved keys.
func HashArgs(args json.RawMessage) (string, error) {
	m := map[string]any{}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &m); err != nil {
			return "", fmt.Errorf("arguments must be a JSON object: %w", err)
		}
	}
	delete(m, ArgConfirmationToken)
	delete(m, ArgIdempotencyKey)
	b, _ := json.Marshal(m) // map keys marshal sorted
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func confirmationTokenArg(args json.RawMessage) string {
	var m struct {
		T string `json:"confirmation_token"`
	}
	_ = json.Unmarshal(args, &m)
	return m.T
}

// Call executes one gated call for caller. elicit may be nil.
func (g *Gate) Call(ctx context.Context, caller *Caller, elicit Elicitor, tool GatedTool, args json.RawMessage) (*Outcome, error) {
	if caller == nil {
		return nil, ErrUnauthenticated
	}
	hash, err := HashArgs(args)
	if err != nil {
		return nil, err
	}
	bind := ConfirmationToken{Issuer: caller.Issuer, Subject: caller.Subject, Tool: tool.Name, ArgsHash: hash}

	if id := confirmationTokenArg(args); id != "" {
		fp, err := g.Store.Consume(ctx, id, bind)
		if err != nil {
			if errors.Is(err, ErrTokenNotConsumable) && g.Store.ArgsMismatch(ctx, id, bind) {
				return nil, ErrArgsChanged
			}
			return nil, err
		}
		// Token is spent; a stale target means the caller must re-preview.
		_, cur, err := tool.Preview(ctx, args)
		if err != nil {
			return nil, err
		}
		if cur != fp {
			return nil, ErrEntityChanged
		}
		res, err := tool.Apply(ctx, args)
		if err != nil {
			return nil, err
		}
		return &Outcome{Applied: true, Result: res}, nil
	}

	preview, fp, err := tool.Preview(ctx, args)
	if err != nil {
		return nil, err
	}
	if elicit != nil && elicit.SupportsElicitation() {
		pj, _ := json.Marshal(preview)
		ok, eerr := elicit.Elicit(ctx, fmt.Sprintf("Confirm %s? %s", tool.Name, pj))
		if eerr == nil {
			if !ok {
				return &Outcome{Declined: true, Preview: preview}, nil
			}
			res, err := tool.Apply(ctx, args)
			if err != nil {
				return nil, err
			}
			return &Outcome{Applied: true, Result: res}, nil
		}
		// Elicitation failed in transport: fall back to the two-call path.
	}

	ttl := g.TTL
	if ttl == 0 {
		ttl = ConfirmationTTL
	}
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return nil, err
	}
	bind.ID = hex.EncodeToString(raw[:])
	bind.Fingerprint = fp
	bind.ExpiresAt = g.now().Add(ttl)
	if err := g.Store.Issue(ctx, bind); err != nil {
		return nil, err
	}
	exp := bind.ExpiresAt
	return &Outcome{Preview: preview, ConfirmationToken: bind.ID, ExpiresAt: &exp}, nil
}

// Handler adapts a gated tool to an MCP tool handler. The tool's input schema
// must permit confirmation_token (and idempotency_key).
func (g *Gate) Handler(tool GatedTool) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		out, err := g.Call(ctx, CallerFromContext(ctx), SessionElicitor{Session: req.Session}, tool, req.Params.Arguments)
		if err != nil {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}, nil
		}
		b, _ := json.Marshal(out)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, nil
	}
}
