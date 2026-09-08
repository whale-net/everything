// StartSession (issue #2117's write path, FR1/FR5/FR9/NFR3). Every failure
// below is fail-closed: no `sessions` row, no `session_agent` row, and no
// Temporal workflow are created (issue body's ordered StartSession steps).
package handlers

import (
	"context"

	"github.com/google/uuid"

	"github.com/whale-net/everything/libs/go/grpcauth"
	pb "github.com/whale-net/everything/whagent_net/protos"
	"github.com/whale-net/everything/whagent_net/session"

	temporalclient "go.temporal.io/sdk/client"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// StartSession creates a new session and starts its SessionWorkflow, in the
// order the issue body fixes so every failure below stays fail-closed:
//
//  1. authenticate the caller (already done by the interceptor chain by the
//     time this handler runs; callerSubject reconstructs the identity)
//  2. resolve the named agent's latest definition version
//  3. FR9 -- the definition's required_role, if any, gates the call
//  4. FR5 -- a requested model_override is checked against the provider
//     catalogue
//  5. insert the `sessions` row
//  6. write the SCD2 `session_agent` assignment row
//  7. start the Temporal workflow, workflow ID == session ID (LB2)
//
// Caps (max_turns_override/max_cost_usd_override) are agent-definition-level
// only in M1 -- a request carrying either is rejected outright (InvalidArgument),
// never silently ignored, before any of the steps above run.
func (s *SessionServer) StartSession(ctx context.Context, req *pb.StartSessionRequest) (*pb.StartSessionResponse, error) {
	agentID := req.GetAgentId()
	if agentID == "" {
		return nil, status.Error(codes.InvalidArgument, "agent_id is required")
	}
	if req.MaxTurnsOverride != nil {
		return nil, status.Error(codes.InvalidArgument, "max_turns_override is not supported in M1: caps are agent-definition-level only")
	}
	if req.MaxCostUsdOverride != nil {
		return nil, status.Error(codes.InvalidArgument, "max_cost_usd_override is not supported in M1: caps are agent-definition-level only")
	}

	// Step 1: authenticate the caller. RequireClaimsUnaryInterceptor
	// (auth.go) already rejected any call with no verified claims before
	// this handler ran; callerSubject just reconstructs the (iss, sub,
	// kind) triple. M1 has no delegated-caller path (LB2/NFR3): the
	// request's optional on_behalf_of field, and parent_session_id, are
	// both reserved for M2/M3 and are not read here -- every M1 session is
	// written with on_behalf_of_* identical to subject_* and
	// parent_session_id NULL, regardless of what the request carries.
	caller, err := s.callerSubject(ctx)
	if err != nil {
		return nil, err
	}

	// Step 2: resolve the named agent's current (latest) definition.
	def, err := s.store.AgentDefinitions().GetLatest(ctx, agentID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get agent definition: %v", err)
	}
	if def == nil {
		return nil, status.Errorf(codes.NotFound, "agent %q not found", agentID)
	}

	// Step 3 (FR9): a definition with no required_role is runnable by any
	// authenticated caller. A definition that names one requires the
	// caller's Keycloak realm/client roles to include it verbatim -- no
	// whagent-side ACL table, no role hierarchy (LB5).
	if def.RequiredRole != nil {
		claims, ok := grpcauth.ClaimsFromContext(ctx)
		if !ok {
			// Unreachable in practice -- see callerSubject's identical guard
			// below for why this is checked anyway rather than assumed.
			return nil, status.Error(codes.Unauthenticated, "authentication required")
		}
		if !hasRole(claims.Roles, *def.RequiredRole) {
			return nil, status.Errorf(codes.PermissionDenied, "caller lacks required role %q for agent %q", *def.RequiredRole, agentID)
		}
	}

	// Step 4 (FR5): an operator-specified model overrides the definition's
	// default for this session only -- the agent definition row itself is
	// never mutated. A model the configured provider does not serve fails
	// here, before any session row exists, mirroring FR9's fail-closed
	// shape -- it must never surface later as a first-turn LLM error.
	resolvedModel := def.Model
	var modelOverride *string
	if req.ModelOverride != nil {
		requested := req.GetModelOverride()
		if s.catalog == nil {
			return nil, status.Error(codes.Internal, "model catalogue is not configured")
		}
		// Supports (whagent_net/llm/catalog.go) fails closed itself: a
		// catalogue fetch error is never treated as "supported", so this
		// Internal branch is reachable (e.g. OpenRouter unavailable) and is
		// distinct from -- never conflated with -- the FailedPrecondition
		// below for a catalogue that answered "no".
		supported, err := s.catalog.Supports(ctx, requested)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "check model catalogue: %v", err)
		}
		if !supported {
			return nil, status.Errorf(codes.FailedPrecondition, "model %q is not served by the configured provider", requested)
		}
		resolvedModel = requested
		modelOverride = &requested
	}

	// Step 5: insert the `sessions` row. subject_*/on_behalf_of_* are
	// identical (NFR3's M1 default); parent_session_id is always NULL in
	// M1.
	sessionID := uuid.New()
	sess := &session.Session{
		SessionID:     sessionID,
		Subject:       caller,
		OnBehalfOf:    caller,
		AgentID:       agentID,
		Model:         resolvedModel,
		ModelOverride: modelOverride,
		Status:        session.StatusRunning,
	}
	if err := s.store.Sessions().Create(ctx, sess); err != nil {
		return nil, status.Errorf(codes.Internal, "create session: %v", err)
	}

	// Step 6 (LB5/NFR6): pin the resolved definition version. AssignToSession
	// always opens a fresh row on a brand-new session (there is nothing to
	// close yet), so exactly one `session_agent` row with valid_to IS NULL
	// exists afterwards.
	if err := s.store.AgentDefinitions().AssignToSession(ctx, sessionID, agentID, def.Version); err != nil {
		return nil, status.Errorf(codes.Internal, "assign agent definition: %v", err)
	}

	// Step 7 (LB2): start the workflow with workflow ID == session ID.
	if _, err := s.temporalClient.ExecuteWorkflow(ctx, temporalclient.StartWorkflowOptions{
		ID:        sessionID.String(),
		TaskQueue: s.taskQueue,
	}, sessionWorkflowName, sessionWorkflowInput{SessionID: sessionID}); err != nil {
		return nil, status.Errorf(codes.Internal, "start session workflow: %v", err)
	}

	return &pb.StartSessionResponse{Session: sessionToProto(sess)}, nil
}

// sessionWorkflowInput mirrors worker.SessionWorkflowInput
// (whagent_net/worker/workflow.go) exactly -- duplicated here for the same
// `package main` reason as sessionWorkflowName (session.go): worker is a Go
// binary's package main and cannot be imported. Temporal's default JSON
// data converter serializes by field name, so the two types must keep
// identical exported field names, not identical Go types.
type sessionWorkflowInput struct {
	SessionID uuid.UUID
}
