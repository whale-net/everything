package testpg

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStart_BasicConnectivity(t *testing.T) {
	pg := Start(t)
	defer pg.Close()

	ctx := context.Background()

	// Verify we can execute queries
	var result int
	err := pg.Pool().QueryRow(ctx, "SELECT 1").Scan(&result)
	require.NoError(t, err)
	assert.Equal(t, 1, result)
}

func TestStart_CustomCredentials(t *testing.T) {
	pg := Start(t, WithCredentials("myuser", "mypass", "mydb"))
	defer pg.Close()

	ctx := context.Background()

	// Verify the database name matches
	var dbName string
	err := pg.Pool().QueryRow(ctx, "SELECT current_database()").Scan(&dbName)
	require.NoError(t, err)
	assert.Equal(t, "mydb", dbName)

	// Verify the user matches
	var user string
	err = pg.Pool().QueryRow(ctx, "SELECT current_user").Scan(&user)
	require.NoError(t, err)
	assert.Equal(t, "myuser", user)
}

func TestStart_CreateTableAndInsert(t *testing.T) {
	pg := Start(t)
	defer pg.Close()

	ctx := context.Background()

	// Create a table
	_, err := pg.Pool().Exec(ctx, `
		CREATE TABLE test_items (
			id SERIAL PRIMARY KEY,
			name TEXT NOT NULL
		)
	`)
	require.NoError(t, err)

	// Insert data
	_, err = pg.Pool().Exec(ctx, "INSERT INTO test_items (name) VALUES ($1)", "hello")
	require.NoError(t, err)

	// Query it back
	var name string
	err = pg.Pool().QueryRow(ctx, "SELECT name FROM test_items WHERE id = 1").Scan(&name)
	require.NoError(t, err)
	assert.Equal(t, "hello", name)
}

func TestConnString_IsUsable(t *testing.T) {
	pg := Start(t)
	defer pg.Close()

	// ConnString should be a valid postgres URL
	connStr := pg.ConnString()
	assert.Contains(t, connStr, "postgres://")
	assert.Contains(t, connStr, "sslmode=disable")
}
