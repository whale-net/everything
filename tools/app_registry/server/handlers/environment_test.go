package handlers

import (
	"testing"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/server/repository/fake"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func upsertReq(key string) *pb.UpsertEnvironmentRequest {
	return &pb.UpsertEnvironmentRequest{
		Key: key, DisplayName: "Development", Rank: 0, GitopsPath: "environments/" + key,
	}
}

// TestUpsertEnvironment_CreateThenUpdate covers the two Upsert branches: a
// fresh key creates (created=true), a repeated key updates every field but
// Key, per repository.EnvironmentRepository.Upsert's doc comment.
func TestUpsertEnvironment_CreateThenUpdate(t *testing.T) {
	ctx := authedCtx()
	srv := NewEnvironmentServer(fake.New())

	created, err := srv.UpsertEnvironment(ctx, upsertReq("dev"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !created.Created || created.Environment.Key != "dev" || created.Environment.DisplayName != "Development" {
		t.Fatalf("expected a newly created dev environment, got %+v", created)
	}
	envID := created.Environment.EnvironmentId

	req := upsertReq("dev")
	req.DisplayName = "Dev (renamed)"
	req.Rank = 5
	req.RequiresApproval = true
	req.AllowedPrincipals = []string{"alice@example.com"}
	updated, err := srv.UpsertEnvironment(ctx, req)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Created {
		t.Fatalf("expected created=false on the second upsert of the same key, got %+v", updated)
	}
	if updated.Environment.EnvironmentId != envID {
		t.Fatalf("expected the same environment_id across upserts, got %s vs %s", updated.Environment.EnvironmentId, envID)
	}
	if updated.Environment.DisplayName != "Dev (renamed)" || updated.Environment.Rank != 5 || !updated.Environment.RequiresApproval {
		t.Fatalf("expected fields updated in place, got %+v", updated.Environment)
	}
	if len(updated.Environment.AllowedPrincipals) != 1 || updated.Environment.AllowedPrincipals[0] != "alice@example.com" {
		t.Fatalf("expected allowed_principals updated, got %+v", updated.Environment.AllowedPrincipals)
	}
}

func TestUpsertEnvironment_RequiresKey(t *testing.T) {
	ctx := authedCtx()
	srv := NewEnvironmentServer(fake.New())
	_, err := srv.UpsertEnvironment(ctx, &pb.UpsertEnvironmentRequest{DisplayName: "no key"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument when key is missing, got %v", err)
	}
}

// TestListEnvironments_OrderedByRank covers ListEnvironmentsResponse's doc
// comment: "Ordered by rank ascending."
func TestListEnvironments_OrderedByRank(t *testing.T) {
	ctx := authedCtx()
	srv := NewEnvironmentServer(fake.New())

	for _, e := range []struct {
		key  string
		rank int32
	}{
		{"prod", 20},
		{"dev", 0},
		{"stage", 10},
	} {
		req := upsertReq(e.key)
		req.Rank = e.rank
		if _, err := srv.UpsertEnvironment(ctx, req); err != nil {
			t.Fatalf("upsert %s: %v", e.key, err)
		}
	}

	resp, err := srv.ListEnvironments(ctx, &pb.ListEnvironmentsRequest{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(resp.Environments) != 3 {
		t.Fatalf("expected 3 environments, got %d", len(resp.Environments))
	}
	gotKeys := []string{resp.Environments[0].Key, resp.Environments[1].Key, resp.Environments[2].Key}
	want := []string{"dev", "stage", "prod"}
	for i := range want {
		if gotKeys[i] != want[i] {
			t.Fatalf("expected rank-ascending order %v, got %v", want, gotKeys)
		}
	}
}

// TestListEnvironments_IncludeArchived covers the include_archived filter:
// archived environments are hidden by default and returned only when asked.
func TestListEnvironments_IncludeArchived(t *testing.T) {
	ctx := authedCtx()
	srv := NewEnvironmentServer(fake.New())

	if _, err := srv.UpsertEnvironment(ctx, upsertReq("dev")); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := srv.ArchiveEnvironment(ctx, &pb.ArchiveEnvironmentRequest{Key: "dev", Reason: "decommissioned"}); err != nil {
		t.Fatalf("archive: %v", err)
	}

	resp, err := srv.ListEnvironments(ctx, &pb.ListEnvironmentsRequest{})
	if err != nil {
		t.Fatalf("list default: %v", err)
	}
	if len(resp.Environments) != 0 {
		t.Fatalf("expected archived environment hidden by default, got %+v", resp.Environments)
	}

	respAll, err := srv.ListEnvironments(ctx, &pb.ListEnvironmentsRequest{IncludeArchived: true})
	if err != nil {
		t.Fatalf("list include_archived: %v", err)
	}
	if len(respAll.Environments) != 1 || !respAll.Environments[0].Archived {
		t.Fatalf("expected 1 archived environment when include_archived=true, got %+v", respAll.Environments)
	}
}

// TestArchiveEnvironment_IdempotentNoOp mirrors SetAppStatus's archived ->
// archived no-op success, so a retried archive call is safe.
func TestArchiveEnvironment_IdempotentNoOp(t *testing.T) {
	ctx := authedCtx()
	srv := NewEnvironmentServer(fake.New())

	if _, err := srv.UpsertEnvironment(ctx, upsertReq("dev")); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	first, err := srv.ArchiveEnvironment(ctx, &pb.ArchiveEnvironmentRequest{Key: "dev", Reason: "gone"})
	if err != nil {
		t.Fatalf("first archive: %v", err)
	}
	if !first.Environment.Archived {
		t.Fatalf("expected archived=true, got %+v", first.Environment)
	}

	second, err := srv.ArchiveEnvironment(ctx, &pb.ArchiveEnvironmentRequest{Key: "dev", Reason: "gone again"})
	if err != nil {
		t.Fatalf("re-archive should be a no-op success, got error: %v", err)
	}
	if !second.Environment.Archived {
		t.Fatalf("expected archived=true, got %+v", second.Environment)
	}
}

func TestArchiveEnvironment_NotFound(t *testing.T) {
	ctx := authedCtx()
	srv := NewEnvironmentServer(fake.New())
	_, err := srv.ArchiveEnvironment(ctx, &pb.ArchiveEnvironmentRequest{Key: "nonexistent", Reason: "gone"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", err)
	}
}

func TestArchiveEnvironment_RequiresReason(t *testing.T) {
	ctx := authedCtx()
	srv := NewEnvironmentServer(fake.New())
	if _, err := srv.UpsertEnvironment(ctx, upsertReq("dev")); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	_, err := srv.ArchiveEnvironment(ctx, &pb.ArchiveEnvironmentRequest{Key: "dev"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument when reason is missing, got %v", err)
	}
}

func TestGetEnvironment_NotFound(t *testing.T) {
	ctx := authedCtx()
	srv := NewEnvironmentServer(fake.New())
	_, err := srv.GetEnvironment(ctx, &pb.GetEnvironmentRequest{Key: "nonexistent"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", err)
	}
}
