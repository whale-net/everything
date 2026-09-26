package monty

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	pb "github.com/whale-net/everything/libs/go/monty/protos"
)

// fakeConn replays a scripted list of child events and records what was sent.
// The protocol is strict alternation, so a flat script in the order the events
// are expected is enough to drive a whole turn.
type fakeConn struct {
	mu     sync.Mutex
	sent   []*pb.ParentRequest
	script []*pb.ChildEvent
	next   int
	closed bool
	alive  bool

	// blockRecv makes an exhausted script block until ctx is done instead of
	// reporting ErrCrashed, so a test can cancel a turn that is genuinely in
	// flight rather than one that has already run out of events.
	blockRecv bool
}

func newFakeConn(events ...*pb.ChildEvent) *fakeConn {
	return &fakeConn{script: events, alive: true}
}

func (c *fakeConn) Send(ctx context.Context, req *pb.ParentRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return ErrClosed
	}
	c.sent = append(c.sent, req)
	return nil
}

func (c *fakeConn) Recv(ctx context.Context) (*pb.ChildEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.next < len(c.script) {
		ev := c.script[c.next]
		c.next++
		c.mu.Unlock()
		return ev, nil
	}
	block := c.blockRecv
	c.mu.Unlock()
	if block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return nil, ErrCrashed
}

func (c *fakeConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	c.alive = false
	return nil
}

func (c *fakeConn) Alive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.alive
}

// requests returns a copy of everything sent so far.
func (c *fakeConn) requests() []*pb.ParentRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*pb.ParentRequest(nil), c.sent...)
}

// awaitRequests blocks until at least n requests have been sent, so a test can
// cancel a turn that is provably mid-flight rather than guessing at a sleep.
func (c *fakeConn) awaitRequests(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(c.requests()) >= n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d requests, got %d", n, len(c.requests()))
}

// ---- event builders ----

func evtOK() *pb.ChildEvent {
	return &pb.ChildEvent{Kind: &pb.ChildEvent_Ok{Ok: &pb.Ok{}}}
}

func evtComplete(value uint32, arena *pb.Arena) *pb.ChildEvent {
	return &pb.ChildEvent{Kind: &pb.ChildEvent_Complete{Complete: &pb.Complete{
		Value: value, Values: arena,
	}}}
}

func evtError(excType, msg string) *pb.ChildEvent {
	return &pb.ChildEvent{Kind: &pb.ChildEvent_Error{Error: &pb.Error{
		Exception: &pb.RaisedException{ExcType: excType, Message: strPtr(msg)},
	}}}
}

func seg(stream pb.PrintStream, text string) *pb.PrintSegment {
	return &pb.PrintSegment{Stream: stream, Text: text}
}

func evtPrint(stream pb.PrintStream, text string) *pb.ChildEvent {
	return evtPrintSegs(seg(stream, text))
}

func evtPrintSegs(segs ...*pb.PrintSegment) *pb.ChildEvent {
	return &pb.ChildEvent{Kind: &pb.ChildEvent_Print{Print: &pb.Print{Segments: segs}}}
}

func evtFunctionCall(name string, callID uint32, args []uint32, arena *pb.Arena) *pb.ChildEvent {
	return &pb.ChildEvent{Kind: &pb.ChildEvent_FunctionCall{FunctionCall: &pb.FunctionCall{
		FunctionName: name,
		Args:         args,
		CallId:       callID,
		Values:       arena,
		Position:     &pb.SourceRange{Filename: "feed.py", Start: 10, End: 14},
	}}}
}

func evtFunctionCallKwargs(name string, callID uint32, arena *pb.Arena, pairs ...*pb.NodePair) *pb.ChildEvent {
	ev := evtFunctionCall(name, callID, nil, arena)
	ev.GetFunctionCall().Kwargs = pairs
	return ev
}

func evtOsCall(call *pb.OsCall) *pb.ChildEvent {
	if call.Position == nil {
		call.Position = &pb.SourceRange{Filename: "feed.py", Start: 1, End: 5}
	}
	return &pb.ChildEvent{Kind: &pb.ChildEvent_OsCall{OsCall: call}}
}

func evtNameLookup(name string) *pb.ChildEvent {
	return &pb.ChildEvent{Kind: &pb.ChildEvent_NameLookup{NameLookup: &pb.NameLookup{
		Name:     name,
		Position: &pb.SourceRange{Filename: "feed.py", Start: 3, End: 9},
	}}}
}

func evtResolveFutures(pending ...uint32) *pb.ChildEvent {
	return &pb.ChildEvent{Kind: &pb.ChildEvent_ResolveFutures{ResolveFutures: &pb.ResolveFutures{
		PendingCallIds: pending,
	}}}
}

func evtShutdown() *pb.ChildEvent {
	return &pb.ChildEvent{Kind: &pb.ChildEvent_Shutdown{Shutdown: &pb.ShutdownDump{}}}
}

func evtDumpResult(state []byte) *pb.ChildEvent {
	return &pb.ChildEvent{Kind: &pb.ChildEvent_DumpResult{DumpResult: &pb.DumpResult{State: state}}}
}

func evtFatal(msg string) *pb.ChildEvent {
	return &pb.ChildEvent{Kind: &pb.ChildEvent_FatalError{FatalError: &pb.FatalError{Message: msg}}}
}

// evtKindless is a ChildEvent with no oneof arm set: a protocol violation that
// only a transport that fails to validate can hand to a session.
func evtKindless() *pb.ChildEvent { return &pb.ChildEvent{} }

// mustArena encodes values the way the sandbox would, so a test can build the
// arena a scripted event carries.
func mustArena(t *testing.T, values ...any) (*pb.Arena, []uint32) {
	t.Helper()
	arena, roots, err := EncodeArena(values)
	require.NoError(t, err)
	return arena, roots
}

// newTestSession wires a session to a scripted conn without going through a
// Pool, so Limits are exactly what the test asked for.
func newTestSession(conn *fakeConn, limits Limits) *Session {
	if limits.MaxSuspensions <= 0 {
		limits.MaxSuspensions = defaultSuspensionLimit
	}
	return &Session{conn: conn, limits: limits}
}

func TestRunReturnsValue(t *testing.T) {
	arena, roots := mustArena(t, int64(42))
	conn := newFakeConn(evtComplete(roots[0], arena))
	s := newTestSession(conn, Limits{})

	res, err := s.Run(context.Background(), "40 + 2", RunOptions{})
	require.NoError(t, err)
	assert.Equal(t, int64(42), res.Value)
	assert.Empty(t, res.Prints)

	require.Len(t, conn.requests(), 1)
	feed := conn.requests()[0].GetFeed()
	require.NotNil(t, feed)
	assert.Equal(t, "40 + 2", feed.GetCode())
	assert.False(t, feed.GetSkipTypeCheck())
}

func TestRunCompleteNoneIsNilValue(t *testing.T) {
	// Python's None is a legitimate result, so a nil Value must not be
	// confused with a failure: the error is nil and the prints are there.
	conn := newFakeConn(
		evtPrint(pb.PrintStream_PRINT_STREAM_STDOUT, "None\n"),
		evtComplete(0, &pb.Arena{NodeCount: 1, Nodes: []*pb.MontyNode{{Kind: &pb.MontyNode_None{None: &pb.Unit{}}}}}),
	)
	s := newTestSession(conn, Limits{})

	res, err := s.Run(context.Background(), "None", RunOptions{})
	require.NoError(t, err)
	assert.Nil(t, res.Value)
	assert.Len(t, res.Prints, 1)
}

func TestRunSkipsTypeCheckAndCwd(t *testing.T) {
	no := false
	arena, roots := mustArena(t, true)
	conn := newFakeConn(evtComplete(roots[0], arena))
	s := newTestSession(conn, Limits{})

	_, err := s.Run(context.Background(), "x", RunOptions{
		TypeCheck: &no,
		CWD:       "/work",
	})
	require.NoError(t, err)
	feed := conn.requests()[0].GetFeed()
	assert.True(t, feed.GetSkipTypeCheck(), "TypeCheck=false asks the worker to skip the check")
	assert.Equal(t, "/work", feed.GetCwd())
}

// TestRunPrintsPreserveEmissionOrder is the property that makes Output a
// sequence rather than two buffers: one wire event can carry several runs that
// alternate between the streams, and grouping them by stream would reorder the
// sandbox's output.
func TestRunPrintsPreserveEmissionOrder(t *testing.T) {
	arena, roots := mustArena(t, nil)
	conn := newFakeConn(
		evtPrintSegs(
			seg(pb.PrintStream_PRINT_STREAM_STDOUT, "out-1 "),
			seg(pb.PrintStream_PRINT_STREAM_STDERR, "err-1\n"),
			seg(pb.PrintStream_PRINT_STREAM_STDOUT, "out-2 "),
			seg(pb.PrintStream_PRINT_STREAM_STDERR, "err-2\n"),
		),
		evtComplete(roots[0], arena),
	)
	s := newTestSession(conn, Limits{})

	res, err := s.Run(context.Background(), "f()", RunOptions{})
	require.NoError(t, err)
	assert.Equal(t, []Output{
		{Stream: StreamStdout, Text: "out-1 "},
		{Stream: StreamStderr, Text: "err-1\n"},
		{Stream: StreamStdout, Text: "out-2 "},
		{Stream: StreamStderr, Text: "err-2\n"},
	}, res.Prints, "segments keep their order, interleaving and all")
}

func TestRunPrintsSpanEvents(t *testing.T) {
	arena, roots := mustArena(t, nil)
	conn := newFakeConn(
		evtPrint(pb.PrintStream_PRINT_STREAM_STDOUT, "a"),
		evtPrint(pb.PrintStream_PRINT_STREAM_STDOUT, "b"),
		evtComplete(roots[0], arena),
	)
	s := newTestSession(conn, Limits{})

	res, err := s.Run(context.Background(), "f()", RunOptions{})
	require.NoError(t, err)
	assert.Equal(t, []Output{
		{Stream: StreamStdout, Text: "a"},
		{Stream: StreamStdout, Text: "b"},
	}, res.Prints)
}

func TestRunOnPrintSeesEverySegmentAndPrintsStaysEmpty(t *testing.T) {
	arena, roots := mustArena(t, nil)
	conn := newFakeConn(
		evtPrintSegs(
			seg(pb.PrintStream_PRINT_STREAM_STDOUT, "x"),
			seg(pb.PrintStream_PRINT_STREAM_STDERR, "y"),
		),
		evtComplete(roots[0], arena),
	)
	s := newTestSession(conn, Limits{})

	var got []Output
	res, err := s.Run(context.Background(), "f()", RunOptions{
		OnPrint: func(stream Stream, text string) {
			got = append(got, Output{Stream: stream, Text: text})
		},
	})
	require.NoError(t, err)
	assert.Empty(t, res.Prints, "OnPrint and Result.Prints are alternatives, not both")
	assert.Equal(t, []Output{
		{Stream: StreamStdout, Text: "x"},
		{Stream: StreamStderr, Text: "y"},
	}, got)
}

// TestRunInputsRoundTrip pins the two halves of the input contract: the values
// travel in one arena, and the NamedRefs that name them are sorted so a run
// does not depend on Go's map iteration order.
func TestRunInputsRoundTrip(t *testing.T) {
	// Only the request matters here, so the script is left to run out.
	conn := newFakeConn()
	s := newTestSession(conn, Limits{})

	_, _ = s.Run(context.Background(), "beta + alpha", RunOptions{
		Inputs: map[string]any{"beta": "b", "alpha": int64(7)},
	})

	req := conn.requests()[0]
	feed := req.GetFeed()
	require.NotNil(t, feed)

	values, err := DecodeArena(feed.GetValues(), []uint32{0, 1})
	require.NoError(t, err)
	assert.Equal(t, []any{int64(7), "b"}, values, "arena order follows sorted name order")

	require.Len(t, feed.GetInputs(), 2)
	assert.Equal(t, "alpha", feed.GetInputs()[0].GetName())
	assert.Equal(t, uint32(0), feed.GetInputs()[0].GetValue(), "alpha indexes its own root")
	assert.Equal(t, "beta", feed.GetInputs()[1].GetName())
	assert.Equal(t, uint32(1), feed.GetInputs()[1].GetValue())

	// And each ref really does resolve to the value it names.
	decoded, err := DecodeArena(feed.GetValues(), []uint32{
		feed.GetInputs()[0].GetValue(),
		feed.GetInputs()[1].GetValue(),
	})
	require.NoError(t, err)
	assert.Equal(t, []any{int64(7), "b"}, decoded)
}

func TestRunNoInputsSendsNoArena(t *testing.T) {
	arena, roots := mustArena(t, nil)
	conn := newFakeConn(evtComplete(roots[0], arena))
	s := newTestSession(conn, Limits{})

	_, err := s.Run(context.Background(), "1", RunOptions{})
	require.NoError(t, err)
	feed := conn.requests()[0].GetFeed()
	assert.Nil(t, feed.GetValues())
	assert.Empty(t, feed.GetInputs())
}

func TestRunInputEncodeError(t *testing.T) {
	s := newTestSession(newFakeConn(), Limits{})
	_, err := s.Run(context.Background(), "1", RunOptions{
		Inputs: map[string]any{"bad": make(chan int)},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot encode")
}

func TestRunFunctionCallServicesHostFunc(t *testing.T) {
	arena, _ := mustArena(t, int64(2), int64(3))
	complete, cRoots := mustArena(t, nil)
	conn := newFakeConn(
		evtPrint(pb.PrintStream_PRINT_STREAM_STDOUT, "calling\n"),
		evtFunctionCall("add", 7, []uint32{0, 1}, arena),
		evtComplete(cRoots[0], complete),
	)
	s := newTestSession(conn, Limits{})

	var seen *Call
	res, err := s.Run(context.Background(), "add(2, 3)", RunOptions{
		Host: func(_ context.Context, call *Call) (any, error) {
			seen = call
			return int64(5), nil
		},
	})
	require.NoError(t, err)
	assert.Nil(t, res.Value, "the snippet's own value is None here")

	require.NotNil(t, seen)
	assert.Equal(t, "add", seen.Name)
	assert.Equal(t, []any{int64(2), int64(3)}, seen.Args)
	assert.Equal(t, "feed.py:10-14", seen.Position)
	assert.Empty(t, seen.ObjectID, "a plain function call names no host object")
	assert.Len(t, res.Prints, 1)

	reqs := conn.requests()
	require.Len(t, reqs, 2, "the resume is a second request on the same turn")
	resume := reqs[1].GetResumeCall()
	require.NotNil(t, resume)
	assert.Equal(t, uint32(7), resume.GetCallId())
	assert.Equal(t, uint32(0), resume.GetResult().GetReturnValue())

	got, err := DecodeArena(resume.GetValues(), []uint32{resume.GetResult().GetReturnValue()})
	require.NoError(t, err)
	assert.Equal(t, []any{int64(5)}, got, "the return value travels in the resume's arena")
}

func TestRunFunctionCallKwargs(t *testing.T) {
	// Keyword names are arena nodes themselves, so a kwargs pair is
	// (key index, value index) into the same arena the args came from.
	arena, roots := mustArena(t, "sep", int64(42), int64(1))
	complete, cRoots := mustArena(t, nil)
	call := evtFunctionCallKwargs("log", 3, arena, &pb.NodePair{Key: roots[0], Value: roots[1]})
	call.GetFunctionCall().Args = []uint32{roots[2]}
	conn := newFakeConn(call, evtComplete(cRoots[0], complete))
	s := newTestSession(conn, Limits{})

	var seen *Call
	_, err := s.Run(context.Background(), "log(1, sep=42)", RunOptions{
		Host: func(_ context.Context, call *Call) (any, error) {
			seen = call
			return nil, nil
		},
	})
	require.NoError(t, err)
	require.NotNil(t, seen)
	assert.Equal(t, map[string]any{"sep": int64(42)}, seen.Kwargs)
	assert.Equal(t, []any{int64(1)}, seen.Args)
}

func TestRunHostFuncNotFound(t *testing.T) {
	arena, _ := mustArena(t)
	complete, cRoots := mustArena(t, nil)
	conn := newFakeConn(
		evtFunctionCall("missing", 4, nil, arena),
		evtComplete(cRoots[0], complete),
	)
	s := newTestSession(conn, Limits{})

	_, err := s.Run(context.Background(), "missing()", RunOptions{
		Host: func(context.Context, *Call) (any, error) { return nil, ErrNotFound },
	})
	require.NoError(t, err)

	resume := conn.requests()[1].GetResumeCall()
	require.NotNil(t, resume)
	assert.Equal(t, "missing", resume.GetResult().GetNotFound(),
		"NotFound names the call, which is how Monty raises its own default")
	assert.Nil(t, resume.GetValues())
}

func TestRunHostFuncErrorBecomesRuntimeError(t *testing.T) {
	arena, _ := mustArena(t)
	complete, cRoots := mustArena(t, nil)
	conn := newFakeConn(
		evtFunctionCall("boom", 1, nil, arena),
		evtComplete(cRoots[0], complete),
	)
	s := newTestSession(conn, Limits{})

	_, err := s.Run(context.Background(), "boom()", RunOptions{
		Host: func(context.Context, *Call) (any, error) { return nil, errors.New("disk on fire") },
	})
	require.NoError(t, err)

	resume := conn.requests()[1].GetResumeCall()
	require.NotNil(t, resume)
	raised := resume.GetResult().GetError()
	require.NotNil(t, raised)
	assert.Equal(t, "RuntimeError", raised.GetExcType())
	assert.Equal(t, "disk on fire", raised.GetMessage())
}

// TestRunNoHostFuncIsNotFound is the default: a client with no HostFunc tells
// Monty it has no handler, which is what produces the call's own default error
// (NameError for a name, PermissionError for a path).
func TestRunNoHostFuncIsNotFound(t *testing.T) {
	arena, _ := mustArena(t)
	complete, cRoots := mustArena(t, nil)
	conn := newFakeConn(
		evtFunctionCall("missing", 9, nil, arena),
		evtComplete(cRoots[0], complete),
	)
	s := newTestSession(conn, Limits{})

	_, err := s.Run(context.Background(), "missing()", RunOptions{})
	require.NoError(t, err)
	assert.Equal(t, "missing", conn.requests()[1].GetResumeCall().GetResult().GetNotFound())
}

func TestRunOsCallSurfacesOperationName(t *testing.T) {
	complete, cRoots := mustArena(t, nil)
	conn := newFakeConn(
		evtOsCall(&pb.OsCall{Call: &pb.OsCall_Exists{Exists: "/work/data.txt"}}),
		evtComplete(cRoots[0], complete),
	)
	s := newTestSession(conn, Limits{})

	var seen *Call
	_, err := s.Run(context.Background(), "exists(p)", RunOptions{
		Host: func(_ context.Context, call *Call) (any, error) {
			seen = call
			return true, nil
		},
	})
	require.NoError(t, err)
	require.NotNil(t, seen)
	assert.Equal(t, "exists", seen.Name)
	assert.Equal(t, []any{"/work/data.txt"}, seen.Args)
	assert.Equal(t, "feed.py:1-5", seen.Position)

	resume := conn.requests()[1].GetResumeCall()
	require.NotNil(t, resume)
	assert.Equal(t, uint32(0), resume.GetResult().GetReturnValue())
}

func TestRunOsCallWithNoOperationIsProtocolViolation(t *testing.T) {
	s := newTestSession(newFakeConn(evtOsCall(&pb.OsCall{})), Limits{})
	_, err := s.Run(context.Background(), "x", RunOptions{})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrProtocol)
}

func TestRunNameLookupUndefinedWithoutHost(t *testing.T) {
	complete, cRoots := mustArena(t, nil)
	conn := newFakeConn(
		evtNameLookup("widget"),
		evtComplete(cRoots[0], complete),
	)
	s := newTestSession(conn, Limits{})

	_, err := s.Run(context.Background(), "widget.make()", RunOptions{})
	require.NoError(t, err)

	resume := conn.requests()[1].GetResumeNameLookup()
	require.NotNil(t, resume)
	assert.NotNil(t, resume.GetUndefined(),
		"Undefined is what makes the sandbox raise the default NameError")
	assert.Nil(t, resume.GetValues())
}

func TestRunNameLookupHostFuncResolvesValue(t *testing.T) {
	complete, cRoots := mustArena(t, nil)
	conn := newFakeConn(
		evtNameLookup("widget"),
		evtComplete(cRoots[0], complete),
	)
	s := newTestSession(conn, Limits{})

	var seen *Call
	_, err := s.Run(context.Background(), "widget", RunOptions{
		Host: func(_ context.Context, call *Call) (any, error) {
			seen = call
			return "a widget", nil
		},
	})
	require.NoError(t, err)
	require.NotNil(t, seen)
	assert.Equal(t, "widget", seen.Name)
	assert.Equal(t, "feed.py:3-9", seen.Position)

	resume := conn.requests()[1].GetResumeNameLookup()
	require.NotNil(t, resume)
	require.NotNil(t, resume.GetValue())
	got, err := DecodeArena(resume.GetValues(), []uint32{resume.GetValue()})
	require.NoError(t, err)
	assert.Equal(t, []any{"a widget"}, got)
}

func TestRunNameLookupHostNotFoundIsUndefined(t *testing.T) {
	complete, cRoots := mustArena(t, nil)
	conn := newFakeConn(evtNameLookup("nope"), evtComplete(cRoots[0], complete))
	s := newTestSession(conn, Limits{})

	_, err := s.Run(context.Background(), "nope", RunOptions{
		Host: func(context.Context, *Call) (any, error) { return nil, ErrNotFound },
	})
	require.NoError(t, err)
	assert.NotNil(t, conn.requests()[1].GetResumeNameLookup().GetUndefined())
}

func TestRunNameLookupHostErrorIsRaisedNotSwallowed(t *testing.T) {
	// hasattr() swallows AttributeError, so a host failure during a lookup has
	// to be raised where the lookup suspended rather than reported as
	// "undefined" -- otherwise the error vanishes.
	complete, cRoots := mustArena(t, nil)
	conn := newFakeConn(evtNameLookup("x"), evtComplete(cRoots[0], complete))
	s := newTestSession(conn, Limits{})

	_, err := s.Run(context.Background(), "x", RunOptions{
		Host: func(context.Context, *Call) (any, error) { return nil, errors.New("lookup blew up") },
	})
	require.NoError(t, err)

	resume := conn.requests()[1].GetResumeNameLookup()
	raised := resume.GetError()
	require.NotNil(t, raised)
	assert.Equal(t, "RuntimeError", raised.GetExcType())
	assert.Equal(t, "lookup blew up", raised.GetMessage())
}

func TestRunResolveFuturesIsRefused(t *testing.T) {
	// This client services host calls synchronously, so it never registers a
	// future; being asked to resolve one means the two sides disagree.
	s := newTestSession(newFakeConn(evtResolveFutures(1, 2)), Limits{})
	_, err := s.Run(context.Background(), "x", RunOptions{})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrProtocol)
	assert.Contains(t, err.Error(), "2 unresolved futures")
}

// TestRunPythonErrorIsRecoverable: a Python exception ends the turn, not the
// session -- the globals the failed snippet had already bound are still there.
func TestRunPythonErrorIsRecoverable(t *testing.T) {
	okArena, okRoots := mustArena(t, "recovered")
	conn := newFakeConn(
		evtError("ValueError", "boom"),
		evtComplete(okRoots[0], okArena),
	)
	s := newTestSession(conn, Limits{})

	res, err := s.Run(context.Background(), "1/0", RunOptions{})
	require.Error(t, err)
	assert.Nil(t, res)

	var pe *PythonError
	require.ErrorAs(t, err, &pe)
	assert.Equal(t, "ValueError", pe.Type)
	assert.Equal(t, "boom", pe.Message)
	assert.True(t, IsPythonError(err))
	assert.False(t, IsTransient(err), "the snippet is what failed, so retrying it is pointless")
	assert.False(t, s.Closed(), "a Python error does not end the session")

	res, err = s.Run(context.Background(), "'recovered'", RunOptions{})
	require.NoError(t, err)
	assert.Equal(t, "recovered", res.Value)
}

func TestRunPythonErrorRendersTraceback(t *testing.T) {
	frame := func(name string) *pb.StackFrame {
		f := &pb.StackFrame{Filename: "feed.py"}
		if name != "" {
			f.FrameName = strPtr(name)
		}
		return f
	}
	conn := newFakeConn(&pb.ChildEvent{Kind: &pb.ChildEvent_Error{Error: &pb.Error{
		Exception: &pb.RaisedException{
			ExcType:   "KeyError",
			Message:   strPtr("missing"),
			Traceback: []*pb.StackFrame{frame("outer"), frame(""), frame("inner")},
		},
	}}})
	s := newTestSession(conn, Limits{})

	_, err := s.Run(context.Background(), "d['k']", RunOptions{})
	require.Error(t, err)
	var pe *PythonError
	require.ErrorAs(t, err, &pe)
	assert.Equal(t, "feed.py in outer\nfeed.py\nfeed.py in inner\n", pe.Traceback)
	assert.Contains(t, err.Error(), "KeyError: missing")
}

func TestRunErrorWithNoException(t *testing.T) {
	s := newTestSession(newFakeConn(&pb.ChildEvent{
		Kind: &pb.ChildEvent_Error{Error: &pb.Error{}},
	}), Limits{})
	_, err := s.Run(context.Background(), "x", RunOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unnamed exception")
}

func TestRunTypingError(t *testing.T) {
	conn := newFakeConn(&pb.ChildEvent{Kind: &pb.ChildEvent_TypingError{
		TypingError: &pb.TypingError{Diagnostics: "line 1: cannot unify int and str"},
	}})
	s := newTestSession(conn, Limits{})

	_, err := s.Run(context.Background(), "1 + 'a'", RunOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "type check rejected")
	assert.Contains(t, err.Error(), "cannot unify int and str")
}

// TestRunShutdownIsTransient: a draining server declined the request, so the
// request did NOT run and resending it on a fresh session is safe.
func TestRunShutdownIsTransient(t *testing.T) {
	s := newTestSession(newFakeConn(evtShutdown()), Limits{})
	_, err := s.Run(context.Background(), "x", RunOptions{})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrShutdown)
	assert.True(t, IsTransient(err))
	assert.False(t, s.Closed(), "a shutdown is the server's business, not a dead worker")
}

func TestRunFatalErrorClosesSession(t *testing.T) {
	conn := newFakeConn(evtFatal("frame desync"))
	s := newTestSession(conn, Limits{})

	_, err := s.Run(context.Background(), "x", RunOptions{})
	require.Error(t, err)

	var fe *FatalError
	require.ErrorAs(t, err, &fe)
	assert.Equal(t, "frame desync", fe.Reason)
	assert.ErrorIs(t, err, ErrProtocol, "a FatalError unwraps to the protocol sentinel")
	assert.True(t, s.Closed(), "the worker exits right after a FatalError, so the session is over")
	assert.False(t, IsTransient(err))
}

func TestRunCrashedIsNotTransient(t *testing.T) {
	// A crash mid-turn may already have run the request, so it is not safely
	// retryable.
	s := newTestSession(newFakeConn(), Limits{})
	_, err := s.Run(context.Background(), "x", RunOptions{})
	assert.ErrorIs(t, err, ErrCrashed)
	assert.False(t, IsTransient(err))
}

// TestRunSuspensionLimit: a snippet looping through host calls forever is
// bounded by the host's own budget, which is what protects a host function with
// side effects.
func TestRunSuspensionLimit(t *testing.T) {
	complete, cRoots := mustArena(t, nil)
	conn := newFakeConn(
		evtNameLookup("a"),
		evtNameLookup("b"),
		evtNameLookup("c"),
		evtComplete(cRoots[0], complete),
	)
	s := newTestSession(conn, Limits{MaxSuspensions: 2})

	_, err := s.Run(context.Background(), "loop()", RunOptions{})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSuspensionLimit)
	assert.Contains(t, err.Error(), "after 2 suspensions")
	assert.Len(t, conn.requests(), 3, "the request plus two answered suspensions, no more")
}

func TestRunSuspensionLimitUnlimited(t *testing.T) {
	// Zero means "no host limit"; a script with more suspensions than the
	// default still runs.
	const n = 5
	script := make([]*pb.ChildEvent, 0, n+1)
	for i := 0; i < n; i++ {
		script = append(script, evtNameLookup("a"))
	}
	complete, cRoots := mustArena(t, int64(1))
	script = append(script, evtComplete(cRoots[0], complete))
	s := newTestSession(newFakeConn(script...), Limits{})

	res, err := s.Run(context.Background(), "loop()", RunOptions{})
	require.NoError(t, err)
	assert.Equal(t, int64(1), res.Value)
}

// TestRunCancelledContextClosesSession: a turn abandoned mid-flight leaves the
// worker's protocol state unknown, so the session must not be reused.
func TestRunCancelledContextClosesSession(t *testing.T) {
	conn := newFakeConn()
	conn.blockRecv = true
	s := newTestSession(conn, Limits{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		_, err := s.Run(ctx, "while True: pass", RunOptions{})
		errCh <- err
	}()

	conn.awaitRequests(t, 1)
	cancel()

	select {
	case err := <-errCh:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the context was cancelled")
	}
	assert.True(t, s.Closed(), "a cancelled turn leaves the worker state unknown")

	_, err := s.Run(context.Background(), "1", RunOptions{})
	assert.ErrorIs(t, err, ErrClosed)
}

func TestRunOnClosedSession(t *testing.T) {
	s := newTestSession(newFakeConn(), Limits{})
	s.closed = true
	_, err := s.Run(context.Background(), "x", RunOptions{})
	assert.ErrorIs(t, err, ErrClosed)
}

func TestRunSendOnClosedConn(t *testing.T) {
	conn := newFakeConn()
	conn.closed = true
	s := newTestSession(conn, Limits{})
	_, err := s.Run(context.Background(), "x", RunOptions{})
	assert.ErrorIs(t, err, ErrClosed)
}

func TestRunOnRecoversExecutionMicros(t *testing.T) {
	arena, roots := mustArena(t, nil)
	ev := evtComplete(roots[0], arena)
	ev.FeedExecutionMicros = 1234
	s := newTestSession(newFakeConn(ev), Limits{})

	res, err := s.Run(context.Background(), "x", RunOptions{})
	require.NoError(t, err)
	assert.Equal(t, uint64(1234), res.ExecutionMicros)
}

// TestRunNonTerminatingEventsDoNotEndATurn: Ok, DumpResult and a kindless
// event are all things the turn must read past rather than stop on.
func TestRunNonTerminatingEventsDoNotEndATurn(t *testing.T) {
	arena, roots := mustArena(t, int64(3))
	conn := newFakeConn(
		evtOK(),
		evtDumpResult([]byte("state-blob")),
		evtOK(),
		evtComplete(roots[0], arena),
	)
	s := newTestSession(conn, Limits{})

	res, err := s.Run(context.Background(), "x", RunOptions{})
	require.NoError(t, err)
	assert.Equal(t, int64(3), res.Value)
}

// TestRunKindlessEventIsProtocolViolation: an event with no kind set cannot be
// dispatched, and silently skipping it would let a turn consume a reply that
// belongs to some other request.
func TestRunKindlessEventIsProtocolViolation(t *testing.T) {
	arena, roots := mustArena(t, int64(3))
	conn := newFakeConn(evtKindless(), evtComplete(roots[0], arena))
	s := newTestSession(conn, Limits{})

	_, err := s.Run(context.Background(), "x", RunOptions{})
	require.Error(t, err, "an event with no kind must be refused, not skipped")
	assert.ErrorIs(t, err, ErrProtocol)
}
