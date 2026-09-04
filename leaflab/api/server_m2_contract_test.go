package main

// Contract-only tests for #1760: the seven M2 RPCs (ClaimBoard, RenameBoard,
// RenameSensor, ListOwnedBoards, ReassignBoardOwner, ClearBoardOwner,
// ListUsers) are stubs at this stage -- their handler logic lands in later
// tasks, which supersede (not delete) the reachability test below by
// replacing its Unimplemented assertion with real behavior coverage. What
// this file guards for good:
//
//   - every new RPC is reachable through a real, registered *grpc.Server
//     (not just callable as a Go method) -- a dropped pb.RegisterLeafLabAPIServer
//     call, or an RPC missing from api.proto entirely, answers with grpc-go's
//     own "unknown service"/"unknown method" Unimplemented, which
//     assertUnimplementedFromHandler below distinguishes from the handler's
//     own deliberate Unimplemented.
//   - NFR5: LeafLabUser (and anything that embeds it) never carries a field
//     named oidc_sub on the wire.

import (
	"context"
	"log/slog"
	"net"
	"strings"
	"testing"

	pb "github.com/whale-net/everything/leaflab/api/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const m2BufSize = 1024 * 1024

// startM2TestServer builds a real *grpc.Server with the actual
// LeafLabAPIServer registered on it (the same pb.RegisterLeafLabAPIServer
// call main.go's run() makes), served over an in-memory bufconn listener --
// no TCP port, no Postgres, no RabbitMQ. repo and publisher are both nil:
// none of the seven RPCs under test reach past their Unimplemented stub, so
// neither is ever dereferenced.
func startM2TestServer(t *testing.T) *grpc.ClientConn {
	t.Helper()

	lis := bufconn.Listen(m2BufSize)
	grpcServer := grpc.NewServer()
	apiServer := NewLeafLabAPIServer(NewRepository(nil), nil, slog.Default())
	pb.RegisterLeafLabAPIServer(grpcServer, apiServer)

	go func() {
		// Serve returns a non-nil error on Stop() too; t.Cleanup below
		// already knows the server is going down, nothing to assert here.
		_ = grpcServer.Serve(lis)
	}()

	dialer := func(context.Context, string) (net.Conn, error) { return lis.Dial() }
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("failed to dial bufconn server: %v", err)
	}

	t.Cleanup(func() {
		conn.Close() //nolint:errcheck
		grpcServer.Stop()
	})

	return conn
}

// assertUnimplementedFromHandler fails the test unless err is a gRPC status
// error with code Unimplemented AND a message matching wantMsg exactly.
//
// Code alone is not enough: grpc-go itself answers codes.Unimplemented with
// message "unknown service ..."/"unknown method ..." for an RPC that was
// never registered at all -- the same code as our stubs' deliberate
// status.Error(codes.Unimplemented, "<RPC>: not implemented"). A dropped
// pb.RegisterLeafLabAPIServer call, or an RPC removed from api.proto, would
// still show codes.Unimplemented and pass a code-only check; the message
// assertion is what catches that case (and is exactly the "removing an RPC
// from the proto breaks the build/test" half of this task's red/green
// check -- see the issue's Testing criteria).
func assertUnimplementedFromHandler(t *testing.T, rpc, wantMsg string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected an Unimplemented error, got nil", rpc)
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("%s: expected a gRPC status error, got %v", rpc, err)
	}
	if st.Code() != codes.Unimplemented {
		t.Fatalf("%s: expected codes.Unimplemented, got %v (%v)", rpc, st.Code(), err)
	}
	if strings.Contains(st.Message(), "unknown service") || strings.Contains(st.Message(), "unknown method") {
		t.Fatalf("%s: got grpc's own %q -- the RPC was never registered/never reached the handler, not just unimplemented", rpc, st.Message())
	}
	if st.Message() != wantMsg {
		t.Fatalf("%s: expected message %q from the handler, got %q", rpc, wantMsg, st.Message())
	}
}

// TestM2RPCs_ReachableAndUnimplemented asserts every one of the seven new
// M2 RPCs is reachable on the registered server and returns
// codes.Unimplemented at this stage (issue #1760's Testing criteria). Each
// subtest is superseded, not deleted, when that RPC's own task lands: it
// should be updated in place to assert the real behavior instead of
// Unimplemented.
func TestM2RPCs_ReachableAndUnimplemented(t *testing.T) {
	conn := startM2TestServer(t)
	client := pb.NewLeafLabAPIClient(conn)
	ctx := context.Background()

	// ClaimBoard's own task (#1765) landed: this subtest is superseded per
	// this test's doc comment above, in place rather than deleted. Its own
	// FR1/FR2/NFR2 behavior (claim succeeds, refusal on an already-owned
	// board, no implicit provisioning, etc.) is covered directly against
	// LeafLabAPIServer in server_test.go's "#1765 ClaimBoard tests" section
	// -- what's left to prove here is only the same reachability guarantee
	// every other subtest proves: a call with no auth claims reaches the
	// real handler (codes.Unauthenticated, from callerUserID) rather than
	// grpc-go's own "unknown service"/"unknown method" Unimplemented for an
	// RPC that was never registered.
	t.Run("ClaimBoard", func(t *testing.T) {
		_, err := client.ClaimBoard(ctx, &pb.ClaimBoardRequest{BoardId: 1})
		st, ok := status.FromError(err)
		if !ok {
			t.Fatalf("ClaimBoard: expected a gRPC status error, got %v", err)
		}
		if st.Code() != codes.Unauthenticated {
			t.Fatalf("ClaimBoard: expected codes.Unauthenticated (reachable, real handler, no claims in ctx), got %v (%v)", st.Code(), err)
		}
		if strings.Contains(st.Message(), "unknown service") || strings.Contains(st.Message(), "unknown method") {
			t.Fatalf("ClaimBoard: got grpc's own %q -- the RPC was never registered/never reached the handler", st.Message())
		}
	})

	// RenameBoard is implemented (#1767, FR3) -- superseded per this test's
	// own doc comment. Its real behavior (owner-only, non-empty-only,
	// unknown-board_id NotFound, no publish) is covered against a fake
	// repository in server_test.go, not here: this file's servers are built
	// with NewRepository(nil), which cannot serve a real RenameBoard call.

	// RenameSensor is implemented (#1770, FR4) -- superseded per this test's
	// own doc comment, same as RenameBoard above. Its real behavior is
	// covered against a fake repository in server_test.go, not here: like
	// RenameBoard, it resolves an existence lookup (GetBoardIDForSensor)
	// before authorizeBoardWrite, so a call against this file's
	// NewRepository(nil) servers cannot be reachability-tested without a
	// real Postgres pool.

	// ListOwnedBoards/ReassignBoardOwner/ClearBoardOwner/ListUsers' own task
	// (#1777, FR11-FR14) landed: these four subtests are superseded per
	// this test's doc comment above, in place rather than deleted --
	// exactly the same shape as the ClaimBoard subtest above. Each RPC now
	// calls requireAdmin first (server.go), which resolves the caller via
	// callerUserID before ever touching the repository -- with no auth
	// claims in ctx, that's codes.Unauthenticated, proving the RPC is
	// reachable and reaches the real handler rather than grpc-go's own
	// "unknown service"/"unknown method" Unimplemented. Real FR11-FR14
	// behavior (admin gate, SCD2 close-and-open, the unowned/
	// current-owner/unknown-user checks) is covered against a fake
	// repository in server_test.go, not here: this file's servers are
	// built with NewRepository(nil), which cannot serve a real call past
	// requireAdmin.

	t.Run("ListOwnedBoards", func(t *testing.T) {
		_, err := client.ListOwnedBoards(ctx, &pb.ListOwnedBoardsRequest{})
		st, ok := status.FromError(err)
		if !ok {
			t.Fatalf("ListOwnedBoards: expected a gRPC status error, got %v", err)
		}
		if st.Code() != codes.Unauthenticated {
			t.Fatalf("ListOwnedBoards: expected codes.Unauthenticated (reachable, real handler, no claims in ctx), got %v (%v)", st.Code(), err)
		}
		if strings.Contains(st.Message(), "unknown service") || strings.Contains(st.Message(), "unknown method") {
			t.Fatalf("ListOwnedBoards: got grpc's own %q -- the RPC was never registered/never reached the handler", st.Message())
		}
	})

	t.Run("ReassignBoardOwner", func(t *testing.T) {
		_, err := client.ReassignBoardOwner(ctx, &pb.ReassignBoardOwnerRequest{BoardId: 1, NewOwnerLeaflabUserId: 2})
		st, ok := status.FromError(err)
		if !ok {
			t.Fatalf("ReassignBoardOwner: expected a gRPC status error, got %v", err)
		}
		if st.Code() != codes.Unauthenticated {
			t.Fatalf("ReassignBoardOwner: expected codes.Unauthenticated (reachable, real handler, no claims in ctx), got %v (%v)", st.Code(), err)
		}
		if strings.Contains(st.Message(), "unknown service") || strings.Contains(st.Message(), "unknown method") {
			t.Fatalf("ReassignBoardOwner: got grpc's own %q -- the RPC was never registered/never reached the handler", st.Message())
		}
	})

	t.Run("ClearBoardOwner", func(t *testing.T) {
		_, err := client.ClearBoardOwner(ctx, &pb.ClearBoardOwnerRequest{BoardId: 1})
		st, ok := status.FromError(err)
		if !ok {
			t.Fatalf("ClearBoardOwner: expected a gRPC status error, got %v", err)
		}
		if st.Code() != codes.Unauthenticated {
			t.Fatalf("ClearBoardOwner: expected codes.Unauthenticated (reachable, real handler, no claims in ctx), got %v (%v)", st.Code(), err)
		}
		if strings.Contains(st.Message(), "unknown service") || strings.Contains(st.Message(), "unknown method") {
			t.Fatalf("ClearBoardOwner: got grpc's own %q -- the RPC was never registered/never reached the handler", st.Message())
		}
	})

	t.Run("ListUsers", func(t *testing.T) {
		_, err := client.ListUsers(ctx, &pb.ListUsersRequest{})
		st, ok := status.FromError(err)
		if !ok {
			t.Fatalf("ListUsers: expected a gRPC status error, got %v", err)
		}
		if st.Code() != codes.Unauthenticated {
			t.Fatalf("ListUsers: expected codes.Unauthenticated (reachable, real handler, no claims in ctx), got %v (%v)", st.Code(), err)
		}
		if strings.Contains(st.Message(), "unknown service") || strings.Contains(st.Message(), "unknown method") {
			t.Fatalf("ListUsers: got grpc's own %q -- the RPC was never registered/never reached the handler", st.Message())
		}
	})
}

// TestLeafLabUser_WireContract_NeverExposesOidcSub protects NFR5 (see
// 013_ownership.up.sql and api.proto's LeafLabUser doc comment): the wire
// contract must never carry a field literally named oidc_sub, either
// directly or nested inside any message reachable from
// ListOwnedBoardsResponse/ListUsersResponse. It walks the actual proto
// descriptor tree -- not the Go struct -- so a future field addition is
// caught regardless of what Go field name protoc-gen-go happens to choose
// for it.
func TestLeafLabUser_WireContract_NeverExposesOidcSub(t *testing.T) {
	assertNoOidcSubField(t, (&pb.ListOwnedBoardsResponse{}).ProtoReflect().Descriptor(), map[protoreflect.FullName]bool{})
	assertNoOidcSubField(t, (&pb.ListUsersResponse{}).ProtoReflect().Descriptor(), map[protoreflect.FullName]bool{})
}

// assertNoOidcSubField recursively walks desc's fields (and the fields of
// any nested message-typed field) failing the test if any field is named
// "oidc_sub". visited guards against re-walking the same message type twice
// (LeafLabUser is reachable from both OwnedBoard and ListUsersResponse).
func assertNoOidcSubField(t *testing.T, desc protoreflect.MessageDescriptor, visited map[protoreflect.FullName]bool) {
	t.Helper()

	if visited[desc.FullName()] {
		return
	}
	visited[desc.FullName()] = true

	fields := desc.Fields()
	for i := 0; i < fields.Len(); i++ {
		field := fields.Get(i)
		if string(field.Name()) == "oidc_sub" {
			t.Fatalf("%s has a field named oidc_sub -- NFR5 forbids the raw OIDC claim string on the wire", desc.FullName())
		}
		if field.Kind() == protoreflect.MessageKind {
			assertNoOidcSubField(t, field.Message(), visited)
		}
	}
}
