//go:build integration

// Proves ListModelDefinitions: all rows ordered by model then id, open to
// non-admin callers, and rejected when unauthenticated.
package handlers_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/whale-net/everything/whagent_net/api/handlers"
	pb "github.com/whale-net/everything/whagent_net/protos"
	"github.com/whale-net/everything/whagent_net/session"
)

func TestListModelDefinitions_AllRowsOrdered_NonAdmin(t *testing.T) {
	srv, store, _ := newAgentsServer(t, testAdminRole)
	ctx := context.Background()
	seeded := []struct{ name, model string }{
		{"c-def", "zeta-model"}, {"a-def", "alpha-model"}, {"b-def", "mid-model"},
	}
	for _, s := range seeded {
		require.NoError(t, store.ModelDefinitions().Upsert(ctx, &session.ModelDefinition{Name: s.name, Model: s.model}))
	}

	resp, err := srv.ListModelDefinitions(ctxAs(humanClaims("u")), &pb.ListModelDefinitionsRequest{})
	require.NoError(t, err)
	require.Len(t, resp.GetModelDefinitions(), len(seeded))
	var models []string
	for _, d := range resp.ModelDefinitions {
		assert.NotEmpty(t, d.GetId())
		models = append(models, d.GetModel())
	}
	assert.Equal(t, []string{"alpha-model", "mid-model", "zeta-model"}, models)
	assert.Equal(t, "a-def", resp.ModelDefinitions[0].GetName())
}

func TestListModelDefinitions_Unauthenticated(t *testing.T) {
	srv, _, _ := newAgentsServer(t, testAdminRole)
	_, err := handlers.RequireClaimsUnaryInterceptor(context.Background(), &pb.ListModelDefinitionsRequest{}, &grpc.UnaryServerInfo{}, func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.ListModelDefinitions(ctx, req.(*pb.ListModelDefinitionsRequest))
	})
	assert.Equal(t, codes.Unauthenticated, status.Code(err))
}
