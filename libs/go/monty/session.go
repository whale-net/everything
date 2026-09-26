package monty

import (
	"context"
	"errors"
	"fmt"
	"sort"

	pb "github.com/whale-net/everything/libs/go/monty/protos"
)

// Stream identifies which sandbox stream a run of print() output came from.
type Stream int

const (
	// StreamStdout is the sandbox's stdout.
	StreamStdout Stream = iota
	// StreamStderr is the sandbox's stderr.
	StreamStderr
)

func (s Stream) String() string {
	if s == StreamStderr {
		return "stderr"
	}
	return "stdout"
}

// Output is one run of sandbox print() output. A single wire event can carry
// several runs, alternating between the streams, so a caller that reassembles
// output must keep the order the sandbox produced rather than grouping by
// stream.
type Output struct {
	Stream Stream
	Text   string
}

// Call is a suspension: the sandbox asked the host to do something.
type Call struct {
	// Name is the function name for an external call, or the OS operation
	// ("getenv", "read_text", ...) for a filesystem or clock call.
	Name string
	// Args are the positional arguments. For a method call on a host-backed
	// object the receiver is NOT here -- it is named by ObjectID, because
	// only the host knows what that uuid refers to.
	Args []any
	// Kwargs are the keyword arguments, in the order the sandbox passed them.
	Kwargs map[string]any
	// ObjectID is set when the sandbox called a method on, or looked up a
	// lazy attribute of, a host-backed object. Empty for a plain call.
	ObjectID string
	// Position is the source range of the call expression, for error
	// messages that point back into the fed code.
	Position string
}

// HostFunc services a suspension. Returning ErrNotFound tells Monty the call
// has no handler here, which makes it raise the call's own default --
// NameError for an unknown name, PermissionError naming the path for a
// filesystem call, RuntimeError for the rest. Returning any other error
// raises it in the sandbox at the call site instead.
type HostFunc func(ctx context.Context, call *Call) (any, error)

// RunOptions configures one Run.
type RunOptions struct {
	// Inputs are bound as sandbox globals for this snippet, in the worker's
	// session namespace. Two names may share a value; Monty keeps one object.
	Inputs map[string]any

	// OnPrint receives sandbox output as it arrives. When nil, output is
	// collected into Result.Prints instead.
	OnPrint func(stream Stream, text string)

	// Host services external-function, attribute and OS suspensions. When
	// nil, every suspension is answered "not found".
	Host HostFunc

	// TypeCheck overrides the session's setting for this snippet only.
	TypeCheck *bool

	// CWD switches the session to an absolute virtual working directory
	// before the snippet runs. Empty keeps the current one.
	CWD string
}

// Result is the outcome of a completed Run.
type Result struct {
	// Value is the snippet's final expression, mapped through DecodeArena.
	// nil is Python's None, which is also a legitimate result -- use
	// Result.Prints and the error to tell a None from a failure.
	Value any
	// Prints is the output the snippet produced, in the order the sandbox
	// emitted it. Empty when OnPrint was set.
	Prints []Output
	// ExecutionMicros is sandbox execution time consumed by this feed, which
	// runs only while the interpreter executes bytecode -- never while the
	// session is suspended waiting on a host call.
	ExecutionMicros uint64
}

// Session is one checked-out worker: a REPL state plus the connection serving
// it. A session is not safe for concurrent use -- Monty has no multiplexing,
// so two Runs on one session have no defined interleaving.
type Session struct {
	conn   Conn
	limits Limits
	// pool is nil for a session opened outside a Pool, which Close then
	// simply tears down.
	pool *Pool
	// reusable is false for a transport that cannot hand the same worker to
	// a later session (a WebSocket worker is single-use).
	reusable bool
	closed   bool
	// broken records that a turn was abandoned or the worker reported a
	// fatal error, so the connection must not go back to the pool even
	// though the process may still be running.
	broken bool
}

// Run feeds one snippet to the session and waits for it to finish.
func (s *Session) Run(ctx context.Context, code string, opts RunOptions) (*Result, error) {
	if s.closed {
		return nil, ErrClosed
	}

	arena, roots, err := s.buildInputs(opts.Inputs)
	if err != nil {
		return nil, err
	}
	req := &pb.ParentRequest{
		Kind: &pb.ParentRequest_Feed{Feed: &pb.Feed{
			Code:          code,
			Values:        arena,
			Cwd:           opts.CWD,
			SkipTypeCheck: opts.TypeCheck != nil && !*opts.TypeCheck,
		}},
	}
	for name, idx := range roots {
		req.GetFeed().Inputs = append(req.GetFeed().Inputs, &pb.NamedRef{Name: name, Value: idx})
	}
	// NamedRef order is the arena's, not the map's, so a session behaves the
	// same across runs.
	sort.Slice(req.GetFeed().Inputs, func(i, j int) bool {
		return req.GetFeed().Inputs[i].Name < req.GetFeed().Inputs[j].Name
	})

	return s.turn(ctx, req, opts)
}

// buildInputs encodes the caller's values into one arena and returns each
// name's root index.
func (s *Session) buildInputs(inputs map[string]any) (*pb.Arena, map[string]uint32, error) {
	if len(inputs) == 0 {
		return nil, nil, nil
	}
	names := make([]string, 0, len(inputs))
	values := make([]any, 0, len(inputs))
	for name := range inputs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		values = append(values, inputs[name])
	}
	arena, roots, err := EncodeArena(values)
	if err != nil {
		return nil, nil, err
	}
	refs := make(map[string]uint32, len(roots))
	for i, name := range names {
		refs[name] = roots[i]
	}
	return arena, refs, nil
}

// turn runs the request/response alternation to a turn-ending event,
// answering suspensions as they arrive.
func (s *Session) turn(ctx context.Context, req *pb.ParentRequest, opts RunOptions) (*Result, error) {
	if err := s.conn.Send(ctx, req); err != nil {
		return nil, err
	}

	res := &Result{}
	suspensions := 0
	for {
		ev, err := s.conn.Recv(ctx)
		if err != nil {
			// A turn abandoned mid-flight leaves the worker's protocol state
			// unknown, so stop the worker rather than let the next Run read a
			// reply that belongs to this one.
			if ctx.Err() != nil {
				s.closed = true
				s.broken = true
				return nil, ctx.Err()
			}
			return nil, err
		}

		if pr := ev.GetPrint(); pr != nil {
			for _, seg := range pr.Segments {
				out := Output{Text: seg.Text}
				if seg.Stream == pb.PrintStream_PRINT_STREAM_STDERR {
					out.Stream = StreamStderr
				}
				if opts.OnPrint != nil {
					opts.OnPrint(out.Stream, out.Text)
				} else {
					res.Prints = append(res.Prints, out)
				}
			}
			continue
		}

		// A kindless event is not "an event a newer worker added": it is a
		// frame the protocol cannot interpret, and skipping it would let
		// this turn consume a reply that belongs to the next request.
		if ev.Kind == nil {
			s.closed = true
			s.broken = true
			return nil, fmt.Errorf("%w: event carried no kind", ErrProtocol)
		}

		switch {
		case ev.GetComplete() != nil:
			value, err := decodeRoot(ev.GetComplete().Values, ev.GetComplete().Value)
			if err != nil {
				return nil, err
			}
			res.Value = value
			res.ExecutionMicros = ev.GetFeedExecutionMicros()
			return res, nil

		case ev.GetError() != nil:
			return nil, pythonError(ev.GetError().GetException())

		case ev.GetTypingError() != nil:
			return nil, fmt.Errorf("monty: type check rejected the snippet: %s", ev.GetTypingError().GetDiagnostics())

		case ev.GetFatalError() != nil:
			// The worker exits immediately after a fatal error, so this
			// connection is finished whether or not the process is.
			s.closed = true
			s.broken = true
			return nil, &FatalError{Reason: ev.GetFatalError().GetMessage()}

		case ev.GetShutdown() != nil:
			return nil, fmt.Errorf("%w: the request did not run", ErrShutdown)

		case ev.GetFunctionCall() != nil, ev.GetOsCall() != nil, ev.GetNameLookup() != nil, ev.GetResolveFutures() != nil:
			suspensions++
			if limit := s.limits.MaxSuspensions; limit > 0 && suspensions > limit {
				return nil, fmt.Errorf("%w after %d suspensions", ErrSuspensionLimit, suspensions-1)
			}
			answer, err := s.answer(ctx, ev, opts)
			if err != nil {
				return nil, err
			}
			if err := s.conn.Send(ctx, answer); err != nil {
				return nil, err
			}

		default:
			// Ok, DumpResult and anything a newer worker adds: none of them
			// end a feed turn.
			continue
		}
	}
}

// answer turns a suspension into the request that resumes it.
func (s *Session) answer(ctx context.Context, ev *pb.ChildEvent, opts RunOptions) (*pb.ParentRequest, error) {
	switch {
	case ev.GetNameLookup() != nil:
		return s.answerNameLookup(ctx, ev.GetNameLookup(), opts)

	case ev.GetFunctionCall() != nil:
		call, err := s.describeFunction(ev.GetFunctionCall(), opts)
		if err != nil {
			return nil, err
		}
		return s.resumeCall(ctx, ev.GetFunctionCall().CallId, call, opts)

	case ev.GetOsCall() != nil:
		call, err := describeOS(ev.GetOsCall())
		if err != nil {
			return nil, err
		}
		return s.resumeCall(ctx, ev.GetOsCall().CallId, call, opts)

	default: // ResolveFutures
		// This client runs host calls synchronously, so it never registers a
		// future and should never be asked to resolve one. Answering with the
		// pending set would leave the sandbox blocked, and refusing leaves it
		// blocked too; the honest move is to end the turn.
		return nil, fmt.Errorf("%w: the sandbox is waiting on %d unresolved futures",
			ErrProtocol, len(ev.GetResolveFutures().GetPendingCallIds()))
	}
}

// resumeCall runs the host function and encodes its answer.
func (s *Session) resumeCall(ctx context.Context, callID uint32, call *Call, opts RunOptions) (*pb.ParentRequest, error) {
	result := &pb.ExtFunctionResult{}
	if opts.Host == nil {
		result.Kind = &pb.ExtFunctionResult_NotFound{NotFound: call.Name}
		return &pb.ParentRequest{Kind: &pb.ParentRequest_ResumeCall{ResumeCall: &pb.ResumeCall{
			CallId: callID, Result: result,
		}}}, nil
	}

	value, err := opts.Host(ctx, call)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			result.Kind = &pb.ExtFunctionResult_NotFound{NotFound: call.Name}
		} else {
			result.Kind = &pb.ExtFunctionResult_Error{Error: &pb.RaisedException{
				ExcType: "RuntimeError",
				Message: strPtr(err.Error()),
			}}
		}
		return &pb.ParentRequest{Kind: &pb.ParentRequest_ResumeCall{ResumeCall: &pb.ResumeCall{
			CallId: callID, Result: result,
		}}}, nil
	}

	arena, roots, err := EncodeArena([]any{value})
	if err != nil {
		return nil, err
	}
	result.Kind = &pb.ExtFunctionResult_ReturnValue{ReturnValue: roots[0]}
	return &pb.ParentRequest{Kind: &pb.ParentRequest_ResumeCall{ResumeCall: &pb.ResumeCall{
		CallId: callID, Result: result, Values: arena,
	}}}, nil
}

// answerNameLookup resolves a name the sandbox did not find, which is how it
// discovers a host-provided function or attribute.
func (s *Session) answerNameLookup(ctx context.Context, nl *pb.NameLookup, opts RunOptions) (*pb.ParentRequest, error) {
	call := &Call{
		Name:     nl.GetName(),
		ObjectID: uuidString(nl.GetObjectId()),
		Position: sourceRange(nl.GetPosition()),
	}
	req := &pb.ParentRequest{Kind: &pb.ParentRequest_ResumeNameLookup{ResumeNameLookup: &pb.ResumeNameLookup{}}}
	if opts.Host == nil {
		req.GetResumeNameLookup().Kind = &pb.ResumeNameLookup_Undefined{Undefined: &pb.Unit{}}
		return req, nil
	}

	value, err := opts.Host(ctx, call)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			req.GetResumeNameLookup().Kind = &pb.ResumeNameLookup_Undefined{Undefined: &pb.Unit{}}
			return req, nil
		}
		// Resolving on the host raised: raise the same error where the
		// lookup suspended, rather than letting hasattr() swallow it.
		req.GetResumeNameLookup().Kind = &pb.ResumeNameLookup_Error{Error: &pb.RaisedException{
			ExcType: "RuntimeError",
			Message: strPtr(err.Error()),
		}}
		return req, nil
	}

	arena, roots, err := EncodeArena([]any{value})
	if err != nil {
		return nil, err
	}
	req.GetResumeNameLookup().Values = arena
	req.GetResumeNameLookup().Kind = &pb.ResumeNameLookup_Value{Value: roots[0]}
	return req, nil
}

// describeFunction decodes a function-call suspension into a Call.
func (s *Session) describeFunction(fc *pb.FunctionCall, opts RunOptions) (*Call, error) {
	args, err := DecodeArena(fc.GetValues(), fc.GetArgs())
	if err != nil {
		return nil, err
	}
	call := &Call{
		Name:     fc.GetFunctionName(),
		Args:     args,
		Kwargs:   map[string]any{},
		ObjectID: uuidString(fc.GetObjectId()),
		Position: sourceRange(fc.GetPosition()),
	}

	// Keyword names are themselves arena nodes, so the pairs are flattened
	// key-then-value and decoded in one pass.
	pairs := fc.GetKwargs()
	flat := make([]uint32, 0, len(pairs)*2)
	for _, p := range pairs {
		flat = append(flat, p.GetKey(), p.GetValue())
	}
	decoded, err := DecodeArena(fc.GetValues(), flat)
	if err != nil {
		return nil, err
	}
	for i := 0; i < len(pairs); i++ {
		name, ok := decoded[2*i].(string)
		if !ok {
			return nil, fmt.Errorf("%w: keyword argument name is %T, not a string", ErrProtocol, decoded[2*i])
		}
		call.Kwargs[name] = decoded[2*i+1]
	}
	return call, nil
}

// describeOS renders an OS suspension as a Call. Every OS operation surfaces
// as its own event -- Monty does not service a mount itself -- so a host
// that answers NotFound gets the documented default for that call.
func describeOS(oc *pb.OsCall) (*Call, error) {
	name, arg, err := osCallName(oc)
	if err != nil {
		return nil, err
	}
	call := &Call{Name: name, Position: sourceRange(oc.GetPosition())}
	switch a := arg.(type) {
	case nil:
	case string:
		// The filesystem probes carry the virtual path and nothing else.
		call.Args = []any{a}
	case *pb.OsCall_Getenv:
		// The default is an arbitrary Python value, so it arrives as an
		// arena index rather than a typed field.
		def, err := DecodeArena(oc.GetValues(), []uint32{a.GetDefault()})
		if err != nil {
			return nil, err
		}
		call.Args = []any{a.GetKey()}
		call.Args = append(call.Args, def...)
	case *pb.OsCall_Urandom:
		call.Args = []any{a.GetSize()}
	case *pb.OsCall_TimeCall:
		// The clock reads share one call name; the caller distinguishes them.
		call.Args = []any{a.GetCaller()}
	case *pb.OsCall_Sleep:
		call.Args = []any{a.GetSeconds()}
	case *pb.OsCall_AsyncSleep:
		call.Args = []any{a.GetDelay()}
	default:
		// The remaining operations (write, open, mkdir, rename, ...) carry a
		// structured payload the host has to know to interpret; surfacing the
		// name alone is enough for a host to answer "not handled".
		call.Args = []any{fmt.Sprintf("%v", a)}
	}
	return call, nil
}

func pythonError(exc *pb.RaisedException) error {
	if exc == nil {
		return fmt.Errorf("monty: the sandbox raised an unnamed exception")
	}
	pe := &PythonError{Type: exc.GetExcType(), Message: exc.GetMessage()}
	for _, frame := range exc.GetTraceback() {
		pe.Traceback += frame.GetFilename()
		if name := frame.GetFrameName(); name != "" {
			pe.Traceback += " in " + name
		}
		pe.Traceback += "\n"
	}
	return pe
}
