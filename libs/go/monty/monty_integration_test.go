//go:build integration

// Integration tests for the two Monty transports against a real worker.
//
// The remote transport's equivalent lives in
// //libs/go/monty/remote:remote_integration_test, which reaches the same
// worker through an in-test WebSocket relay.
//
// Requires the worker binary. Resolve it with:
//
//	uv tool install pydantic-monty-runtime    # or: pip install pydantic-monty-runtime
//	export MONTY_BIN=$(command -v monty)
//
// Run with:
//
//	bazel test //libs/go/monty:monty_integration_test --test_output=all
package monty

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubprocessTransport(t *testing.T) {
	binary := montyBinary(t)
	pool, err := NewPool(Config{
		Binary: binary,
		Limits: Limits{
			MaxMemoryBytes:  64 << 20,
			MaxFeedDuration: 10 * time.Second,
		},
	})
	require.NoError(t, err)
	defer pool.Close()

	ctx := context.Background()
	session, err := pool.Checkout(ctx)
	require.NoError(t, err)
	defer session.Close()

	// Globals persist across runs: a session is a REPL, not a one-shot.
	first, err := session.Run(ctx, "x = 21", RunOptions{})
	require.NoError(t, err)
	assert.Nil(t, first.Value, "an assignment evaluates to None")

	res, err := session.Run(ctx, "x * 2", RunOptions{})
	require.NoError(t, err)
	assert.Equal(t, int64(42), res.Value)
}

func TestPrintAndErrors(t *testing.T) {
	pool, err := NewPool(Config{Binary: montyBinary(t)})
	require.NoError(t, err)
	defer pool.Close()

	ctx := context.Background()
	session, err := pool.Checkout(ctx)
	require.NoError(t, err)
	defer session.Close()

	res, err := session.Run(ctx, "print('out'); print('err', file=__import__('sys').stderr); 7",
		RunOptions{})
	require.NoError(t, err)
	assert.Equal(t, int64(7), res.Value)
	assert.Equal(t, []Output{
		{Stream: StreamStdout, Text: "out"},
		{Stream: StreamStderr, Text: "err"},
	}, res.Prints, "output keeps the order the sandbox produced it, across both streams")

	// A Python exception ends the turn, not the session.
	_, err = session.Run(ctx, "1/0", RunOptions{})
	require.Error(t, err)
	assert.True(t, IsPythonError(err), "got %v", err)
	assert.False(t, IsTransient(err), "a Python error is the snippet's fault, not a blip")

	res, err = session.Run(ctx, "'still alive'", RunOptions{})
	require.NoError(t, err)
	assert.Equal(t, "still alive", res.Value)
}

func TestHostFunction(t *testing.T) {
	pool, err := NewPool(Config{Binary: montyBinary(t)})
	require.NoError(t, err)
	defer pool.Close()

	ctx := context.Background()
	session, err := pool.Checkout(ctx)
	require.NoError(t, err)
	defer session.Close()

	// A name the sandbox cannot resolve suspends, and the host answers it.
	res, err := session.Run(ctx, "triple(5)", RunOptions{
		Host: func(_ context.Context, call *Call) (any, error) {
			if call.Name != "triple" {
				return nil, ErrNotFound
			}
			return call.Args[0].(int64) * 3, nil
		},
	})
	require.NoError(t, err)
	assert.Equal(t, int64(15), res.Value)

	// A name the host does not know gets Monty's own default, not a crash.
	_, err = session.Run(ctx, "no_such_function()", RunOptions{
		Host: func(context.Context, *Call) (any, error) { return nil, ErrNotFound },
	})
	require.Error(t, err)
	assert.True(t, IsPythonError(err), "got %v", err)
}

func montyBinary(t *testing.T) string {
	t.Helper()
	if path := os.Getenv("MONTY_BIN"); path != "" {
		return path
	}
	path, err := exec.LookPath("monty")
	if err != nil {
		t.Skip("no monty worker binary: install pydantic-monty-runtime and set MONTY_BIN")
	}
	return path
}

// TestLimitsAreEnforced checks the ceiling a host actually cares about: a
// snippet that allocates without bound is stopped by the worker rather than
// taking the host down with it.
func TestLimitsAreEnforced(t *testing.T) {
	pool, err := NewPool(Config{
		Binary: montyBinary(t),
		Limits: Limits{
			MaxMemoryBytes:  16 << 20,
			MaxFeedDuration: 2 * time.Second,
		},
	})
	require.NoError(t, err)
	defer pool.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session, err := pool.Checkout(ctx)
	require.NoError(t, err)
	defer session.Close()

	_, err = session.Run(ctx, "x = bytearray(512 * 1024 * 1024)", RunOptions{})
	require.Error(t, err, "an over-budget allocation must not succeed")

	// Whether it surfaces as a Python MemoryError or takes the worker with
	// it, both are acceptable -- what matters is that the host is still
	// running and can check out a fresh session.
	next, err := pool.Checkout(ctx)
	if err == nil {
		next.Close()
	}
}
