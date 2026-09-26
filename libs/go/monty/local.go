package monty

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sync"

	pb "github.com/whale-net/everything/libs/go/monty/protos"
)

// subprocessConn is a Conn backed by a `monty subprocess` child.
//
// The child is a separate process for a reason: a stack overflow or an
// allocator abort inside the sandbox takes the worker down, and only the
// process boundary keeps that from taking the host with it. Anything that
// kills the child must therefore also discard the conn rather than reuse it.
type subprocessConn struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stream *EventStream

	exited chan struct{}

	writeMu sync.Mutex
	closed  bool
}

// newSubprocessConn starts a worker. The argv is exactly the binary and the
// one argument `subprocess` -- no flags, and an empty environment so a value
// the host process happens to carry cannot reach the sandbox's view of it.
func newSubprocessConn(ctx context.Context, binary string) (Conn, error) {
	cmd := exec.Command(binary, "subprocess")
	cmd.Env = []string{}
	if runtime.GOOS == "windows" {
		// Windows refuses to start a process with no environment at all.
		cmd.Env = append(cmd.Env, "SystemRoot="+os.Getenv("SystemRoot"))
	}
	// stderr is deliberately not piped: it carries the worker's own
	// diagnostics, and a host that swallows them loses the only clue about
	// why a worker refused to start.
	cmd.Stderr = os.Stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("monty: worker stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("monty: worker stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("monty: start %q: %w", binary, err)
	}

	c := &subprocessConn{cmd: cmd, stdin: stdin, exited: make(chan struct{})}
	frames := &frameReader{r: stdout}
	c.stream = NewEventStream(func() (*pb.ChildEvent, error) {
		body, err := frames.next()
		if err != nil {
			return nil, err
		}
		return decodeEvent(body)
	})

	go func() {
		_ = cmd.Wait()
		close(c.exited)
	}()
	return c, nil
}

func (c *subprocessConn) Send(ctx context.Context, req *pb.ParentRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.closed {
		return ErrClosed
	}
	if err := writeFrame(c.stdin, req); err != nil {
		return fmt.Errorf("monty: send request: %w", err)
	}
	return nil
}

func (c *subprocessConn) Recv(ctx context.Context) (*pb.ChildEvent, error) {
	return c.stream.Recv(ctx)
}

func (c *subprocessConn) Alive() bool {
	select {
	case <-c.exited:
		return false
	default:
		return true
	}
}

func (c *subprocessConn) Close() error {
	c.writeMu.Lock()
	if c.closed {
		c.writeMu.Unlock()
		return nil
	}
	c.closed = true
	c.writeMu.Unlock()

	c.stream.Close()
	_ = c.stdin.Close()
	// A worker mid-turn is holding state this client will never ask for
	// again, so there is nothing to salvage: kill rather than wait.
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	<-c.exited
	return nil
}
