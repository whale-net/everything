// Package handlers implements whagent-net's SessionServiceServer
// (whagent_net/protos/session.proto, issue #2113). Issue #2113's
// Implementation phase filled in the two read paths -- GetSession (FR3) and
// ReadTranscript (FR2) -- against whagent_net/session.Store directly (no
// Temporal round trip, ARCHITECTURE.md "Service boundary vs. package
// boundary"). This file (session.go) owns the SessionServer type itself,
// those two read RPCs, ListSessions, and the proto-mapping helpers every
// handler file shares; the three write RPCs live in their own files per
// issue #2117's Scaffold split -- start.go (StartSession), send.go
// (SendTurn), stop.go (StopSession) -- and are real as of #2117's
// Implementation phase: each drives the SessionWorkflow #2114 built,
// through the Temporal client and model catalogue wired onto SessionServer
// (see NewSessionServer below). ListSessions stays UNIMPLEMENTED: the issue
// allows either a minimal "list the caller's own sessions" implementation
// or UNIMPLEMENTED, and session.SessionStore (#2109) exposes no
// by-subject list query -- adding one would expand scope beyond any
// named task's read/write paths, so UNIMPLEMENTED is the conservative
// choice here.
package handlers

import (
	"context"

	"github.com/whale-net/everything/whagent_net/events"
	"github.com/whale-net/everything/whagent_net/llm"
	pb "github.com/whale-net/everything/whagent_net/protos"
	"github.com/whale-net/everything/whagent_net/session"

	"github.com/google/uuid"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/logging"
	"github.com/whale-net/everything/libs/go/rmq"
	temporalclient "go.temporal.io/sdk/client"
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

// sessionWorkflowTaskQueue is the Temporal task queue StartSession starts
// (and SendTurn/StopSession signal) SessionWorkflow executions on. Must
// match whagent_net/worker/workflow.go's TaskQueue constant exactly --
// duplicated rather than imported because that constant lives in a Go
// `package main` (the worker binary), which nothing outside it can
// import; kept here as the single place `api` itself needs the name.
const sessionWorkflowTaskQueue = "whagent-net-session"

// sessionWorkflowName is the Temporal workflow type StartSession starts.
// Temporal's SDK registers a workflow function under its bare Go function
// name when no explicit name is passed to worker.Worker.RegisterWorkflow
// (go.temporal.io/sdk/internal.getFunctionName: the last "."-separated
// element of the function's fully-qualified runtime name) -- worker/
// main.go calls `w.RegisterWorkflow(SessionWorkflow)` with no override, so
// this string must track that function's name exactly. Duplicated (not
// imported) for the same `package main` reason as sessionWorkflowTaskQueue
// above.
const sessionWorkflowName = "SessionWorkflow"

// Signal name constants start.go/send.go/stop.go signal a running
// SessionWorkflow with. Must match worker/workflow.go's SignalSendTurn/
// SignalStop constants exactly -- duplicated for the same `package main`
// reason as sessionWorkflowTaskQueue above.
const (
	signalSendTurn = "SendTurn"
	signalStop     = "Stop"
)

// SessionServer implements pb.SessionServiceServer over a
// *session.Store -- the same store package `worker` will import directly
// (ARCHITECTURE.md "Service boundary vs. package boundary": no RPC hop
// between api and worker) -- plus a Temporal client (issue #2117's
// Scaffold) the write RPCs (start.go/send.go/stop.go) use to start and
// signal each session's SessionWorkflow (#2114).
type SessionServer struct {
	pb.UnimplementedSessionServiceServer

	store *session.Store
	// issuer is the Keycloak realm issuer URL every verified token's `iss`
	// claim matches (grpcauth.ServerConfig.IssuerURL in main.go) --
	// grpcauth.Claims itself carries only Subject (the `sub` claim), so
	// this is what lets callerSubject reconstruct the full (iss, sub)
	// identity pair a stored session.Subject is compared against.
	issuer string
	// temporalClient is the //libs/go/temporal-constructed client
	// (main.go) StartSession/SendTurn/StopSession drive SessionWorkflow
	// through. Unused by the two read RPCs (GetSession/ReadTranscript),
	// which read the `sessions`/`transcript_event` tables directly and
	// never round-trip through Temporal.
	temporalClient temporalclient.Client
	// taskQueue is the Temporal task queue to start/signal
	// SessionWorkflow executions on.
	taskQueue string
	// catalog is StartSession's FR5 gate: the OpenRouter model catalogue a
	// requested model_override is checked against before any session row is
	// written. nil is tolerated only by tests that never exercise
	// StartSession's override path (e.g. the read-path integration
	// coverage in session_integration_test.go) -- main.go always
	// constructs a real one.
	catalog *llm.Catalog
	// eventsConsumer is StreamEvents' (issue #2239) shared, per-process
	// subscription onto the whagent/events exchange -- declared/bound
	// once at api startup (main.go's initializeEventsConsumer), mirroring
	// tools/app_registry/ui/main.go's initializeSSEHub attach shape,
	// rather than per-stream, so no RPC call ever needs its own broker
	// credentials or connection (C17). nil when RABBITMQ_URL is unset or
	// the broker is unreachable at startup (same non-fatal-construction
	// convention as worker/main.go's initializePublisher) -- StreamEvents
	// must report UNAVAILABLE rather than block when this is nil. Unused
	// by every other RPC.
	eventsConsumer *rmq.Consumer
	// broadcaster is the in-process fan-out (broadcast.go) that turns
	// eventsConsumer's single shared subscription into however many
	// concurrent StreamEvents calls are tailing a session -- see
	// eventBroadcaster's doc comment. Wired to eventsConsumer as
	// eventsConsumer's sole handler and started, both by NewSessionServer
	// below, exactly when eventsConsumer is non-nil; nil (and unused by
	// StreamEvents, which checks eventsConsumer instead) otherwise.
	broadcaster *eventBroadcaster
}

var _ pb.SessionServiceServer = (*SessionServer)(nil)

// NewSessionServer returns a SessionServer backed by store, authenticating
// callers against issuer (see SessionServer.issuer), starting/signalling
// SessionWorkflow executions through temporalClient on taskQueue, checking
// StartSession's FR5 model_override against catalog, and serving
// StreamEvents (issue #2239) off eventsConsumer -- nil is fine (see
// SessionServer.eventsConsumer). When eventsConsumer is non-nil,
// NewSessionServer registers a fresh eventBroadcaster as its sole handler
// and starts it against ctx (rmq.Consumer.Start's doc comment: it launches
// its own background goroutine and self-heals broker-side failures
// forever, until ctx is cancelled -- so ctx here must be the server's own
// run-scoped lifetime context, main.go's, never a per-request one). If
// Start fails, eventsConsumer/broadcaster are both discarded (set nil) so
// StreamEvents reports UNAVAILABLE rather than ever trying to read from a
// consumer that never started -- the same non-fatal-construction
// convention main.go's initializeEventsConsumer already applies to every
// earlier setup failure (RABBITMQ_URL unset, connect failed, exchange
// declare failed).
//
// An empty taskQueue defaults to sessionWorkflowTaskQueue -- main.go
// passes //libs/go/temporal's Config.TaskQueue (TEMPORAL_TASK_QUEUE)
// straight through unchanged, mirroring whagent_net/worker/main.go's
// identical "env value if set, else the package's own default" fallback
// for the same setting on the other side of this same queue.
func NewSessionServer(ctx context.Context, store *session.Store, issuer string, temporalClient temporalclient.Client, taskQueue string, catalog *llm.Catalog, eventsConsumer *rmq.Consumer) *SessionServer {
	if taskQueue == "" {
		taskQueue = sessionWorkflowTaskQueue
	}

	var broadcaster *eventBroadcaster
	if eventsConsumer != nil {
		broadcaster = newEventBroadcaster()
		eventsConsumer.RegisterHandler("#", broadcaster.HandleMessage)
		if err := eventsConsumer.Start(ctx); err != nil {
			logging.Get("streamevents").Warn("failed to start events consumer; StreamEvents will be unavailable", "error", err)
			eventsConsumer = nil
			broadcaster = nil
		}
	}

	return &SessionServer{
		store:          store,
		issuer:         issuer,
		temporalClient: temporalClient,
		taskQueue:      taskQueue,
		catalog:        catalog,
		eventsConsumer: eventsConsumer,
		broadcaster:    broadcaster,
	}
}

// GetSession is a read path (FR3): reads the `sessions` row
// (session.Store.Sessions().GetByID) and maps it to a Session proto --
// NOT_FOUND for an unknown session id. Any authenticated caller may read
// any session, whoever started or controls it (FR2/C14: on-call viewers are
// the point) -- there is no ownership check here, only the
// RequireClaimsUnaryInterceptor-enforced requirement that the caller be
// authenticated at all. See canControl for the (unrelated, stricter) rule
// that gates SendTurn/StopSession.
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

	// Read access is unconditional for any authenticated caller (FR2/C14) --
	// callerSubject is not even consulted here. RequireClaimsUnaryInterceptor
	// already rejected any call with no claims before this handler ran, so
	// there is no anonymous read path despite the absence of an explicit
	// check.
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

// ReadTranscript is a read path (FR2): pages through
// session.Store.Transcript().Read in commit order (seq), mapping each
// Event to a TranscriptEvent proto. Works identically for a running or
// ended session -- Read has no status filter, it just returns whatever has
// been committed so far. NOT_FOUND follows the same rule as GetSession; so
// does the absence of any ownership check -- any authenticated caller may
// read any session's transcript (FR2/C14), see GetSession's doc comment.
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

	limit := int(req.GetLimit())
	switch {
	case limit <= 0:
		limit = defaultTranscriptLimit
	case limit > maxTranscriptLimit:
		limit = maxTranscriptLimit
	}

	transcriptEvents, err := s.store.Transcript().Read(ctx, id, req.GetFromSeq(), limit)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read transcript: %v", err)
	}

	// next_from_seq resumes exactly after the last event returned -- no
	// gap, no duplicate (FR2). When nothing new was read, it echoes the
	// request's from_seq back unchanged (nothing to resume past yet).
	nextFromSeq := req.GetFromSeq()
	pbEvents := make([]*pb.TranscriptEvent, len(transcriptEvents))
	for i, ev := range transcriptEvents {
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

// canControl reports whether caller may send turns to or stop sess (FR1/
// C13). Control is scoped to sess.OnBehalfOf, never sess.Subject: a caller
// controls a session when it is (or is acting for) the on-behalf-of
// subject the session runs as, regardless of which identity actually
// started it -- e.g. a service account starting a session on a user's
// behalf records that user as OnBehalfOf, and it is the user, not the
// service account's own identity, who can subsequently send turns or stop
// it. Reads have no such check at all (see GetSession/ReadTranscript) --
// this is deliberately the only place a control decision is made, so no
// later UI/MCP task re-derives it. There is no admin override in M1: a
// caller who is neither the session's on-behalf-of subject nor started it
// simply cannot control it.
func canControl(sess *session.Session, caller session.Subject) bool {
	return subjectsEqual(sess.OnBehalfOf, caller)
}

// hasRole reports whether required is present in roles (FR9's role check:
// StartSession requires the caller's Keycloak realm/client roles --
// grpcauth.Claims.Roles -- to include an agent definition's required_role
// verbatim; there is no whagent-side ACL table or role hierarchy, LB5).
func hasRole(roles []string, required string) bool {
	for _, r := range roles {
		if r == required {
			return true
		}
	}
	return false
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
func transcriptEventToProto(ev events.Event) *pb.TranscriptEvent {
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
