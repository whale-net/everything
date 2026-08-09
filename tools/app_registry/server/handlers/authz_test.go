package handlers

import (
	"context"
	"testing"

	"github.com/whale-net/everything/libs/go/grpcauth"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/server/auth"
	"github.com/whale-net/everything/tools/app_registry/server/repository/fake"
	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// authedCtx returns a context authenticated as a principal holding every
// app-registry role. The handler-behaviour tests in app_test.go and
// artifact_test.go are not about authorization — they use this so business
// logic keeps being exercised the same way it was before auth existed. The
// authorization boundary itself is asserted by the tests in this file.
func authedCtx() context.Context {
	return ctxWithRoles(auth.AllRoles()...)
}

func ctxWithRoles(roles ...string) context.Context {
	return grpcauth.ContextWithClaims(context.Background(), &grpcauth.Claims{
		Subject: "test-user",
		Roles:   roles,
	})
}

func requireCode(t *testing.T, err error, want codes.Code, rpc string) {
	t.Helper()
	if status.Code(err) != want {
		t.Fatalf("%s: expected %v, got %v", rpc, want, err)
	}
}

// TestReconcileApps_Authorization covers AppRegistry.ReconcileApps, which
// requires RoleBuilder per ARCHITECTURE.md's Authorization table.
func TestReconcileApps_Authorization(t *testing.T) {
	req := &pb.ReconcileAppsRequest{
		Manifests:      manifestSet([]*appmetapb.AppManifest{oneApp("demo", "svc")}, nil),
		IdempotencyKey: "authz-reconcile",
	}

	t.Run("correct role allowed", func(t *testing.T) {
		srv := NewAppServer(fake.New())
		_, err := srv.ReconcileApps(ctxWithRoles(auth.RoleBuilder), req)
		if err != nil {
			t.Fatalf("expected builder to be allowed, got %v", err)
		}
	})

	t.Run("wrong role is PermissionDenied", func(t *testing.T) {
		srv := NewAppServer(fake.New())
		_, err := srv.ReconcileApps(ctxWithRoles(auth.RoleAdmin), req)
		requireCode(t, err, codes.PermissionDenied, "ReconcileApps")
	})

	t.Run("no claims is Unauthenticated", func(t *testing.T) {
		srv := NewAppServer(fake.New())
		_, err := srv.ReconcileApps(context.Background(), req)
		requireCode(t, err, codes.Unauthenticated, "ReconcileApps")
	})
}

// TestSetAppStatus_Authorization covers AppRegistry.SetAppStatus, which
// requires RoleAdmin. Builder does not imply admin — see auth.Require's doc
// comment on the flat-roles decision.
func TestSetAppStatus_Authorization(t *testing.T) {
	newSrvWithMissingApp := func(t *testing.T) (*AppServer, string) {
		t.Helper()
		srv := NewAppServer(fake.New())
		created, err := srv.ReconcileApps(ctxWithRoles(auth.RoleBuilder), &pb.ReconcileAppsRequest{
			Manifests:      manifestSet([]*appmetapb.AppManifest{oneApp("demo", "svc")}, nil),
			IdempotencyKey: "authz-setstatus-1",
		})
		if err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		if _, err := srv.ReconcileApps(ctxWithRoles(auth.RoleBuilder), &pb.ReconcileAppsRequest{
			Manifests:      manifestSet(nil, nil),
			IdempotencyKey: "authz-setstatus-2",
		}); err != nil {
			t.Fatalf("reconcile dropping svc: %v", err)
		}
		return srv, created.CreatedApps[0].AppId
	}

	req := func(appID string) *pb.SetAppStatusRequest {
		return &pb.SetAppStatusRequest{AppId: appID, Status: pb.AppStatus_APP_STATUS_ARCHIVED, Reason: "cleanup"}
	}

	t.Run("correct role allowed", func(t *testing.T) {
		srv, appID := newSrvWithMissingApp(t)
		_, err := srv.SetAppStatus(ctxWithRoles(auth.RoleAdmin), req(appID))
		if err != nil {
			t.Fatalf("expected admin to be allowed, got %v", err)
		}
	})

	t.Run("wrong role is PermissionDenied", func(t *testing.T) {
		srv, appID := newSrvWithMissingApp(t)
		// A builder-only principal — holds the role every other write RPC in
		// this package requires, but not admin.
		_, err := srv.SetAppStatus(ctxWithRoles(auth.RoleBuilder), req(appID))
		requireCode(t, err, codes.PermissionDenied, "SetAppStatus")
	})

	t.Run("no claims is Unauthenticated", func(t *testing.T) {
		srv, appID := newSrvWithMissingApp(t)
		_, err := srv.SetAppStatus(context.Background(), req(appID))
		requireCode(t, err, codes.Unauthenticated, "SetAppStatus")
	})
}

// TestAppReads_RequireAuthenticationOnly covers ListApps, GetApp, and
// ListCharts: any authenticated principal may call them, no specific role.
func TestAppReads_RequireAuthenticationOnly(t *testing.T) {
	srv := NewAppServer(fake.New())
	reconciled, err := srv.ReconcileApps(ctxWithRoles(auth.RoleBuilder), &pb.ReconcileAppsRequest{
		Manifests:      manifestSet([]*appmetapb.AppManifest{oneApp("demo", "svc")}, nil),
		IdempotencyKey: "authz-reads",
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	appID := reconciled.CreatedApps[0].AppId

	// A principal with an unrelated single role still passes — reads check
	// authentication, not any particular role.
	readerCtx := ctxWithRoles(auth.RolePromoterDev)

	t.Run("ListApps", func(t *testing.T) {
		if _, err := srv.ListApps(readerCtx, &pb.ListAppsRequest{}); err != nil {
			t.Fatalf("expected any authenticated principal to list apps, got %v", err)
		}
		if _, err := srv.ListApps(context.Background(), &pb.ListAppsRequest{}); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("expected Unauthenticated with no claims, got %v", err)
		}
	})

	t.Run("GetApp", func(t *testing.T) {
		if _, err := srv.GetApp(readerCtx, &pb.GetAppRequest{AppId: appID}); err != nil {
			t.Fatalf("expected any authenticated principal to get an app, got %v", err)
		}
		if _, err := srv.GetApp(context.Background(), &pb.GetAppRequest{AppId: appID}); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("expected Unauthenticated with no claims, got %v", err)
		}
	})

	t.Run("ListCharts", func(t *testing.T) {
		if _, err := srv.ListCharts(readerCtx, &pb.ListChartsRequest{}); err != nil {
			t.Fatalf("expected any authenticated principal to list charts, got %v", err)
		}
		if _, err := srv.ListCharts(context.Background(), &pb.ListChartsRequest{}); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("expected Unauthenticated with no claims, got %v", err)
		}
	})
}

// TestRecordBuild_Authorization and TestRecordArtifact_Authorization cover
// ArtifactRegistry's write RPCs, which require RoleBuilder. A promoter
// credential — holding real power elsewhere in the system — must not be
// able to record builds/artifacts; that is precisely the boundary
// KEYCLOAK.md's credential model depends on.
func TestRecordBuild_Authorization(t *testing.T) {
	req := &pb.RecordBuildRequest{GitSha: "abc123", WorkflowRunId: "run-authz", IdempotencyKey: "authz-build"}

	t.Run("correct role allowed", func(t *testing.T) {
		srv := NewArtifactServer(fake.New())
		_, err := srv.RecordBuild(ctxWithRoles(auth.RoleBuilder), req)
		if err != nil {
			t.Fatalf("expected builder to be allowed, got %v", err)
		}
	})

	t.Run("wrong role is PermissionDenied", func(t *testing.T) {
		srv := NewArtifactServer(fake.New())
		_, err := srv.RecordBuild(ctxWithRoles(auth.RolePromoterProd), req)
		requireCode(t, err, codes.PermissionDenied, "RecordBuild")
	})

	t.Run("no claims is Unauthenticated", func(t *testing.T) {
		srv := NewArtifactServer(fake.New())
		_, err := srv.RecordBuild(context.Background(), req)
		requireCode(t, err, codes.Unauthenticated, "RecordBuild")
	})
}

func TestRecordArtifact_Authorization(t *testing.T) {
	newSrvWithBuild := func(t *testing.T) (*ArtifactServer, string) {
		t.Helper()
		srv := NewArtifactServer(fake.New())
		appSrv := NewAppServer(fake.New())
		_ = appSrv
		build, err := srv.RecordBuild(ctxWithRoles(auth.RoleBuilder), &pb.RecordBuildRequest{
			GitSha: "abc123", WorkflowRunId: "run-authz-artifact", IdempotencyKey: "authz-artifact-build",
		})
		if err != nil {
			t.Fatalf("record build: %v", err)
		}
		return srv, build.Build.BuildId
	}

	req := func(buildID string) *pb.RecordArtifactRequest {
		return &pb.RecordArtifactRequest{
			BuildId: buildID, Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE,
			OwnerFullName: "demo-svc", Digest: "sha256:authz", Version: "v1.0.0",
			IdempotencyKey: "authz-record-artifact",
		}
	}

	t.Run("wrong role is PermissionDenied", func(t *testing.T) {
		srv, buildID := newSrvWithBuild(t)
		// A promoter credential — real power elsewhere, not here.
		_, err := srv.RecordArtifact(ctxWithRoles(auth.RolePromoterDev), req(buildID))
		requireCode(t, err, codes.PermissionDenied, "RecordArtifact")
	})

	t.Run("no claims is Unauthenticated", func(t *testing.T) {
		srv, buildID := newSrvWithBuild(t)
		_, err := srv.RecordArtifact(context.Background(), req(buildID))
		requireCode(t, err, codes.Unauthenticated, "RecordArtifact")
	})

	// The "correct role allowed" case for RecordArtifact is already covered
	// end-to-end by artifact_test.go (owner resolution requires a real app),
	// which now runs authenticated as auth.AllRoles() via authedCtx().
}

// TestArtifactReads_RequireAuthenticationOnly covers ListArtifacts,
// GetArtifact, and ResolveArtifact: any authenticated principal, no
// specific role.
func TestArtifactReads_RequireAuthenticationOnly(t *testing.T) {
	srv := NewArtifactServer(fake.New())
	build, err := srv.RecordBuild(ctxWithRoles(auth.RoleBuilder), &pb.RecordBuildRequest{
		GitSha: "abc123", WorkflowRunId: "run-authz-reads", IdempotencyKey: "authz-reads-build",
	})
	if err != nil {
		t.Fatalf("record build: %v", err)
	}

	readerCtx := ctxWithRoles(auth.RolePromoterStage)

	t.Run("ListArtifacts", func(t *testing.T) {
		if _, err := srv.ListArtifacts(readerCtx, &pb.ListArtifactsRequest{}); err != nil {
			t.Fatalf("expected any authenticated principal to list artifacts, got %v", err)
		}
		if _, err := srv.ListArtifacts(context.Background(), &pb.ListArtifactsRequest{}); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("expected Unauthenticated with no claims, got %v", err)
		}
	})

	t.Run("GetArtifact", func(t *testing.T) {
		_, err := srv.GetArtifact(readerCtx, &pb.GetArtifactRequest{ArtifactId: "nonexistent"})
		if status.Code(err) == codes.Unauthenticated || status.Code(err) == codes.PermissionDenied {
			t.Fatalf("expected authorization to pass (NotFound expected next), got %v", err)
		}
		_, err = srv.GetArtifact(context.Background(), &pb.GetArtifactRequest{ArtifactId: "nonexistent"})
		if status.Code(err) != codes.Unauthenticated {
			t.Fatalf("expected Unauthenticated with no claims, got %v", err)
		}
	})

	t.Run("ResolveArtifact", func(t *testing.T) {
		_, err := srv.ResolveArtifact(readerCtx, &pb.ResolveArtifactRequest{ArtifactId: "nonexistent"})
		if status.Code(err) == codes.Unauthenticated || status.Code(err) == codes.PermissionDenied {
			t.Fatalf("expected authorization to pass (NotFound expected next), got %v", err)
		}
		_, err = srv.ResolveArtifact(context.Background(), &pb.ResolveArtifactRequest{ArtifactId: "nonexistent"})
		if status.Code(err) != codes.Unauthenticated {
			t.Fatalf("expected Unauthenticated with no claims, got %v", err)
		}
	})

	_ = build
}
