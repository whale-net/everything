package store_test

// Pure-Go coverage for authz.go's CanApprove/CanInvite/CanReconnect/
// CanRead/CanWrite/CanRemove/CanViewAudit against a fake RoleStore. This
// complements (does not replace) store_integration_test.go's real-Postgres
// coverage of the same functions: it runs everywhere (no Docker required)
// and pins down the exact role-set semantics (creator-tier-only vs.
// creator-tier-or-analyst vs. the CanRemove matrix) at the unit level,
// while the integration test proves the same functions read live
// channel_person state through the real SQL-backed RoleStore.
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

func (f fakeRoleStore) AddRole(context.Context, uuid.UUID, uuid.UUID, store.Role, uuid.UUID) error {
	return errors.New("fakeRoleStore.AddRole is not used by authz tests")
}

func (f fakeRoleStore) RemoveRole(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (bool, error) {
	return false, errors.New("fakeRoleStore.RemoveRole is not used by authz tests")
}

func (f fakeRoleStore) ChannelsForPerson(context.Context, uuid.UUID) ([]store.Channel, error) {
	return nil, errors.New("fakeRoleStore.ChannelsForPerson is not used by authz tests")
}

func (f fakeRoleStore) RowID(context.Context, uuid.UUID, uuid.UUID) (uuid.UUID, bool, error) {
	return uuid.Nil, false, errors.New("fakeRoleStore.RowID is not used by authz tests")
}

func (f fakeRoleStore) RowByID(context.Context, uuid.UUID) (store.ChannelPerson, error) {
	return store.ChannelPerson{}, errors.New("fakeRoleStore.RowByID is not used by authz tests")
}

// TestAuthz_CreatorTierChecks covers CanApprove/CanInvite/CanReconnect,
// which per FR32 grant symmetric authority to both Founder (RoleCreator)
// and Co-Creator (RoleCoCreator) -- and to no one else. The "no roles"
// case doubles as this suite's "actor's only row is closed" coverage:
// RolesFor (per its own doc comment) only ever returns currently-open
// rows, so a Person whose sole row has been closed (valid_to set) is
// indistinguishable, from this function's point of view, from a Person
// with no row at all -- both surface as an empty role set here. The
// existing M1 tests asserted this same "no live role -> false" behavior
// for the two-tier version of these helpers; this case pins down that the
// M2 widening does not regress it.
func TestAuthz_CreatorTierChecks(t *testing.T) {
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
		{"co_creator role -> authorized (FR32 symmetry)", []store.Role{store.RoleCoCreator}, true},
		{"analyst role only -> not authorized", []store.Role{store.RoleAnalyst}, false},
		{"no roles (or actor's only row closed) -> not authorized", nil, false},
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

// TestAuthz_CreatorTierOrAnalystChecks covers CanRead/CanWrite, which
// authorize all three tiers.
func TestAuthz_CreatorTierOrAnalystChecks(t *testing.T) {
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
		{"co_creator role -> authorized", []store.Role{store.RoleCoCreator}, true},
		{"analyst role -> authorized", []store.Role{store.RoleAnalyst}, true},
		{"no roles (or actor's only row closed) -> not authorized", nil, false},
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

// TestAuthz_CanViewAudit covers FR35's audit gate: Founder and Co-Creator
// only, Analyst excluded.
func TestAuthz_CanViewAudit(t *testing.T) {
	ctx := context.Background()
	channelID, personID := uuid.New(), uuid.New()

	cases := []struct {
		name  string
		roles []store.Role
		want  bool
	}{
		{"creator role -> authorized", []store.Role{store.RoleCreator}, true},
		{"co_creator role -> authorized", []store.Role{store.RoleCoCreator}, true},
		{"analyst role -> NOT authorized (FR35 excludes Analyst)", []store.Role{store.RoleAnalyst}, false},
		{"no roles (or actor's only row closed) -> not authorized", nil, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rs := fakeRoleStore{channelID: channelID, personID: personID, roles: tc.roles}
			got, err := store.CanViewAudit(ctx, rs, channelID, personID)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// twoPersonRoleStore is a fakeRoleStore variant that fixes independent
// role sets for two distinct Persons on the same Channel -- what
// CanRemove's actor/target pair needs, which the single-pair fakeRoleStore
// above cannot express.
type twoPersonRoleStore struct {
	channelID               uuid.UUID
	actorID, targetID       uuid.UUID
	actorRoles, targetRoles []store.Role
}

var _ store.RoleStore = twoPersonRoleStore{}

func (f twoPersonRoleStore) RolesFor(_ context.Context, channelID, personID uuid.UUID) ([]store.Role, error) {
	if channelID != f.channelID {
		return nil, nil
	}
	switch personID {
	case f.actorID:
		return f.actorRoles, nil
	case f.targetID:
		return f.targetRoles, nil
	default:
		return nil, nil
	}
}

func (f twoPersonRoleStore) AddRole(context.Context, uuid.UUID, uuid.UUID, store.Role, uuid.UUID) error {
	return errors.New("twoPersonRoleStore.AddRole is not used by CanRemove tests")
}

func (f twoPersonRoleStore) RemoveRole(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (bool, error) {
	return false, errors.New("twoPersonRoleStore.RemoveRole is not used by CanRemove tests")
}

func (f twoPersonRoleStore) ChannelsForPerson(context.Context, uuid.UUID) ([]store.Channel, error) {
	return nil, errors.New("twoPersonRoleStore.ChannelsForPerson is not used by CanRemove tests")
}

func (f twoPersonRoleStore) RowID(context.Context, uuid.UUID, uuid.UUID) (uuid.UUID, bool, error) {
	return uuid.Nil, false, errors.New("twoPersonRoleStore.RowID is not used by CanRemove tests")
}

func (f twoPersonRoleStore) RowByID(context.Context, uuid.UUID) (store.ChannelPerson, error) {
	return store.ChannelPerson{}, errors.New("twoPersonRoleStore.RowByID is not used by CanRemove tests")
}

// TestAuthz_CanRemove_FullMatrix pins down every cell of FR33's removal
// matrix documented on CanRemove, plus the no-open-role-on-target case
// (FR33 idempotency) and self-removal in every actor role (never
// authorized, since a Founder or Co-Creator removing themselves lands in
// their own column, both of which are false).
func TestAuthz_CanRemove_FullMatrix(t *testing.T) {
	ctx := context.Background()
	channelID := uuid.New()

	tiers := []struct {
		name  string
		roles []store.Role
	}{
		{"Founder", []store.Role{store.RoleCreator}},
		{"Co-Creator", []store.Role{store.RoleCoCreator}},
		{"Analyst", []store.Role{store.RoleAnalyst}},
	}

	want := map[string]map[string]bool{
		"Founder":    {"Founder": false, "Co-Creator": true, "Analyst": true},
		"Co-Creator": {"Founder": false, "Co-Creator": false, "Analyst": true},
		"Analyst":    {"Founder": false, "Co-Creator": false, "Analyst": false},
	}

	for _, actorTier := range tiers {
		for _, targetTier := range tiers {
			name := actorTier.name + " removes " + targetTier.name
			t.Run(name, func(t *testing.T) {
				actorID, targetID := uuid.New(), uuid.New()
				rs := twoPersonRoleStore{
					channelID:  channelID,
					actorID:    actorID,
					targetID:   targetID,
					actorRoles: actorTier.roles,
					targetRoles: func() []store.Role {
						if actorTier.name == targetTier.name {
							// Self-removal: actor and target are the same
							// Person, so the target's role set is the
							// actor's own.
							return actorTier.roles
						}
						return targetTier.roles
					}(),
				}
				got, err := store.CanRemove(ctx, rs, channelID, actorID, targetID)
				require.NoError(t, err)
				assert.Equal(t, want[actorTier.name][targetTier.name], got)
			})
		}
	}

	t.Run("self-removal every tier -> false", func(t *testing.T) {
		for _, tier := range tiers {
			personID := uuid.New()
			rs := twoPersonRoleStore{
				channelID:   channelID,
				actorID:     personID,
				targetID:    personID,
				actorRoles:  tier.roles,
				targetRoles: tier.roles,
			}
			got, err := store.CanRemove(ctx, rs, channelID, personID, personID)
			require.NoError(t, err)
			assert.False(t, got, "%s removing themselves must never be authorized", tier.name)
		}
	})

	t.Run("target has no open role -> false, no error (FR33 idempotent no-op)", func(t *testing.T) {
		founderID, targetID := uuid.New(), uuid.New()
		rs := twoPersonRoleStore{
			channelID:   channelID,
			actorID:     founderID,
			targetID:    targetID,
			actorRoles:  []store.Role{store.RoleCreator},
			targetRoles: nil,
		}
		got, err := store.CanRemove(ctx, rs, channelID, founderID, targetID)
		require.NoError(t, err)
		assert.False(t, got, "a target with no open role must read false, which callers treat as an idempotent no-op, not an authorization failure")
	})
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
