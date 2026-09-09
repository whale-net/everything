package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/whagent_net/config"
)

// TestDevRolesCarrySeededRequiredRoles proves GRPC_AUTH_MODE=none's dev
// Claims carry every seeded agent's required_role (FR9, issue #2154) --
// not grpcauth's generic ["admin"] default, which matches none of them
// (issue #2149's finding, which left StartSession permanently
// PermissionDenied under the documented local-dev workflow). Exercises the
// same wiring run() builds in main.go: config.Load() ->
// config.RequiredRoles(agentDefs) -> grpcauth.ServerConfig.DevRoles, then
// runs the resulting unary interceptor directly against a fake handler
// that captures grpcauth.ClaimsFromContext(ctx) -- no gRPC server/bufconn
// needed since the interceptor itself is what's under test here (compare
// tools/app_registry/server/main_test.go's bufconn-based sibling, needed
// there because service registration -- not just the interceptor -- was
// also under test).
func TestDevRolesCarrySeededRequiredRoles(t *testing.T) {
	agentDefs, err := config.Load()
	require.NoError(t, err)

	unaryAuth, _, err := grpcauth.NewServerInterceptors(context.Background(), grpcauth.ServerConfig{
		Mode:     grpcauth.AuthModeNone,
		DevRoles: config.RequiredRoles(agentDefs),
	})
	require.NoError(t, err)

	var captured *grpcauth.Claims
	fakeHandler := func(ctx context.Context, req interface{}) (interface{}, error) {
		claims, ok := grpcauth.ClaimsFromContext(ctx)
		require.True(t, ok, "expected Claims injected by AuthModeNone")
		captured = claims
		return nil, nil
	}

	_, err = unaryAuth(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/whagent.SessionService/StartSession"}, fakeHandler)
	require.NoError(t, err)
	require.NotNil(t, captured)

	assert.Contains(t, captured.Roles, "whagent-audience-score-system-research")
}
