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
