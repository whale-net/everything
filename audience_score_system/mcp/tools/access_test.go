package tools

// Pure-Go coverage for access.go (issue #1718): flipRefBit's round-trip
// property and hasAccessRole's membership check, ChannelScopeID/
// IdempotencyKey for all three input types, mutate's argument validation
// (unparseable channel_id/person_id) and missing-caller-credential
// rejection driven against in-memory fakes, and each render function's
// ref-decoding logic (unflipped vs. flipped, and remove's bare-person-id
// fallback) -- entirely bypassing the MCP session/HTTP/auth plumbing and
// a real database. store.CanInvite/store.CanRemove's own authorization
// matrix (which requires a real *store.Person on ctx, only obtainable
// through the real auth middleware) and the full grant/revoke write path
// are access_integration_test.go's job (build tag "integration"), per
// issue #1718's Testing section.

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/audience_score_system/store"
)

// ── flipRefBit / hasAccessRole ───────────────────────────────────────────

func TestFlipRefBit_RoundTrips(t *testing.T) {
	id := uuid.New()
	flipped := flipRefBit(id)
	assert.NotEqual(t, id, flipped, "flipping must actually change the value")
	assert.Equal(t, id, flipRefBit(flipped), "flipping twice must recover the original id")
}

func TestFlipRefBit_NeverMapsUUIDNilToItself(t *testing.T) {
	assert.NotEqual(t, uuid.Nil, flipRefBit(uuid.Nil))
}

func TestHasAccessRole(t *testing.T) {
	roles := []store.Role{store.RoleAnalyst, store.RoleCoCreator}
	assert.True(t, hasAccessRole(roles, store.RoleAnalyst))
	assert.True(t, hasAccessRole(roles, store.RoleCoCreator))
	assert.False(t, hasAccessRole(roles, store.RoleCreator))
	assert.False(t, hasAccessRole(nil, store.RoleAnalyst))
}

// ── fakes ─────────────────────────────────────────────────────────────────

// fakeAccessRoleStore is a configurable, in-memory store.RoleStore stand-in
// scoped to what access.go's mutate/render functions need, keyed by
// personID (channelID is fixed within any one test).
type fakeAccessRoleStore struct {
	rolesByPerson map[uuid.UUID][]store.Role
	rolesErr      error

	rowIDByPerson map[uuid.UUID]uuid.UUID
	rowIDErr      error

	rowsByID   map[uuid.UUID]store.ChannelPerson
	rowByIDErr error

	addRoleErr   error
	addRoleCalls []addRoleCall

	removeResult bool
	removeErr    error
	removeCalls  []removeRoleCall
}

type addRoleCall struct {
	channelID, personID, grantedBy uuid.UUID
	role                           store.Role
}

type removeRoleCall struct {
	channelID, personID, revokedBy uuid.UUID
}

var _ store.RoleStore = (*fakeAccessRoleStore)(nil)

func (f *fakeAccessRoleStore) RolesFor(_ context.Context, _, personID uuid.UUID) ([]store.Role, error) {
	if f.rolesErr != nil {
		return nil, f.rolesErr
	}
	if f.rolesByPerson == nil {
		return nil, nil
	}
	return f.rolesByPerson[personID], nil
}

func (f *fakeAccessRoleStore) AddRole(_ context.Context, channelID, personID uuid.UUID, role store.Role, grantedBy uuid.UUID) error {
	f.addRoleCalls = append(f.addRoleCalls, addRoleCall{channelID, personID, grantedBy, role})
	return f.addRoleErr
}

func (f *fakeAccessRoleStore) RemoveRole(_ context.Context, channelID, personID, revokedBy uuid.UUID) (bool, error) {
	f.removeCalls = append(f.removeCalls, removeRoleCall{channelID, personID, revokedBy})
	return f.removeResult, f.removeErr
}

func (f *fakeAccessRoleStore) ChannelsForPerson(context.Context, uuid.UUID) ([]store.Channel, error) {
	return nil, errors.New("fakeAccessRoleStore.ChannelsForPerson is not used by these tests")
}

func (f *fakeAccessRoleStore) RowID(_ context.Context, _, personID uuid.UUID) (uuid.UUID, bool, error) {
	if f.rowIDErr != nil {
		return uuid.Nil, false, f.rowIDErr
	}
	id, ok := f.rowIDByPerson[personID]
	return id, ok, nil
}

func (f *fakeAccessRoleStore) RowByID(_ context.Context, id uuid.UUID) (store.ChannelPerson, error) {
	if f.rowByIDErr != nil {
		return store.ChannelPerson{}, f.rowByIDErr
	}
	row, ok := f.rowsByID[id]
	if !ok {
		return store.ChannelPerson{}, pgx.ErrNoRows
	}
	return row, nil
}

// fakeAccessInviteStore is a configurable, in-memory store.InviteStore
// stand-in scoped to what access.go's invite_co_creator functions need.
type fakeAccessInviteStore struct {
	byID map[uuid.UUID]store.Invite

	generateResult store.Invite
	generateErr    error

	liveForRoleResult store.Invite
	liveForRoleFound  bool
	liveForRoleErr    error
}

var _ store.InviteStore = (*fakeAccessInviteStore)(nil)

func (f *fakeAccessInviteStore) Generate(context.Context, uuid.UUID, uuid.UUID, store.Role) (store.Invite, error) {
	return f.generateResult, f.generateErr
}

func (f *fakeAccessInviteStore) Lookup(context.Context, string) (store.Invite, error) {
	return store.Invite{}, errors.New("fakeAccessInviteStore.Lookup is not used by these tests")
}

func (f *fakeAccessInviteStore) Consume(context.Context, string, uuid.UUID) error {
	return errors.New("fakeAccessInviteStore.Consume is not used by these tests")
}

func (f *fakeAccessInviteStore) GetByID(_ context.Context, id uuid.UUID) (store.Invite, error) {
	inv, ok := f.byID[id]
	if !ok {
		return store.Invite{}, pgx.ErrNoRows
	}
	return inv, nil
}

func (f *fakeAccessInviteStore) LiveForRole(context.Context, uuid.UUID, store.Role) (store.Invite, bool, error) {
	return f.liveForRoleResult, f.liveForRoleFound, f.liveForRoleErr
}

// fakeAccessPersonStore is a minimal store.PersonStore stand-in scoped to
// removeChannelPersonRender's bare-person-id fallback.
type fakeAccessPersonStore struct {
	byID map[uuid.UUID]store.Person
	err  error
}

var _ store.PersonStore = fakeAccessPersonStore{}

func (f fakeAccessPersonStore) UpsertByGoogleSubject(context.Context, string, string, string) (store.Person, bool, error) {
	return store.Person{}, false, errors.New("fakeAccessPersonStore.UpsertByGoogleSubject is not used by these tests")
}

func (f fakeAccessPersonStore) GetByID(_ context.Context, id uuid.UUID) (store.Person, error) {
	if f.err != nil {
		return store.Person{}, f.err
	}
	p, ok := f.byID[id]
	if !ok {
		return store.Person{}, pgx.ErrNoRows
	}
	return p, nil
}

// ── ChannelScopeID / IdempotencyKey ─────────────────────────────────────

func TestAccessInputs_ChannelScopeID(t *testing.T) {
	id := uuid.New()

	assert.Equal(t, id, InviteCoCreatorInput{ChannelID: id.String()}.ChannelScopeID())
	assert.Equal(t, uuid.Nil, InviteCoCreatorInput{ChannelID: "not-a-uuid"}.ChannelScopeID())

	assert.Equal(t, id, PromoteToCoCreatorInput{ChannelID: id.String()}.ChannelScopeID())
	assert.Equal(t, uuid.Nil, PromoteToCoCreatorInput{ChannelID: "not-a-uuid"}.ChannelScopeID())

	assert.Equal(t, id, RemoveChannelPersonInput{ChannelID: id.String()}.ChannelScopeID())
	assert.Equal(t, uuid.Nil, RemoveChannelPersonInput{ChannelID: "not-a-uuid"}.ChannelScopeID())
}

func TestAccessInputs_IdempotencyKey(t *testing.T) {
	assert.Equal(t, "abc", InviteCoCreatorInput{IdempotencyKeyArg: "abc"}.IdempotencyKey())
	assert.Equal(t, "abc", PromoteToCoCreatorInput{IdempotencyKeyArg: "abc"}.IdempotencyKey())
	assert.Equal(t, "abc", RemoveChannelPersonInput{IdempotencyKeyArg: "abc"}.IdempotencyKey())
	assert.Empty(t, InviteCoCreatorInput{}.IdempotencyKey())
}

// ── invite_co_creator ─────────────────────────────────────────────────────

func TestInviteCoCreatorMutate_InvalidChannelID_Rejected(t *testing.T) {
	invites := &fakeAccessInviteStore{}
	roles := &fakeAccessRoleStore{}
	mutate := inviteCoCreatorMutate(invites, roles)

	_, err := mutate(context.Background(), InviteCoCreatorInput{ChannelID: "not-a-uuid"})
	require.Error(t, err)
}

func TestInviteCoCreatorMutate_MissingCaller_Rejected(t *testing.T) {
	invites := &fakeAccessInviteStore{}
	roles := &fakeAccessRoleStore{}
	mutate := inviteCoCreatorMutate(invites, roles)

	_, err := mutate(context.Background(), InviteCoCreatorInput{ChannelID: uuid.New().String()})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unauthenticated")
}

func TestInviteCoCreatorRender_UnflippedRef_FreshlyMinted(t *testing.T) {
	inv := store.Invite{ID: uuid.New(), ChannelID: uuid.New(), Code: "abc123", Role: store.RoleCoCreator}
	invites := &fakeAccessInviteStore{byID: map[uuid.UUID]store.Invite{inv.ID: inv}}
	render := inviteCoCreatorRender(invites)

	_, out, err := render(context.Background(), inv.ID)
	require.NoError(t, err)
	assert.False(t, out.AlreadyLive, "an unflipped ref must render as freshly minted")
	assert.Equal(t, "abc123", out.Code)
	assert.Equal(t, "co_creator", out.Role)
	assert.Equal(t, inv.ChannelID.String(), out.ChannelID)
}

func TestInviteCoCreatorRender_FlippedRef_AlreadyLive(t *testing.T) {
	inv := store.Invite{ID: uuid.New(), ChannelID: uuid.New(), Code: "xyz789", Role: store.RoleCoCreator}
	invites := &fakeAccessInviteStore{byID: map[uuid.UUID]store.Invite{inv.ID: inv}}
	render := inviteCoCreatorRender(invites)

	_, out, err := render(context.Background(), flipRefBit(inv.ID))
	require.NoError(t, err)
	assert.True(t, out.AlreadyLive, "a flipped ref must render as already_live")
	assert.Equal(t, "xyz789", out.Code)
}

// ── promote_to_co_creator ───────────────────────────────────────────────

func TestPromoteToCoCreatorMutate_InvalidChannelID_Rejected(t *testing.T) {
	roles := &fakeAccessRoleStore{}
	mutate := promoteToCoCreatorMutate(roles)

	_, err := mutate(context.Background(), PromoteToCoCreatorInput{ChannelID: "not-a-uuid", PersonID: uuid.New().String()})
	require.Error(t, err)
}

func TestPromoteToCoCreatorMutate_InvalidPersonID_Rejected(t *testing.T) {
	roles := &fakeAccessRoleStore{}
	mutate := promoteToCoCreatorMutate(roles)

	_, err := mutate(context.Background(), PromoteToCoCreatorInput{ChannelID: uuid.New().String(), PersonID: "not-a-uuid"})
	require.Error(t, err)
}

func TestPromoteToCoCreatorMutate_MissingCaller_Rejected(t *testing.T) {
	roles := &fakeAccessRoleStore{}
	mutate := promoteToCoCreatorMutate(roles)

	_, err := mutate(context.Background(), PromoteToCoCreatorInput{ChannelID: uuid.New().String(), PersonID: uuid.New().String()})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unauthenticated")
	assert.Empty(t, roles.addRoleCalls, "no grant may happen without a resolved caller")
}

func TestPromoteToCoCreatorRender_UnflippedRef_Changed(t *testing.T) {
	channelID, personID, rowID := uuid.New(), uuid.New(), uuid.New()
	roles := &fakeAccessRoleStore{rowsByID: map[uuid.UUID]store.ChannelPerson{
		rowID: {ID: rowID, ChannelID: channelID, PersonID: personID, Role: store.RoleCoCreator},
	}}
	render := promoteToCoCreatorRender(roles)

	_, out, err := render(context.Background(), rowID)
	require.NoError(t, err)
	assert.True(t, out.Changed)
	assert.Equal(t, channelID.String(), out.ChannelID)
	assert.Equal(t, personID.String(), out.PersonID)
	assert.Equal(t, "co_creator", out.Role)
}

func TestPromoteToCoCreatorRender_FlippedRef_UnchangedNoOp(t *testing.T) {
	channelID, personID, rowID := uuid.New(), uuid.New(), uuid.New()
	roles := &fakeAccessRoleStore{rowsByID: map[uuid.UUID]store.ChannelPerson{
		rowID: {ID: rowID, ChannelID: channelID, PersonID: personID, Role: store.RoleCoCreator},
	}}
	render := promoteToCoCreatorRender(roles)

	_, out, err := render(context.Background(), flipRefBit(rowID))
	require.NoError(t, err)
	assert.False(t, out.Changed, "a flipped ref must render as the already-co_creator no-op")
	assert.Equal(t, channelID.String(), out.ChannelID)
}

func TestPromoteToCoCreatorRender_NeitherRefFound_Errors(t *testing.T) {
	roles := &fakeAccessRoleStore{rowsByID: map[uuid.UUID]store.ChannelPerson{}}
	render := promoteToCoCreatorRender(roles)

	_, _, err := render(context.Background(), uuid.New())
	require.Error(t, err, "a successful promote_to_co_creator always has a row to find one of the two ways -- a miss on both is a bug, not a valid no-op")
}

// ── remove_channel_person ───────────────────────────────────────────────

func TestRemoveChannelPersonMutate_InvalidChannelID_Rejected(t *testing.T) {
	roles := &fakeAccessRoleStore{}
	mutate := removeChannelPersonMutate(roles)

	_, err := mutate(context.Background(), RemoveChannelPersonInput{ChannelID: "not-a-uuid", PersonID: uuid.New().String()})
	require.Error(t, err)
}

func TestRemoveChannelPersonMutate_InvalidPersonID_Rejected(t *testing.T) {
	roles := &fakeAccessRoleStore{}
	mutate := removeChannelPersonMutate(roles)

	_, err := mutate(context.Background(), RemoveChannelPersonInput{ChannelID: uuid.New().String(), PersonID: "not-a-uuid"})
	require.Error(t, err)
}

func TestRemoveChannelPersonMutate_MissingCaller_Rejected(t *testing.T) {
	roles := &fakeAccessRoleStore{}
	mutate := removeChannelPersonMutate(roles)

	_, err := mutate(context.Background(), RemoveChannelPersonInput{ChannelID: uuid.New().String(), PersonID: uuid.New().String()})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unauthenticated")
	assert.Empty(t, roles.removeCalls, "no removal may happen without a resolved caller")
}

func TestRemoveChannelPersonRender_UnflippedRef_Removed(t *testing.T) {
	channelID, personID, rowID := uuid.New(), uuid.New(), uuid.New()
	roles := &fakeAccessRoleStore{rowsByID: map[uuid.UUID]store.ChannelPerson{
		rowID: {ID: rowID, ChannelID: channelID, PersonID: personID, Role: store.RoleAnalyst},
	}}
	persons := fakeAccessPersonStore{}
	render := removeChannelPersonRender(roles, persons)

	_, out, err := render(context.Background(), rowID)
	require.NoError(t, err)
	assert.True(t, out.Removed)
	assert.Equal(t, channelID.String(), out.ChannelID)
	assert.Equal(t, personID.String(), out.PersonID)
}

func TestRemoveChannelPersonRender_FlippedRef_AlreadyRemovedNoOp(t *testing.T) {
	channelID, personID, rowID := uuid.New(), uuid.New(), uuid.New()
	roles := &fakeAccessRoleStore{rowsByID: map[uuid.UUID]store.ChannelPerson{
		rowID: {ID: rowID, ChannelID: channelID, PersonID: personID, Role: store.RoleAnalyst},
	}}
	persons := fakeAccessPersonStore{}
	render := removeChannelPersonRender(roles, persons)

	_, out, err := render(context.Background(), flipRefBit(rowID))
	require.NoError(t, err)
	assert.False(t, out.Removed, "a flipped ref must render as the already-removed no-op")
	assert.Equal(t, channelID.String(), out.ChannelID)
}

func TestRemoveChannelPersonRender_NeitherRefFound_FallsBackToBarePersonID(t *testing.T) {
	roles := &fakeAccessRoleStore{rowsByID: map[uuid.UUID]store.ChannelPerson{}}
	target := store.Person{ID: uuid.New(), DisplayName: "Never A Member"}
	persons := fakeAccessPersonStore{byID: map[uuid.UUID]store.Person{target.ID: target}}
	render := removeChannelPersonRender(roles, persons)

	_, out, err := render(context.Background(), target.ID)
	require.NoError(t, err)
	assert.False(t, out.Removed)
	assert.Equal(t, target.ID.String(), out.PersonID)
	assert.Empty(t, out.ChannelID, "no channel_person row exists to anchor a fresh channel_id read to -- see removeChannelPersonRender's doc comment")
}
