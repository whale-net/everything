//go:build integration

// Integration test for the remote transport, against a real worker.
//
// //libs/go/monty/monty_integration_test covers the local subprocess path;
// this covers the same sandbox reached over a WebSocket. The relay below
// reproduces what monty-server does -- one binary message per protobuf body,
// no length prefix -- so the remote path is exercisable end to end without a
// commercial server.
//
// Requires the worker binary:
//
//	uv tool install pydantic-monty-runtime    # or: pip install pydantic-monty-runtime
//	export MONTY_BIN=$(command -v monty)
//
// Run with:
//
//	bazel test //libs/go/monty/remote:remote_integration_test --test_output=all
package remote

import (
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/libs/go/monty"
)

func TestRemoteTransportRunsPython(t *testing.T) {
	url := startRelay(t, montyBinary(t))

	pool, err := monty.NewPool(monty.Config{
		NewConn: Dialer(url),
		Limits: monty.Limits{
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

	// Globals persist across a turn, the same as on the local transport.
	_, err = session.Run(ctx, "total = 0", monty.RunOptions{})
	require.NoError(t, err)

	res, err := session.Run(ctx, "sum(range(101))", monty.RunOptions{})
	require.NoError(t, err)
	assert.Equal(t, int64(5050), res.Value)

	res, err = session.Run(ctx, "sorted(['pear', 'apple'])", monty.RunOptions{})
	require.NoError(t, err)
	assert.Equal(t, monty.Tuple{"apple", "pear"}, res.Value)
}

func TestRemoteHostFunction(t *testing.T) {
	pool, err := monty.NewPool(monty.Config{NewConn: Dialer(startRelay(t, montyBinary(t)))})
	require.NoError(t, err)
	defer pool.Close()

	ctx := context.Background()
	session, err := pool.Checkout(ctx)
	require.NoError(t, err)
	defer session.Close()

	res, err := session.Run(ctx, "double(21)", monty.RunOptions{
		Host: func(_ context.Context, call *monty.Call) (any, error) {
			if call.Name != "double" {
				return nil, monty.ErrNotFound
			}
			return call.Args[0].(int64) * 2, nil
		},
	})
	require.NoError(t, err)
	assert.Equal(t, int64(42), res.Value)
}

// TestRemotePrintStreams checks that print() output survives the hop with its
// stream and its order intact -- a relay that reorders or merges the two
// streams would still pass a value-only test.
func TestRemotePrintStreams(t *testing.T) {
	pool, err := monty.NewPool(monty.Config{NewConn: Dialer(startRelay(t, montyBinary(t)))})
	require.NoError(t, err)
	defer pool.Close()

	ctx := context.Background()
	session, err := pool.Checkout(ctx)
	require.NoError(t, err)
	defer session.Close()

	res, err := session.Run(ctx, "import sys; print('a'); print('b', file=sys.stderr); print('c')",
		monty.RunOptions{})
	require.NoError(t, err)
	assert.Equal(t, []monty.Output{
		{Stream: monty.StreamStdout, Text: "a"},
		{Stream: monty.StreamStderr, Text: "b"},
		{Stream: monty.StreamStdout, Text: "c"},
	}, res.Prints)
}

// startRelay bridges WebSocket connections to fresh `monty subprocess`
// children. This is the server side of the remote-worker hop: it translates
// framing only, never parses the protobuf, and is what monty-server does
// internally (plus capacity limits, quotas and session storage).
func startRelay(t *testing.T, binary string) string {
	t.Helper()
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		bridge(t, ws, binary)
	}))
	t.Cleanup(server.Close)
	return "ws://" + server.Listener.Addr().String() + "/"
}

func bridge(t *testing.T, ws *websocket.Conn, montyBin string) {
	t.Helper()
	child := exec.Command(montyBin, "subprocess")
	child.Env = []string{}
	child.Stderr = os.Stderr
	stdin, err := child.StdinPipe()
	if err != nil {
		return
	}
	stdout, err := child.StdoutPipe()
	if err != nil {
		return
	}
	if err := child.Start(); err != nil {
		return
	}
	defer func() {
		_ = child.Process.Kill()
		_, _ = child.Process.Wait()
	}()

	done := make(chan struct{}, 2)
	// WebSocket -> child: add the 4-byte little-endian length prefix.
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			typ, body, err := ws.ReadMessage()
			if err != nil || typ != websocket.BinaryMessage {
				return
			}
			var prefix [4]byte
			binary.LittleEndian.PutUint32(prefix[:], uint32(len(body)))
			if _, err := stdin.Write(prefix[:]); err != nil {
				return
			}
			if _, err := stdin.Write(body); err != nil {
				return
			}
		}
	}()
	// child -> WebSocket: strip it, one binary message per frame.
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			var prefix [4]byte
			if _, err := io.ReadFull(stdout, prefix[:]); err != nil {
				return
			}
			body := make([]byte, binary.LittleEndian.Uint32(prefix[:]))
			if _, err := io.ReadFull(stdout, body); err != nil {
				return
			}
			if err := ws.WriteMessage(websocket.BinaryMessage, body); err != nil {
				return
			}
		}
	}()
	<-done
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
