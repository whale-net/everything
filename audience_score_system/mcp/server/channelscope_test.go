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

// TestRequireChannelAccess is FR11/FR12's boundary matrix, exercised
// directly against RequireChannelAccess rather than through the registry
// (registry_test.go covers the RegisterRead/RegisterWrite wiring that
// calls this alongside RequireChannelRole).
func TestRequireChannelAccess(t *testing.T) {
	otherChannelID, personID := uuid.New(), uuid.New()

	ctxWithPath := func(path AuthPath) context.Context {
		return withPerson(context.Background(), store.Person{ID: personID}, path)
	}

	t.Run("whagent caller with zero roles across every Channel gets the linking-flow message", func(t *testing.T) {
		roles := newFakeRoleStore()
		err := RequireChannelAccess(ctxWithPath(AuthPathWhagent), roles, personID)
		require.Error(t, err)
		assert.Equal(t, linkingRequiredMessage, err.Error())
	})

	t.Run("whagent caller with a role on some Channel is unaffected, even one this call doesn't target", func(t *testing.T) {
		roles := newFakeRoleStore()
		roles.grant(otherChannelID, personID, store.RoleAnalyst)
		assert.NoError(t, RequireChannelAccess(ctxWithPath(AuthPathWhagent), roles, personID),
			"the whole-Person check must pass once ANY Channel role exists -- RequireChannelRole, not this check, is what rejects a mismatched target Channel")
	})

	t.Run("mcp_credential caller with zero roles across every Channel never gets the message (FR12)", func(t *testing.T) {
		roles := newFakeRoleStore()
		assert.NoError(t, RequireChannelAccess(ctxWithPath(AuthPathMCPCredential), roles, personID),
			"an ASS-native Person can hold zero roles for an unrelated reason (e.g. pending an invite)")
	})

	t.Run("AuthPathUnknown caller with zero roles never gets the message", func(t *testing.T) {
		roles := newFakeRoleStore()
		assert.NoError(t, RequireChannelAccess(ctxWithPath(AuthPathUnknown), roles, personID))
	})

	t.Run("a store error propagates rather than reading as zero-roles", func(t *testing.T) {
		roles := newFakeRoleStore()
		wantErr := errors.New("role store unavailable")
		roles.channelsForPersonErr = wantErr
		err := RequireChannelAccess(ctxWithPath(AuthPathWhagent), roles, personID)
		require.Error(t, err)
		assert.ErrorIs(t, err, wantErr)
		assert.NotEqual(t, linkingRequiredMessage, err.Error(), "a genuine store failure must not be misreported as the linking-flow message")
	})
}

// TestRequireChannelAccess_RedGreen drops the AuthPathWhagent condition
// (this task's Testing-section discipline check) to prove the FR12
// exclusion test above actually guards something: with the condition
// gone, an mcp_credential caller with zero roles WOULD also get the
// linking message, which is exactly the regression FR12 forbids.
func TestRequireChannelAccess_RedGreen(t *testing.T) {
	roles := newFakeRoleStore()
	personID := uuid.New()
	ctx := withPerson(context.Background(), store.Person{ID: personID}, AuthPathMCPCredential)

	brokenRequireChannelAccess := func(ctx context.Context, roles store.RoleStore, personID uuid.UUID) error {
		// Same body as RequireChannelAccess, MINUS the AuthPathWhagent
		// guard -- simulates the regression a careless edit could
		// introduce.
		channels, err := roles.ChannelsForPerson(ctx, personID)
		if err != nil {
			return err
		}
		if len(channels) == 0 {
			return errors.New(linkingRequiredMessage)
		}
		return nil
	}

	assert.NoError(t, RequireChannelAccess(ctx, roles, personID), "green: the real check must never message an mcp_credential caller")
	assert.Error(t, brokenRequireChannelAccess(ctx, roles, personID), "red: without the AuthPathWhagent guard, the same caller WOULD wrongly get the message")
}
