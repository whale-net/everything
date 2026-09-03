package store_test

// Pure-Go coverage for authz.go's CanApprove/CanInvite/CanReconnect/
// CanRead/CanWrite against a fake RoleStore. This complements (does not
// replace) store_integration_test.go's real-Postgres coverage of the same
// functions: it runs everywhere (no Docker required) and pins down the
// exact role-set semantics (creator-only vs. creator-or-analyst) at the
// unit level, while the integration test proves the same functions read
// live channel_person state through the real SQL-backed RoleStore.
import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/audience_score_system/store"
)

// fakeRoleStore implements store.RoleStore by returning a fixed role set
// for one (channelID, personID) pair, keyed by pair equality -- enough to
// drive authz.go's Can* functions without a real database. AddRole is
// unused by these tests (authz.go is defined purely in terms of RolesFor).
type fakeRoleStore struct {
	channelID, personID uuid.UUID
	roles               []store.Role
}

var _ store.RoleStore = fakeRoleStore{}

func (f fakeRoleStore) RolesFor(_ context.Context, channelID, personID uuid.UUID) ([]store.Role, error) {
	if channelID == f.channelID && personID == f.personID {
		return f.roles, nil
	}
	return nil, nil
}

func (f fakeRoleStore) AddRole(context.Context, uuid.UUID, uuid.UUID, store.Role) error {
	return errors.New("fakeRoleStore.AddRole is not used by authz tests")
}

func TestAuthz_CreatorOnlyChecks(t *testing.T) {
	ctx := context.Background()
	channelID, personID := uuid.New(), uuid.New()

	checks := []struct {
		name string
		fn   func(context.Context, store.RoleStore, uuid.UUID, uuid.UUID) (bool, error)
	}{
		{"CanApprove", store.CanApprove},
		{"CanInvite", store.CanInvite},
		{"CanReconnect", store.CanReconnect},
	}

	cases := []struct {
		name  string
		roles []store.Role
		want  bool
	}{
		{"creator role -> authorized", []store.Role{store.RoleCreator}, true},
		{"analyst role only -> not authorized", []store.Role{store.RoleAnalyst}, false},
		{"no roles -> not authorized", nil, false},
	}

	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					rs := fakeRoleStore{channelID: channelID, personID: personID, roles: tc.roles}
					got, err := check.fn(ctx, rs, channelID, personID)
					require.NoError(t, err)
					assert.Equal(t, tc.want, got)
				})
			}
		})
	}
}

func TestAuthz_CreatorOrAnalystChecks(t *testing.T) {
	ctx := context.Background()
	channelID, personID := uuid.New(), uuid.New()

	checks := []struct {
		name string
		fn   func(context.Context, store.RoleStore, uuid.UUID, uuid.UUID) (bool, error)
	}{
		{"CanRead", store.CanRead},
		{"CanWrite", store.CanWrite},
	}

	cases := []struct {
		name  string
		roles []store.Role
		want  bool
	}{
		{"creator role -> authorized", []store.Role{store.RoleCreator}, true},
		{"analyst role -> authorized", []store.Role{store.RoleAnalyst}, true},
		{"no roles -> not authorized", nil, false},
	}

	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					rs := fakeRoleStore{channelID: channelID, personID: personID, roles: tc.roles}
					got, err := check.fn(ctx, rs, channelID, personID)
					require.NoError(t, err)
					assert.Equal(t, tc.want, got)
				})
			}
		})
	}
}

// TestAuthz_DifferentChannelOrPerson_NotAuthorized proves a Can* check
// never returns true for a (channelID, personID) pair it wasn't asked
// about -- guards against a hypothetical implementation that ignores its
// arguments and always queries "the" role.
func TestAuthz_DifferentChannelOrPerson_NotAuthorized(t *testing.T) {
	ctx := context.Background()
	channelID, personID := uuid.New(), uuid.New()
	rs := fakeRoleStore{channelID: channelID, personID: personID, roles: []store.Role{store.RoleCreator}}

	got, err := store.CanApprove(ctx, rs, uuid.New(), personID)
	require.NoError(t, err)
	assert.False(t, got, "a different channelID must not be authorized")

	got, err = store.CanApprove(ctx, rs, channelID, uuid.New())
	require.NoError(t, err)
	assert.False(t, got, "a different personID must not be authorized")
}
