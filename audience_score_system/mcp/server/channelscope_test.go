package server

// Pure-Go coverage for RequireChannelRole (channelscope.go) directly --
// registry_test.go already exercises it indirectly through
// RegisterRead/RegisterWrite, but this pins down its own contract: a
// passing check returns nil, a failing check returns a "permission
// denied" error, and an error from check itself (e.g. a store failure)
// propagates rather than being swallowed as "not authorized".
import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/audience_score_system/store"
)

func TestRequireChannelRole(t *testing.T) {
	ctx := context.Background()
	channelID, personID := uuid.New(), uuid.New()

	t.Run("authorized returns nil", func(t *testing.T) {
		check := func(context.Context, store.RoleStore, uuid.UUID, uuid.UUID) (bool, error) { return true, nil }
		assert.NoError(t, RequireChannelRole(ctx, nil, check, channelID, personID))
	})

	t.Run("unauthorized returns a permission-denied error", func(t *testing.T) {
		check := func(context.Context, store.RoleStore, uuid.UUID, uuid.UUID) (bool, error) { return false, nil }
		err := RequireChannelRole(ctx, nil, check, channelID, personID)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "permission denied")
	})

	t.Run("a check error propagates rather than reading as unauthorized", func(t *testing.T) {
		wantErr := errors.New("role store unavailable")
		check := func(context.Context, store.RoleStore, uuid.UUID, uuid.UUID) (bool, error) { return false, wantErr }
		err := RequireChannelRole(ctx, nil, check, channelID, personID)
		require.Error(t, err)
		assert.ErrorIs(t, err, wantErr)
	})
}
