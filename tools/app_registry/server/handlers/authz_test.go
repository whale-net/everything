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

// TestUpsertEnvironment_Authorization and TestArchiveEnvironment_Authorization
// cover EnvironmentRegistry's write RPCs, which require RoleAdmin per
// ARCHITECTURE.md's Authorization table -- unlike every other service,
// EnvironmentRegistry has no builder/promoter carve-out at all, so a
// builder credential (real power over AppRegistry/ArtifactRegistry) must
// not be able to touch it either.
func TestUpsertEnvironment_Authorization(t *testing.T) {
	req := &pb.UpsertEnvironmentRequest{Key: "dev", DisplayName: "Development"}

	t.Run("correct role allowed", func(t *testing.T) {
		srv := NewEnvironmentServer(fake.New())
		_, err := srv.UpsertEnvironment(ctxWithRoles(auth.RoleAdmin), req)
		if err != nil {
			t.Fatalf("expected admin to be allowed, got %v", err)
		}
	})

	t.Run("wrong role is PermissionDenied", func(t *testing.T) {
		srv := NewEnvironmentServer(fake.New())
		_, err := srv.UpsertEnvironment(ctxWithRoles(auth.RoleBuilder), req)
		requireCode(t, err, codes.PermissionDenied, "UpsertEnvironment")
	})

	t.Run("no claims is Unauthenticated", func(t *testing.T) {
		srv := NewEnvironmentServer(fake.New())
		_, err := srv.UpsertEnvironment(context.Background(), req)
		requireCode(t, err, codes.Unauthenticated, "UpsertEnvironment")
	})
}

func TestArchiveEnvironment_Authorization(t *testing.T) {
	newSrvWithEnv := func(t *testing.T) *EnvironmentServer {
		t.Helper()
		srv := NewEnvironmentServer(fake.New())
		if _, err := srv.UpsertEnvironment(ctxWithRoles(auth.RoleAdmin), &pb.UpsertEnvironmentRequest{Key: "dev"}); err != nil {
			t.Fatalf("seed environment: %v", err)
		}
		return srv
	}
	req := &pb.ArchiveEnvironmentRequest{Key: "dev", Reason: "decommissioned"}

	t.Run("correct role allowed", func(t *testing.T) {
		srv := newSrvWithEnv(t)
		_, err := srv.ArchiveEnvironment(ctxWithRoles(auth.RoleAdmin), req)
		if err != nil {
			t.Fatalf("expected admin to be allowed, got %v", err)
		}
	})

	t.Run("wrong role is PermissionDenied", func(t *testing.T) {
		srv := newSrvWithEnv(t)
		// A builder credential -- real power over AppRegistry/ArtifactRegistry,
		// none over EnvironmentRegistry.
		_, err := srv.ArchiveEnvironment(ctxWithRoles(auth.RoleBuilder), req)
		requireCode(t, err, codes.PermissionDenied, "ArchiveEnvironment")
	})

	t.Run("no claims is Unauthenticated", func(t *testing.T) {
		srv := newSrvWithEnv(t)
		_, err := srv.ArchiveEnvironment(context.Background(), req)
		requireCode(t, err, codes.Unauthenticated, "ArchiveEnvironment")
	})
}

// TestEnvironmentReads_RequireAuthenticationOnly covers GetEnvironment and
// ListEnvironments: any authenticated principal, no specific role -- not
// even RoleAdmin, matching every other service's read RPCs.
func TestEnvironmentReads_RequireAuthenticationOnly(t *testing.T) {
	srv := NewEnvironmentServer(fake.New())
	if _, err := srv.UpsertEnvironment(ctxWithRoles(auth.RoleAdmin), &pb.UpsertEnvironmentRequest{Key: "dev"}); err != nil {
		t.Fatalf("seed environment: %v", err)
	}

	// A principal with an unrelated single role still passes.
	readerCtx := ctxWithRoles(auth.RoleBuilder)

	t.Run("GetEnvironment", func(t *testing.T) {
		if _, err := srv.GetEnvironment(readerCtx, &pb.GetEnvironmentRequest{Key: "dev"}); err != nil {
			t.Fatalf("expected any authenticated principal to get an environment, got %v", err)
		}
		if _, err := srv.GetEnvironment(context.Background(), &pb.GetEnvironmentRequest{Key: "dev"}); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("expected Unauthenticated with no claims, got %v", err)
		}
	})

	t.Run("ListEnvironments", func(t *testing.T) {
		if _, err := srv.ListEnvironments(readerCtx, &pb.ListEnvironmentsRequest{}); err != nil {
			t.Fatalf("expected any authenticated principal to list environments, got %v", err)
		}
		if _, err := srv.ListEnvironments(context.Background(), &pb.ListEnvironmentsRequest{}); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("expected Unauthenticated with no claims, got %v", err)
		}
	})
}

// TestPromote_Authorization and TestRollback_Authorization cover
// PromotionRegistry's write RPCs, which require
// auth.RequirePromoter(ctx, environment_key) -- an ENVIRONMENT-SCOPED role,
// not a flat one. This is the credential-model boundary KEYCLOAK.md and
// ARCHITECTURE.md's Authorization table both describe: a promoter-dev
// credential must not be able to promote to prod, and a builder credential
// (real power over AppRegistry/ArtifactRegistry) must not be able to
// promote anywhere.
func TestPromote_Authorization(t *testing.T) {
	newSrv := func(t *testing.T) *PromotionServer {
		t.Helper()
		repo := fake.New()
		envSrv := NewEnvironmentServer(repo)
		if _, err := envSrv.UpsertEnvironment(ctxWithRoles(auth.RoleAdmin), &pb.UpsertEnvironmentRequest{Key: "dev"}); err != nil {
			t.Fatalf("seed dev environment: %v", err)
		}
		if _, err := envSrv.UpsertEnvironment(ctxWithRoles(auth.RoleAdmin), &pb.UpsertEnvironmentRequest{Key: "prod", Rank: 20}); err != nil {
			t.Fatalf("seed prod environment: %v", err)
		}
		return NewPromotionServer(repo)
	}
	req := func(env string) *pb.PromoteRequest {
		return &pb.PromoteRequest{EnvironmentKey: env, ArtifactId: "nonexistent", Reason: "test", IdempotencyKey: "authz-promote"}
	}

	t.Run("matching environment role allowed through to business logic", func(t *testing.T) {
		srv := newSrv(t)
		// promoter-dev may call Promote against dev -- the request fails
		// downstream (unknown artifact), which is exactly how we know auth
		// let it through instead of stopping it.
		_, err := srv.Promote(ctxWithRoles(auth.RolePromoterDev), req("dev"))
		if status.Code(err) == codes.Unauthenticated || status.Code(err) == codes.PermissionDenied {
			t.Fatalf("expected promoter-dev to pass authorization for dev, got %v", err)
		}
	})

	t.Run("wrong environment's promoter role is PermissionDenied", func(t *testing.T) {
		srv := newSrv(t)
		// promoter-dev holds real power over dev, none over prod -- the
		// exact per-environment isolation ARCHITECTURE.md depends on.
		_, err := srv.Promote(ctxWithRoles(auth.RolePromoterDev), req("prod"))
		requireCode(t, err, codes.PermissionDenied, "Promote(prod) as promoter-dev")
	})

	t.Run("builder credential cannot promote", func(t *testing.T) {
		srv := newSrv(t)
		_, err := srv.Promote(ctxWithRoles(auth.RoleBuilder), req("dev"))
		requireCode(t, err, codes.PermissionDenied, "Promote as builder")
	})

	t.Run("no claims is Unauthenticated", func(t *testing.T) {
		srv := newSrv(t)
		_, err := srv.Promote(context.Background(), req("dev"))
		requireCode(t, err, codes.Unauthenticated, "Promote")
	})
}

func TestRollback_Authorization(t *testing.T) {
	repo := fake.New()
	envSrv := NewEnvironmentServer(repo)
	if _, err := envSrv.UpsertEnvironment(ctxWithRoles(auth.RoleAdmin), &pb.UpsertEnvironmentRequest{Key: "prod", Rank: 20}); err != nil {
		t.Fatalf("seed prod environment: %v", err)
	}
	srv := NewPromotionServer(repo)
	req := &pb.RollbackRequest{EnvironmentKey: "prod", OwnerFullName: "demo-svc", Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE, Reason: "test", IdempotencyKey: "authz-rollback"}

	t.Run("wrong environment's promoter role is PermissionDenied", func(t *testing.T) {
		_, err := srv.Rollback(ctxWithRoles(auth.RolePromoterDev), req)
		requireCode(t, err, codes.PermissionDenied, "Rollback(prod) as promoter-dev")
	})

	t.Run("no claims is Unauthenticated", func(t *testing.T) {
		_, err := srv.Rollback(context.Background(), req)
		requireCode(t, err, codes.Unauthenticated, "Rollback")
	})
}

// TestPromotionReads_RequireAuthenticationOnly covers GetEnvironmentState,
// ListPromotions, and ListPromotionEvents: any authenticated principal, no
// specific (let alone environment-scoped) role.
func TestPromotionReads_RequireAuthenticationOnly(t *testing.T) {
	repo := fake.New()
	envSrv := NewEnvironmentServer(repo)
	if _, err := envSrv.UpsertEnvironment(ctxWithRoles(auth.RoleAdmin), &pb.UpsertEnvironmentRequest{Key: "dev"}); err != nil {
		t.Fatalf("seed dev environment: %v", err)
	}
	srv := NewPromotionServer(repo)
	readerCtx := ctxWithRoles(auth.RoleBuilder)

	t.Run("GetEnvironmentState", func(t *testing.T) {
		if _, err := srv.GetEnvironmentState(readerCtx, &pb.GetEnvironmentStateRequest{EnvironmentKey: "dev"}); err != nil {
			t.Fatalf("expected any authenticated principal to read environment state, got %v", err)
		}
		if _, err := srv.GetEnvironmentState(context.Background(), &pb.GetEnvironmentStateRequest{EnvironmentKey: "dev"}); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("expected Unauthenticated with no claims, got %v", err)
		}
	})

	t.Run("ListPromotions", func(t *testing.T) {
		if _, err := srv.ListPromotions(readerCtx, &pb.ListPromotionsRequest{}); err != nil {
			t.Fatalf("expected any authenticated principal to list promotions, got %v", err)
		}
		if _, err := srv.ListPromotions(context.Background(), &pb.ListPromotionsRequest{}); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("expected Unauthenticated with no claims, got %v", err)
		}
	})

	t.Run("ListPromotionEvents", func(t *testing.T) {
		if _, err := srv.ListPromotionEvents(readerCtx, &pb.ListPromotionEventsRequest{}); err != nil {
			t.Fatalf("expected any authenticated principal to list promotion events, got %v", err)
		}
		if _, err := srv.ListPromotionEvents(context.Background(), &pb.ListPromotionEventsRequest{}); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("expected Unauthenticated with no claims, got %v", err)
		}
	})
}
