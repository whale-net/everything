package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/whale-net/everything/libs/go/htmxauth"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/ui/components"
)

// This file covers #652 (NFR-17, FR-42-46, FR-59) at the handler layer:
// FR-45's PermissionDenied-vs-transport-failure message and NFR-14's
// "role gating is presentation-only" guarantee. The role-subset rendering
// assertions (no-roles / promoter-dev-only / admin-only / all-roles, FR-43's
// per-environment derivation, and FR-59's absent-vs-empty banner) are
// covered directly against the templ page components in
// pages/role_gating_test.go and the primitives in
// components/roles_test.go + components/banner_test.go -- htmxauth's
// context key that carries *UserInfo is package-private, so a handler test
// cannot inject an arbitrary role set into a request's context without
// going through a real OIDC session or the DB-backed session manager;
// rendering the pages directly (as those two files do) exercises the exact
// same role-branching code with full control over Roles.

// --- FR-45: PermissionDenied distinguished from a transport/server failure -

func TestGrpcErrorMessage_DistinguishesPermissionDeniedFromOtherFailures(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		want    []string // substrings that must all appear
		mustNot string   // substring that must NOT appear (empty = skip)
	}{
		{
			name:    "PermissionDenied names a missing role, not a generic failure",
			err:     status.Error(codes.PermissionDenied, "missing role app-registry-promoter-prod"),
			want:    []string{"Denied", "lacks the required role", "app-registry-promoter-prod"},
			mustNot: "Transport or server failure",
		},
		{
			name:    "Unavailable (transport/server) is labelled as such, not as a role denial",
			err:     status.Error(codes.Unavailable, "connection refused"),
			want:    []string{"Transport or server failure"},
			mustNot: "lacks the required role",
		},
		{
			name: "a bare, non-gRPC-status error is also labelled a transport failure",
			err:  context.DeadlineExceeded,
			want: []string{"Transport or server failure"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := grpcErrorMessage(c.err)
			for _, want := range c.want {
				if !strings.Contains(got, want) {
					t.Errorf("grpcErrorMessage(%v) = %q, want it to contain %q", c.err, got, want)
				}
			}
			if c.mustNot != "" && strings.Contains(got, c.mustNot) {
				t.Errorf("grpcErrorMessage(%v) = %q, must not contain %q", c.err, got, c.mustNot)
			}
		})
	}
}

// The two messages above must themselves be textually distinguishable from
// one another -- FR-45's actual requirement, not just that each contains
// its own expected substring.
func TestGrpcErrorMessage_PermissionDeniedAndTransportFailureAreDistinguishable(t *testing.T) {
	denied := grpcErrorMessage(status.Error(codes.PermissionDenied, "nope"))
	transport := grpcErrorMessage(status.Error(codes.Unavailable, "nope"))
	if denied == transport {
		t.Fatalf("PermissionDenied and Unavailable produced identical messages: %q", denied)
	}
}

// --- NFR-14: role gating is presentation only -------------------------------
//
// Every fake below is called with NO user in the request context (the
// default state of a bare httptest.NewRequest, equivalent to the "no
// roles" -- indeed "no session at all" -- principal) specifically because
// the handlers under test in this section must reach the RPC regardless of
// what the caller holds; if they gated on role at all, using no user is the
// strictest possible test of that.

type nfr14EnvClient struct {
	pb.EnvironmentRegistryClient

	upsertCalls  int
	archiveCalls int
}

func (f *nfr14EnvClient) GetEnvironment(ctx context.Context, in *pb.GetEnvironmentRequest, opts ...grpc.CallOption) (*pb.GetEnvironmentResponse, error) {
	return &pb.GetEnvironmentResponse{Environment: &pb.Environment{Key: in.GetKey(), Rank: 5}}, nil
}

func (f *nfr14EnvClient) UpsertEnvironment(ctx context.Context, in *pb.UpsertEnvironmentRequest, opts ...grpc.CallOption) (*pb.UpsertEnvironmentResponse, error) {
	f.upsertCalls++
	return &pb.UpsertEnvironmentResponse{Environment: &pb.Environment{Key: in.GetKey()}}, nil
}

func (f *nfr14EnvClient) ArchiveEnvironment(ctx context.Context, in *pb.ArchiveEnvironmentRequest, opts ...grpc.CallOption) (*pb.ArchiveEnvironmentResponse, error) {
	f.archiveCalls++
	return &pb.ArchiveEnvironmentResponse{}, nil
}

// handleEnvironmentSave/Archive never inspect the caller's roles at all
// (see handlers_environments.go) -- the FR-38 admin gate lives entirely in
// the template (disabled Save/Archive buttons); the handler itself always
// forwards to UpsertEnvironment/ArchiveEnvironment and trusts
// app-registry-api's own auth.RequireAdmin to reject an unauthorized
// caller. A caller who bypasses the disabled button (e.g. curl'ing the
// form action directly) must still reach the server, not be silently
// dropped by the UI.
func TestHandleEnvironmentSave_ReachesAPIWithNoUserInContext(t *testing.T) {
	env := &nfr14EnvClient{}
	app := &App{registry: &RegistryClient{Environment: env}}

	form := url.Values{"key": {"dev"}, "display_name": {"Dev"}, "rank": {"0"}, "gitops_path": {"envs/dev"}}
	req := httptest.NewRequest(http.MethodPost, "/environments/save", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	app.handleEnvironmentSave(w, req)

	if env.upsertCalls != 1 {
		t.Errorf("UpsertEnvironment calls = %d, want 1 -- NFR-14: the UI must never short-circuit a write before it reaches the API, regardless of what role (if any) the caller holds", env.upsertCalls)
	}
}

func TestHandleEnvironmentArchive_ReachesAPIWithNoUserInContext(t *testing.T) {
	env := &nfr14EnvClient{}
	app := &App{registry: &RegistryClient{Environment: env}}

	form := url.Values{"key": {"dev"}, "reason": {"decommissioned"}}
	req := httptest.NewRequest(http.MethodPost, "/environments/archive", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	app.handleEnvironmentArchive(w, req)

	if env.archiveCalls != 1 {
		t.Errorf("ArchiveEnvironment calls = %d, want 1 -- NFR-14: must reach the API regardless of caller role", env.archiveCalls)
	}
}

type nfr14PromoteEnvClient struct {
	pb.EnvironmentRegistryClient
}

func (f *nfr14PromoteEnvClient) GetEnvironment(ctx context.Context, in *pb.GetEnvironmentRequest, opts ...grpc.CallOption) (*pb.GetEnvironmentResponse, error) {
	return &pb.GetEnvironmentResponse{Environment: &pb.Environment{Key: in.GetKey(), Rank: 0}}, nil
}

type nfr14PromoteArtifactClient struct {
	pb.ArtifactRegistryClient
}

func (f *nfr14PromoteArtifactClient) ListArtifacts(ctx context.Context, in *pb.ListArtifactsRequest, opts ...grpc.CallOption) (*pb.ListArtifactsResponse, error) {
	if in.GetPromotableOnly() {
		return &pb.ListArtifactsResponse{Artifacts: []*pb.Artifact{{ArtifactId: "a1"}}}, nil
	}
	return &pb.ListArtifactsResponse{}, nil
}

type nfr14PromotionClient struct {
	pb.PromotionRegistryClient

	promoteCalls  int
	rollbackCalls int
}

func (f *nfr14PromotionClient) Promote(ctx context.Context, in *pb.PromoteRequest, opts ...grpc.CallOption) (*pb.PromoteResponse, error) {
	f.promoteCalls++
	return &pb.PromoteResponse{Promotion: &pb.Promotion{PromotionId: "p1"}}, nil
}

func (f *nfr14PromotionClient) Rollback(ctx context.Context, in *pb.RollbackRequest, opts ...grpc.CallOption) (*pb.RollbackResponse, error) {
	f.rollbackCalls++
	return &pb.RollbackResponse{Promotion: &pb.Promotion{PromotionId: "p1"}}, nil
}

// handlePromoteSubmit (handlers_promote.go) never inspects the caller's
// role at all -- confirmed by reading the file: reason/override/dry-run
// UI-layer policy checks only, then straight to Promotion.Promote. It must
// reach the API even for a caller holding no role/session whatsoever.
func TestHandlePromoteSubmit_Commit_ReachesAPIWithNoUserInContext(t *testing.T) {
	promo := &nfr14PromotionClient{}
	app := &App{registry: &RegistryClient{
		Environment: &nfr14PromoteEnvClient{},
		Artifact:    &nfr14PromoteArtifactClient{},
		Promotion:   promo,
	}}

	form := url.Values{
		"owner": {"platform-worker"}, "kind": {"image"}, "env": {"dev"},
		"action": {"commit"}, "artifact_id": {"a1"}, "reason": {"ship it"},
	}
	req := httptest.NewRequest(http.MethodPost, "/promote", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	app.handlePromote(w, req)

	if promo.promoteCalls != 1 {
		t.Fatalf("Promote calls = %d, want 1 -- NFR-14: promote must reach the API regardless of the caller's role; body = %s", promo.promoteCalls, w.Body.String())
	}
}

// By contrast, handleRollback DOES pre-check components.GateCellAction and
// skips the Rollback RPC entirely when denied (handlers_rollback.go's own
// doc comment: "Check client-side first ... but the server's own
// auth.RequirePromoter ... is what actually makes this true, not this check
// alone"). This is documented, deliberate behaviour from the rollback
// screen's own implementation issue, not an oversight introduced here --
// but it is worth naming explicitly since it reads as a narrower
// application of NFR-14's "presentation only" principle than Promote's: the
// UI still never determines who is authorized (the server does, for any
// caller who bypasses this pre-check), but it does skip the round trip
// entirely for a caller it already knows will be denied, which is not
// "every write path still submits to the API". Recorded here rather than
// silently left untested, per the close comment on #652.
func TestHandleRollbackShow_DoesNotReachAPIWhenClientSideGateDenies(t *testing.T) {
	promo := &nfr14PromotionClient{}
	app := &App{registry: &RegistryClient{
		Environment: &nfr14PromoteEnvClient{},
		Promotion:   promo,
	}}

	req := httptest.NewRequest(http.MethodGet, "/rollback?owner=platform-worker&kind=image&env=dev", nil)
	w := httptest.NewRecorder()
	app.handleRollback(w, req)

	if promo.rollbackCalls != 0 {
		t.Errorf("Rollback calls = %d, want 0 -- handleRollbackShow's own doc comment says it pre-checks the role and skips the RPC when denied", promo.rollbackCalls)
	}
	if !strings.Contains(w.Body.String(), "Requires role") {
		t.Errorf("expected the denied state to name the missing role; body = %s", w.Body.String())
	}
}

// --- issue #890 FR4/NFR5: release trigger requires the same permission as
// promote ---------------------------------------------------------------
//
// The submit/show gate-denial behaviour itself (a denied caller never
// reaches TriggerRelease, both GET and POST) is covered in
// handlers_release_test.go (mirroring
// TestHandleRollbackShow_DoesNotReachAPIWhenClientSideGateDenies above --
// same pattern, kept in that file alongside the rest of the release-trigger
// handler tests rather than duplicated here). What belongs here is the
// role-identity assertion: releaseTriggerGate (handlers_release.go) must
// evaluate to the *exact* same GateDecision promote.templ's own gate line
// (`components.GateCellAction(user, components.EnvironmentPromoterRole(s.EnvKey), false)`)
// would produce for the "dev" environment -- i.e. release trigger reuses
// promote's gate function and role-naming scheme verbatim, not a
// parallel/independent check that merely happens to behave similarly today.
func TestReleaseTriggerGate_MatchesPromoteScreensGateForDevEnvironment(t *testing.T) {
	promoteGateForDev := func(user *htmxauth.UserInfo) components.GateDecision {
		return components.GateCellAction(user, components.EnvironmentPromoterRole("dev"), false)
	}

	cases := []struct {
		name string
		user *htmxauth.UserInfo
	}{
		{"no user", nil},
		{"user with no roles", &htmxauth.UserInfo{Roles: []string{}}},
		{"user missing the dev promoter role", &htmxauth.UserInfo{Roles: []string{"app-registry-promoter-prod"}}},
		{"user holding the dev promoter role", &htmxauth.UserInfo{Roles: []string{"app-registry-promoter-dev"}}},
		{"dev sentinel (AuthModeNone)", &htmxauth.UserInfo{Roles: htmxauth.AllRoles}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := releaseTriggerGate(c.user)
			want := promoteGateForDev(c.user)
			if got != want {
				t.Errorf("releaseTriggerGate(%+v) = %+v, want %+v (must match promote's own gate for the dev environment)", c.user, got, want)
			}
		})
	}
}
