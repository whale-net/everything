// Wire-shape conversion helpers shared by every tool in this package:
// pb.SessionState/CapKind/ErrorCategory each need to round-trip to the
// same lowercase strings whagent_net/session.Status/CapKind/ErrorCategory
// already use (and that each tool's *Output doc comment promises), but
// this package intentionally does not import whagent_net/session itself
// -- mcp is a pure facade over api's SessionService (issue #2120's
// Implementation section) and must carry no dependency on the store
// package api and worker share directly. These functions are therefore a
// second, independent copy of that string table, keyed off the proto
// enum instead.
package tools

import pb "github.com/whale-net/everything/whagent_net/protos"

// sessionStateString renders s as the lowercase string every *Output.State
// field in this package documents (SESSION_STATE_UNSPECIFIED renders as
// "unspecified" -- api never sends this for a real session, but a tool
// must not panic or silently drop the field if it ever did).
func sessionStateString(s pb.SessionState) string {
	switch s {
	case pb.SessionState_SESSION_STATE_RUNNING:
		return "running"
	case pb.SessionState_SESSION_STATE_AWAITING_INPUT:
		return "awaiting_input"
	case pb.SessionState_SESSION_STATE_DONE:
		return "done"
	case pb.SessionState_SESSION_STATE_STOPPED:
		return "stopped"
	case pb.SessionState_SESSION_STATE_FAILED:
		return "failed"
	case pb.SessionState_SESSION_STATE_CAPPED:
		return "capped"
	default:
		return "unspecified"
	}
}

// capKindString renders k as get_session's cap_kind field documents.
func capKindString(k pb.CapKind) string {
	switch k {
	case pb.CapKind_CAP_KIND_TURNS:
		return "turns"
	case pb.CapKind_CAP_KIND_COST:
		return "cost"
	default:
		return "unspecified"
	}
}

// errorCategoryString renders c as get_session's error_category field
// documents.
func errorCategoryString(c pb.ErrorCategory) string {
	switch c {
	case pb.ErrorCategory_ERROR_CATEGORY_RETRYABLE:
		return "retryable"
	case pb.ErrorCategory_ERROR_CATEGORY_NON_RETRYABLE:
		return "non_retryable"
	default:
		return "unspecified"
	}
}
