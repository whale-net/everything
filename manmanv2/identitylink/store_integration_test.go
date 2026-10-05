//go:build integration

// Postgres-backed coverage for the identity-link store.
//
//	bazel test //manmanv2/identitylink:store_integration_test --test_output=all
package identitylink

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
	"github.com/whale-net/everything/manmanv2/migrate/schema"
)

func newStore(t *testing.T) Store {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{})
	db, err := sql.Open("pgx", pg.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, migrate.NewRunner(db, schema.Migrations, schema.Dir).Up())
	return Store{DB: db}
}

func TestStore_LinkResolve(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	_, found, err := s.Resolve(ctx, "iss", "sub")
	require.NoError(t, err)
	assert.False(t, found)

	out, err := s.Link(ctx, "iss", "sub", "user-1")
	require.NoError(t, err)
	assert.Equal(t, Created, out)

	out, err = s.Link(ctx, "iss", "sub", "user-1")
	require.NoError(t, err)
	assert.Equal(t, AlreadyLinked, out)

	_, err = s.Link(ctx, "iss", "sub", "user-2")
	assert.ErrorIs(t, err, ErrLinkedToOtherUser)

	// Same sub under another issuer is a distinct identity.
	_, found, err = s.Resolve(ctx, "other-iss", "sub")
	require.NoError(t, err)
	assert.False(t, found)

	got, found, err := s.Resolve(ctx, "iss", "sub")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "user-1", got)
}

func TestStore_Unlink(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	removed, err := s.Unlink(ctx, "iss", "sub")
	require.NoError(t, err)
	assert.False(t, removed)

	_, err = s.Link(ctx, "iss", "sub", "user-1")
	require.NoError(t, err)
	_, err = s.Link(ctx, "iss", "other", "user-1")
	require.NoError(t, err)

	removed, err = s.Unlink(ctx, "iss", "sub")
	require.NoError(t, err)
	assert.True(t, removed)

	_, found, err := s.Resolve(ctx, "iss", "sub")
	require.NoError(t, err)
	assert.False(t, found)
	_, found, err = s.Resolve(ctx, "iss", "other")
	require.NoError(t, err)
	assert.True(t, found, "other identities stay linked")

	// Relinking after an unlink works, including to a different user.
	out, err := s.Link(ctx, "iss", "sub", "user-2")
	require.NoError(t, err)
	assert.Equal(t, Created, out)
}

func TestStore_ConsumeReplayAndReap(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	consumed, err := s.IsConsumed(ctx, "j1")
	require.NoError(t, err)
	assert.False(t, consumed)

	require.NoError(t, s.Consume(ctx, "j1", time.Now().Add(time.Minute)))
	assert.ErrorIs(t, s.Consume(ctx, "j1", time.Now().Add(time.Minute)), ErrAssertionConsumed)

	consumed, err = s.IsConsumed(ctx, "j1")
	require.NoError(t, err)
	assert.True(t, consumed)

	require.NoError(t, s.Consume(ctx, "old", time.Now().Add(-time.Minute)))
	n, err := s.ReapExpired(ctx, time.Now())
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
}

func TestMigration_DownUpRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{})
	db, err := sql.Open("pgx", pg.ConnString)
	require.NoError(t, err)
	defer db.Close()

	runner := migrate.NewRunner(db, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up())

	exists := func(name string) bool {
		var ok bool
		require.NoError(t, db.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = $1)`, name).Scan(&ok))
		return ok
	}
	assert.True(t, exists("whagent_identity_link"))
	assert.True(t, exists("whagent_link_assertion"))

	require.NoError(t, runner.Migrate(48))
	assert.False(t, exists("whagent_identity_link"))
	assert.False(t, exists("whagent_link_assertion"))
	require.NoError(t, runner.Migrate(49))
	assert.True(t, exists("whagent_identity_link"))
}
