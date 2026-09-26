package monty

import (
	"errors"
	"fmt"
	"strings"
)

// Sentinel errors. Every error this package returns wraps one of these or is
// a *PythonError, so callers classify with errors.Is rather than by matching
// strings.
var (
	// ErrClosed is returned once the pool or session has been closed.
	ErrClosed = errors.New("monty: closed")

	// ErrCrashed means the worker process or connection died. The request in
	// flight may or may not have run: Monty cannot tell a worker that vanished
	// mid-turn from one that finished, so a crashed turn is not safely
	// retryable.
	ErrCrashed = errors.New("monty: worker crashed")

	// ErrProtocol means the peer violated the wire contract -- a malformed
	// frame, an out-of-order event, an event with no kind set. The worker is
	// discarded rather than reused.
	ErrProtocol = errors.New("monty: protocol violation")

	// ErrVersion means the peer does not serve the protocol version this
	// client speaks. Monty does not negotiate: it answers Configure with a
	// FatalError naming the range it supports.
	ErrVersion = errors.New("monty: unsupported protocol version")

	// ErrShutdown means a monty-server declined a request because it is
	// draining. The request did NOT run, so it is safe to resend once a
	// session is restored.
	ErrShutdown = errors.New("monty: server shutting down")

	// ErrSuspensionLimit is returned when a single Run exceeds the
	// configured suspension budget -- a sandbox looping through host calls
	// forever.
	ErrSuspensionLimit = errors.New("monty: suspension limit exceeded")

	// ErrNotFound is what a Run without a HostFunc answers to an external
	// function or an OS call the client does not implement, which is how
	// Monty learns to raise the call's own default (NameError,
	// PermissionError, ...).
	ErrNotFound = errors.New("monty: no handler")
)

// PythonError is a Python exception raised inside the sandbox. The session
// survives it: a later Run on the same session still sees the globals the
// failed snippet had already bound.
type PythonError struct {
	// Type is the Python exception class name, e.g. "ValueError".
	Type string
	// Message is the exception's message argument, if it had one.
	Message string
	// Traceback is the rendered traceback, outermost frame first. Empty when
	// the worker did not send one.
	Traceback string
}

func (e *PythonError) Error() string {
	var b strings.Builder
	b.WriteString("monty: ")
	b.WriteString(e.Type)
	if e.Message != "" {
		b.WriteString(": ")
		b.WriteString(e.Message)
	}
	if e.Traceback != "" {
		b.WriteString("\n")
		b.WriteString(e.Traceback)
	}
	return b.String()
}

// FatalError is Monty's unrecoverable worker error -- a frame desync, a panic,
// or a protocol version this build does not serve. The worker exits
// immediately after sending it. EOF without a FatalError is a crash instead
// (see ErrCrashed).
type FatalError struct {
	Reason string
}

func (e *FatalError) Error() string {
	return fmt.Sprintf("monty: worker reported a fatal error: %s", e.Reason)
}

// Unwrap lets errors.Is(err, ErrVersion) succeed for a FatalError that was
// raised by a rejected Configure, which the session records at construction.
func (e *FatalError) Unwrap() error { return ErrProtocol }

// IsTransient reports whether err is worth retrying on a fresh session. A
// Python error, a crash mid-turn, and a protocol violation are not: the
// snippet itself may be what triggered them, and a crashed turn may already
// have run.
func IsTransient(err error) bool {
	return errors.Is(err, ErrShutdown)
}
