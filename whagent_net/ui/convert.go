// Wire-shape conversion helpers between whagentpb's generated types and
// this binary's plain rendering-ready view structs
// (whagent_net/ui/components.SessionView/SubjectView/TranscriptEventView).
// Mirrors whagent_net/mcp/tools/convert.go's stance: this package does not
// hand proto types to the templ layer directly, so the rendering layer
// never depends on generated code, but -- unlike mcp/tools -- `ui` is
// already a `whagentpb` consumer for everything else (grpc_client.go), so
// this is purely a rendering-boundary convenience, not a dependency
// avoidance.
package main

import (
	whagentpb "github.com/whale-net/everything/whagent_net/protos"
	"github.com/whale-net/everything/whagent_net/ui/components"
)

// sessionStateString renders s as the lowercase string get_session's
// mcp/tools/convert.go documents (SESSION_STATE_UNSPECIFIED renders as
// "unspecified" -- a real session never reports this, but a handler must
// not panic or silently drop the field if it ever did).
func sessionStateString(s whagentpb.SessionState) string {
	switch s {
	case whagentpb.SessionState_SESSION_STATE_RUNNING:
		return "running"
	case whagentpb.SessionState_SESSION_STATE_AWAITING_INPUT:
		return "awaiting_input"
	case whagentpb.SessionState_SESSION_STATE_DONE:
		return "done"
	case whagentpb.SessionState_SESSION_STATE_STOPPED:
		return "stopped"
	case whagentpb.SessionState_SESSION_STATE_FAILED:
		return "failed"
	case whagentpb.SessionState_SESSION_STATE_CAPPED:
		return "capped"
	default:
		return "unspecified"
	}
}

// capKindString renders k the way GetSessionOutput.CapKind documents.
func capKindString(k whagentpb.CapKind) string {
	switch k {
	case whagentpb.CapKind_CAP_KIND_TURNS:
		return "turns"
	case whagentpb.CapKind_CAP_KIND_COST:
		return "cost"
	case whagentpb.CapKind_CAP_KIND_TOOL_ITERATIONS:
		return "tool_iterations"
	default:
		return "unspecified"
	}
}

// errorCategoryString renders c the way GetSessionOutput.ErrorCategory
// documents.
func errorCategoryString(c whagentpb.ErrorCategory) string {
	switch c {
	case whagentpb.ErrorCategory_ERROR_CATEGORY_RETRYABLE:
		return "retryable"
	case whagentpb.ErrorCategory_ERROR_CATEGORY_NON_RETRYABLE:
		return "non_retryable"
	default:
		return "unspecified"
	}
}

// subjectKindString renders k as NFR3's "started by" line documents:
// "human" or "service", never a raw enum name.
func subjectKindString(k whagentpb.SubjectKind) string {
	switch k {
	case whagentpb.SubjectKind_SUBJECT_KIND_HUMAN:
		return "human"
	case whagentpb.SubjectKind_SUBJECT_KIND_SERVICE:
		return "service"
	default:
		return "unspecified"
	}
}

// sessionStateFromString is sessionStateString's reverse (GET /sessions'
// `state` filter, FR3/C15, issue #2247): ok is false for any string other
// than the six lowercase state values sessionStateString emits -- callers
// treat "" (unset) as a separate case before ever calling this, so
// "unspecified" is deliberately not a recognized input here.
func sessionStateFromString(s string) (state whagentpb.SessionState, ok bool) {
	switch s {
	case "running":
		return whagentpb.SessionState_SESSION_STATE_RUNNING, true
	case "awaiting_input":
		return whagentpb.SessionState_SESSION_STATE_AWAITING_INPUT, true
	case "done":
		return whagentpb.SessionState_SESSION_STATE_DONE, true
	case "stopped":
		return whagentpb.SessionState_SESSION_STATE_STOPPED, true
	case "failed":
		return whagentpb.SessionState_SESSION_STATE_FAILED, true
	case "capped":
		return whagentpb.SessionState_SESSION_STATE_CAPPED, true
	default:
		return whagentpb.SessionState_SESSION_STATE_UNSPECIFIED, false
	}
}

// subjectKindFromString is subjectKindString's reverse (GET /sessions'
// `started_by_kind` filter, FR3/NFR3, issue #2247): ok is false for any
// string other than "human"/"service" -- see sessionStateFromString's doc
// comment for why "" and "unspecified" are both rejected here.
func subjectKindFromString(s string) (kind whagentpb.SubjectKind, ok bool) {
	switch s {
	case "human":
		return whagentpb.SubjectKind_SUBJECT_KIND_HUMAN, true
	case "service":
		return whagentpb.SubjectKind_SUBJECT_KIND_SERVICE, true
	default:
		return whagentpb.SubjectKind_SUBJECT_KIND_UNSPECIFIED, false
	}
}

// subjectToView converts s to components.SubjectView. A nil s (should be
// unreachable -- Session.subject/on_behalf_of are always populated,
// session.proto's doc comment) renders as the zero SubjectView rather
// than panicking.
func subjectToView(s *whagentpb.Subject) components.SubjectView {
	if s == nil {
		return components.SubjectView{}
	}
	return components.SubjectView{
		Iss:  s.GetIss(),
		Sub:  s.GetSub(),
		Kind: subjectKindString(s.GetKind()),
	}
}

// sessionToView converts sess to components.SessionView (FR2/FR3/NFR3).
// CapKind/ErrorCategory/ErrorDetail are populated only when the
// underlying proto3 `optional` pointer is non-nil (presence, not
// zero-value) -- mirrors get_session.go's GetSessionOutput conversion.
func sessionToView(sess *whagentpb.Session) components.SessionView {
	view := components.SessionView{
		SessionID:  sess.GetSessionId(),
		State:      sessionStateString(sess.GetState()),
		AgentID:    sess.GetAgentId(),
		Model:      sess.GetModel(),
		Subject:    subjectToView(sess.GetSubject()),
		OnBehalfOf: subjectToView(sess.GetOnBehalfOf()),
		CreatedAt:  sess.GetCreatedAt().AsTime(),
		UpdatedAt:  sess.GetUpdatedAt().AsTime(),
	}
	if sess.CapKind != nil {
		view.CapKind = capKindString(*sess.CapKind)
	}
	if sess.ErrorCategory != nil {
		view.ErrorCategory = errorCategoryString(*sess.ErrorCategory)
	}
	if sess.ErrorDetail != nil {
		view.ErrorDetail = *sess.ErrorDetail
	}
	return view
}

// usageToView converts u to components.UsageView (FR4). A nil u (should be
// unreachable -- GetSessionUsageResponse.usage is always populated,
// session.proto's doc comment) renders as the zero UsageView -- "0 / 0",
// not a panic -- rather than crashing the fragment.
func usageToView(u *whagentpb.SessionUsage) components.UsageView {
	return components.UsageView{
		TurnsUsed:     u.GetTurnsUsed(),
		TurnCap:       u.GetTurnCap(),
		CostUsedUSD:   u.GetCostUsd(),
		CostCapUSD:    u.GetCostCapUsd(),
		CostEstimated: u.GetCostEstimated(),
	}
}

// transcriptEventToView converts one whagentpb.TranscriptEvent to
// components.TranscriptEventView (FR2's exact field set: event_id, seq,
// turn, type, payload, committed_at).
func transcriptEventToView(ev *whagentpb.TranscriptEvent) components.TranscriptEventView {
	return components.TranscriptEventView{
		EventID:     ev.GetEventId(),
		Seq:         ev.GetSeq(),
		Turn:        ev.GetTurn(),
		Type:        ev.GetType(),
		Payload:     ev.GetPayload(),
		CommittedAt: ev.GetCommittedAt().AsTime(),
	}
}
