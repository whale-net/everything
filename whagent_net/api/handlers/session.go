// Package handlers implements whagent-net's SessionServiceServer
// (whagent_net/protos/session.proto, issue #2113). Scaffold phase wired up
// every RPC through the authenticated gRPC server (whagent_net/api/main.go,
// auth.go) with every method returning codes.Unimplemented. Implementation
// phase fills in this task's two read paths -- GetSession (FR3) and
// ReadTranscript (FR2) -- against whagent_net/session.Store directly (no
// Temporal round trip, ARCHITECTURE.md "Service boundary vs. package
// boundary"). The three write RPCs (StartSession/SendTurn/StopSession) stay
// UNIMPLEMENTED until the follow-up task that builds the SessionWorkflow
// lands (#2117). ListSessions also stays UNIMPLEMENTED: the issue allows
// either a minimal "list the caller's own sessions" implementation or
// UNIMPLEMENTED, and session.SessionStore (#2109) exposes no by-subject
// list query -- adding one would expand this task's scope beyond the two
// named read paths, so UNIMPLEMENTED is the conservative choice here.
package handlers

import (
	"context"

	pb "github.com/whale-net/everything/whagent_net/protos"
	"github.com/whale-net/everything/whagent_net/session"

	"github.com/google/uuid"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// defaultTranscriptLimit and maxTranscriptLimit bound ReadTranscript's
// page size (FR2's pagination): 0 in the request means "server default",
// and no caller can force an unbounded single-page read.
const (
	defaultTranscriptLimit = 100
	maxTranscriptLimit     = 500
)

// SessionServer implements pb.SessionServiceServer over a
// *session.Store -- the same store package `worker` will import directly
// (ARCHITECTURE.md "Service boundary vs. package boundary": no RPC hop
// between api and worker).
type SessionServer struct {
	pb.UnimplementedSessionServiceServer

	store *session.Store
	// issuer is the Keycloak realm issuer URL every verified token's `iss`
	// claim matches (grpcauth.ServerConfig.IssuerURL in main.go) --
	// grpcauth.Claims itself carries only Subject (the `sub` claim), so
	// this is what lets callerSubject reconstruct the full (iss, sub)
	// identity pair a stored session.Subject is compared against.
	issuer string
}

var _ pb.SessionServiceServer = (*SessionServer)(nil)

// NewSessionServer returns a SessionServer backed by store, authenticating
// callers against issuer (see SessionServer.issuer).
func NewSessionServer(store *session.Store, issuer string) *SessionServer {
	return &SessionServer{store: store, issuer: issuer}
}

// StartSession is UNIMPLEMENTED until the SessionWorkflow exists (follow-up
// task to #2113 per whagent_net/ARCHITECTURE.md "Session workflow").
func (s *SessionServer) StartSession(ctx context.Context, req *pb.StartSessionRequest) (*pb.StartSessionResponse, error) {
	return nil, status.Error(codes.Unimplemented, "StartSession: session workflow not yet implemented")
}

// SendTurn is UNIMPLEMENTED until the SessionWorkflow exists.
func (s *SessionServer) SendTurn(ctx context.Context, req *pb.SendTurnRequest) (*pb.SendTurnResponse, error) {
	return nil, status.Error(codes.Unimplemented, "SendTurn: session workflow not yet implemented")
}

// StopSession is UNIMPLEMENTED until the SessionWorkflow exists.
func (s *SessionServer) StopSession(ctx context.Context, req *pb.StopSessionRequest) (*pb.StopSessionResponse, error) {
	return nil, status.Error(codes.Unimplemented, "StopSession: session workflow not yet implemented")
}

// GetSession is this issue's Implementation-phase read path (FR3): reads
// the `sessions` row (session.Store.Sessions().GetByID) and maps it to a
// Session proto -- NOT_FOUND for an unknown session id, PERMISSION_DENIED
// for a session belonging to a different subject than the caller.
func (s *SessionServer) GetSession(ctx context.Context, req *pb.GetSessionRequest) (*pb.GetSessionResponse, error) {
	id, err := uuid.Parse(req.GetSessionId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid session_id: %v", err)
	}

	sess, err := s.store.Sessions().GetByID(ctx, id)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get session: %v", err)
	}
	if sess == nil {
		return nil, status.Error(codes.NotFound, "session not found")
	}

	caller, err := s.callerSubject(ctx)
	if err != nil {
		return nil, err
	}
	if !subjectsEqual(sess.Subject, caller) {
		return nil, status.Error(codes.PermissionDenied, "session belongs to a different subject")
	}

	return &pb.GetSessionResponse{Session: sessionToProto(sess)}, nil
}

// ListSessions may be minimal in M1 per the issue body -- either list the
// caller's own sessions or stay UNIMPLEMENTED. session.SessionStore
// (#2109) has no by-subject list query, so implementing "the caller's own
// sessions" here would mean adding one -- out of scope for this task's two
// named read paths (GetSession, ReadTranscript). Stays UNIMPLEMENTED.
func (s *SessionServer) ListSessions(ctx context.Context, req *pb.ListSessionsRequest) (*pb.ListSessionsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ListSessions: not implemented in M1")
}

// ReadTranscript is this issue's Implementation-phase read path (FR2):
// pages through session.Store.Transcript().Read in commit order (seq),
// mapping each Event to a TranscriptEvent proto. Works identically for a
// running or ended session -- Read has no status filter, it just returns
// whatever has been committed so far. NOT_FOUND/PERMISSION_DENIED follow
// the same rules as GetSession.
func (s *SessionServer) ReadTranscript(ctx context.Context, req *pb.ReadTranscriptRequest) (*pb.ReadTranscriptResponse, error) {
	id, err := uuid.Parse(req.GetSessionId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid session_id: %v", err)
	}

	sess, err := s.store.Sessions().GetByID(ctx, id)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get session: %v", err)
	}
	if sess == nil {
		return nil, status.Error(codes.NotFound, "session not found")
	}

	caller, err := s.callerSubject(ctx)
	if err != nil {
		return nil, err
	}
	if !subjectsEqual(sess.Subject, caller) {
		return nil, status.Error(codes.PermissionDenied, "session belongs to a different subject")
	}

	limit := int(req.GetLimit())
	switch {
	case limit <= 0:
		limit = defaultTranscriptLimit
	case limit > maxTranscriptLimit:
		limit = maxTranscriptLimit
	}

	events, err := s.store.Transcript().Read(ctx, id, req.GetFromSeq(), limit)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read transcript: %v", err)
	}

	// next_from_seq resumes exactly after the last event returned -- no
	// gap, no duplicate (FR2). When nothing new was read, it echoes the
	// request's from_seq back unchanged (nothing to resume past yet).
	nextFromSeq := req.GetFromSeq()
	pbEvents := make([]*pb.TranscriptEvent, len(events))
	for i, ev := range events {
		pbEvents[i] = transcriptEventToProto(ev)
		if ev.Seq >= nextFromSeq {
			nextFromSeq = ev.Seq + 1
		}
	}

	return &pb.ReadTranscriptResponse{
		Events:      pbEvents,
		NextFromSeq: nextFromSeq,
	}, nil
}

// callerSubject reconstructs the authenticated caller's session.Subject
// from the grpcauth.Claims RequireClaimsUnaryInterceptor already
// guarantees are present by the time any handler runs. Kind defaults to
// SubjectKindHuman: grpcauth.Claims carries no field distinguishing a
// human caller from a service account yet (same M1 gap the issue's
// Authentication section notes for on_behalf_of).
func (s *SessionServer) callerSubject(ctx context.Context) (session.Subject, error) {
	claims, ok := grpcauth.ClaimsFromContext(ctx)
	if !ok {
		// Unreachable in practice: RequireClaimsUnaryInterceptor/
		// RequireClaimsStreamInterceptor (auth.go) already reject any call
		// with no claims before a handler runs. Guarded here so this
		// method never silently treats a missing-claims bug as "caller has
		// no subject" and leaks a PERMISSION_DENIED that looks like an
		// ownership check instead of an auth wiring bug.
		return session.Subject{}, status.Error(codes.Unauthenticated, "authentication required")
	}
	return session.Subject{
		Iss:  s.issuer,
		Sub:  claims.Subject,
		Kind: session.SubjectKindHuman,
	}, nil
}

// subjectsEqual compares identity only -- (iss, sub) -- not Kind, which is
// metadata about the identity rather than part of it.
func subjectsEqual(a, b session.Subject) bool {
	return a.Iss == b.Iss && a.Sub == b.Sub
}

// sessionToProto maps a `sessions` row to its wire shape (FR3), translating
// Status/CapKind/ErrorCategory to their proto enums. The terminal-reason
// fields (CapKind/ErrorCategory/ErrorDetail) are left unset on the proto
// whenever they're nil on sess -- never coerced to a zero-value enum or
// empty string -- so a caller's has_cap_kind/has_error_category check is
// meaningful (issue body, "Session" message doc).
func sessionToProto(sess *session.Session) *pb.Session {
	out := &pb.Session{
		SessionId:  sess.SessionID.String(),
		State:      statusToProto(sess.Status),
		AgentId:    sess.AgentID,
		Model:      sess.Model,
		Subject:    subjectToProto(sess.Subject),
		OnBehalfOf: subjectToProto(sess.OnBehalfOf),
		CreatedAt:  timestamppb.New(sess.CreatedAt),
		UpdatedAt:  timestamppb.New(sess.UpdatedAt),
	}
	if sess.ParentSessionID != nil {
		out.ParentSessionId = sess.ParentSessionID.String()
	}
	if sess.CapKind != nil {
		ck := capKindToProto(*sess.CapKind)
		out.CapKind = &ck
	}
	if sess.ErrorCategory != nil {
		ec := errorCategoryToProto(*sess.ErrorCategory)
		out.ErrorCategory = &ec
	}
	if sess.ErrorDetail != nil {
		out.ErrorDetail = sess.ErrorDetail
	}
	return out
}

// transcriptEventToProto maps a `transcript_event` row to its wire shape
// (FR2). Payload is carried verbatim -- a tool-result event's own
// domain-server `isError` flag lives inside this JSON body unchanged, so
// FR2's distinction (a domain-server error is an ordinary tool-result
// event, never the session's own failure event) falls out of `type` never
// being rewritten here, not out of any special-casing in this mapping.
func transcriptEventToProto(ev session.Event) *pb.TranscriptEvent {
	return &pb.TranscriptEvent{
		EventId:     ev.EventID.String(),
		Seq:         ev.Seq,
		Turn:        int32(ev.Turn),
		Type:        ev.Type,
		Payload:     []byte(ev.Payload),
		CommittedAt: timestamppb.New(ev.CommittedAt),
	}
}

func subjectToProto(sub session.Subject) *pb.Subject {
	return &pb.Subject{
		Iss:  sub.Iss,
		Sub:  sub.Sub,
		Kind: subjectKindToProto(sub.Kind),
	}
}

func statusToProto(st session.Status) pb.SessionState {
	switch st {
	case session.StatusRunning:
		return pb.SessionState_SESSION_STATE_RUNNING
	case session.StatusAwaitingInput:
		return pb.SessionState_SESSION_STATE_AWAITING_INPUT
	case session.StatusDone:
		return pb.SessionState_SESSION_STATE_DONE
	case session.StatusStopped:
		return pb.SessionState_SESSION_STATE_STOPPED
	case session.StatusFailed:
		return pb.SessionState_SESSION_STATE_FAILED
	case session.StatusCapped:
		return pb.SessionState_SESSION_STATE_CAPPED
	default:
		return pb.SessionState_SESSION_STATE_UNSPECIFIED
	}
}

func capKindToProto(ck session.CapKind) pb.CapKind {
	switch ck {
	case session.CapKindTurns:
		return pb.CapKind_CAP_KIND_TURNS
	case session.CapKindCost:
		return pb.CapKind_CAP_KIND_COST
	default:
		return pb.CapKind_CAP_KIND_UNSPECIFIED
	}
}

func errorCategoryToProto(ec session.ErrorCategory) pb.ErrorCategory {
	switch ec {
	case session.ErrorCategoryRetryable:
		return pb.ErrorCategory_ERROR_CATEGORY_RETRYABLE
	case session.ErrorCategoryNonRetryable:
		return pb.ErrorCategory_ERROR_CATEGORY_NON_RETRYABLE
	default:
		return pb.ErrorCategory_ERROR_CATEGORY_UNSPECIFIED
	}
}

func subjectKindToProto(k session.SubjectKind) pb.SubjectKind {
	switch k {
	case session.SubjectKindHuman:
		return pb.SubjectKind_SUBJECT_KIND_HUMAN
	case session.SubjectKindService:
		return pb.SubjectKind_SUBJECT_KIND_SERVICE
	default:
		return pb.SubjectKind_SUBJECT_KIND_UNSPECIFIED
	}
}
