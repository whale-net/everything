// gRPC status -> MCP tool error translation, shared by every tool in
// this package (issue #2120's Implementation section, "Error mapping").
// A regular Go error returned from a tool's call method is wrapped by
// the go-sdk into a CallToolResult with IsError:true and the error's own
// message as its text content (mcp.AddTool's doc comment) -- so the
// message toolError produces IS what the operator sees in Claude Code.
// It must stay legible without reading api's logs: the gRPC status code
// plus api's own message, never a generic "internal error".
package tools

import (
	"errors"
	"fmt"

	"google.golang.org/grpc/status"
)

// toolError formats err -- expected to be (or wrap) a gRPC status error
// returned by a pb.SessionServiceClient call -- as an MCP tool error for
// rpcName. The status code names the failure class an operator needs to
// react to (PERMISSION_DENIED, FAILED_PRECONDITION, NOT_FOUND,
// INVALID_ARGUMENT, ...) and the status message is api's own, preserved
// verbatim. A non-status err (e.g. a transport-level failure that never
// reached api) falls back to its plain Error() text, still prefixed with
// rpcName so the operator knows which underlying RPC failed.
func toolError(rpcName string, err error) error {
	if err == nil {
		return nil
	}
	if st, ok := status.FromError(err); ok && st != nil {
		return fmt.Errorf("%s: %s: %s", rpcName, st.Code(), st.Message())
	}
	return fmt.Errorf("%s: %w", rpcName, err)
}

// reauthRequiredError is the mid-call grpcauth.ErrGrantNeedsReauth
// translation (issue #2431, FR18): dispatch.go's acquireGrantToken detects
// errors.Is(err, grpcauth.ErrGrantNeedsReauth) coming back from
// GrantSource.TokenSource(...).Token(ctx) and, instead of leaving that
// error as the plain scope-naming wrap #2430's acquireGrantToken already
// produces for every other grant failure, returns this instead -- it exists
// as its own type (rather than reusing that plain wrap unchanged) so a
// caller that needs to distinguish "no grant at all" / "revoked" from
// "grant exists but a human must re-consent" can do so with errors.As,
// without string-matching Error() text, while errors.Is(err,
// grpcauth.ErrGrantNeedsReauth) still holds through Unwrap.
//
// Deliberately does not import libs/go/grpcauth for the sentinel itself --
// cause is whatever GrantSource.TokenSource(...).Token(ctx) returned
// (already wrapping grpcauth.ErrGrantNeedsReauth), so Unwrap alone is
// sufficient and this package's existing dependency surface is unchanged.
//
// Scope is always the value dispatch.go's acquireGrantToken already
// resolved before calling TokenSource -- never re-derived from cause's own
// text -- so it is exact even if a future wrap of ErrGrantNeedsReauth
// changes its message.
type reauthRequiredError struct {
	scope string
	cause  error
}

// newReauthRequiredError builds the mid-call reauth-required error for
// scope, wrapping cause (expected to be, or wrap, grpcauth.ErrGrantNeedsReauth).
func newReauthRequiredError(scope string, cause error) error {
	return &reauthRequiredError{scope: scope, cause: cause}
}

// Error names scope explicitly and points at the standalone per-scope
// consent route (issue #2428's GET /mcp/consent?scope=<d>, mounted on
// `ui`) so an operator reading the tool-call failure in their transcript
// (mcp.AddTool's doc comment: a returned error's own message IS what the
// operator sees) has both what happened and exactly where to go to fix
// it -- not an opaque "auth failed".
func (e *reauthRequiredError) Error() string {
	return fmt.Sprintf("scope %q needs re-consent (grant rejected by Keycloak); complete consent again at GET /mcp/consent?scope=%s: %s", e.scope, e.scope, e.cause)
}

// Unwrap exposes cause so errors.Is(err, grpcauth.ErrGrantNeedsReauth) still
// matches through this wrapper.
func (e *reauthRequiredError) Unwrap() error {
	return e.cause
}

// reauthScope reports the affected scope if err is or wraps a
// reauthRequiredError, for callers (dispatch.go's structured WARNING
// logging) that need the scope value without re-parsing Error() text.
func reauthScope(err error) (string, bool) {
	var e *reauthRequiredError
	if errors.As(err, &e) {
		return e.scope, true
	}
	return "", false
}
