package rmq

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/whale-net/everything/libs/go/rmq"
)

// fakeCommandHandler is a minimal CommandHandler implementation for testing
// consumer dispatch behavior without needing a live RabbitMQ connection or
// real session manager. Only HandleStopSession is exercised by these tests;
// the rest are no-ops satisfying the interface.
type fakeCommandHandler struct {
	// stopBlock, if non-nil, causes HandleStopSession to block until the
	// channel is closed (or a value is sent).
	stopBlock chan struct{}
	// stopErr is returned by HandleStopSession once unblocked.
	stopErr error

	mu         sync.Mutex
	stopCalled bool
	stopDone   chan struct{}
}

func newFakeCommandHandler() *fakeCommandHandler {
	return &fakeCommandHandler{
		stopDone: make(chan struct{}),
	}
}

func (f *fakeCommandHandler) HandleStartSession(ctx context.Context, cmd *StartSessionCommand) error {
	return nil
}

func (f *fakeCommandHandler) HandleStopSession(ctx context.Context, cmd *StopSessionCommand) error {
	f.mu.Lock()
	f.stopCalled = true
	f.mu.Unlock()

	if f.stopBlock != nil {
		<-f.stopBlock
	}

	close(f.stopDone)
	return f.stopErr
}

func (f *fakeCommandHandler) HandleKillSession(ctx context.Context, cmd *KillSessionCommand) error {
	return nil
}

func (f *fakeCommandHandler) HandleSendInput(ctx context.Context, cmd *SendInputCommand) error {
	return nil
}

func (f *fakeCommandHandler) HandleDownloadAddon(ctx context.Context, cmd *DownloadAddonCommand) error {
	return nil
}

func (f *fakeCommandHandler) HandleRemoveAddon(ctx context.Context, cmd *RemoveAddonCommand) error {
	return nil
}

func (f *fakeCommandHandler) HandleBackup(ctx context.Context, cmd *BackupCommand) error {
	return nil
}

func (f *fakeCommandHandler) wasStopCalled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stopCalled
}

func mustMarshalStopSessionCommand(t *testing.T, cmd StopSessionCommand) []byte {
	t.Helper()
	body, err := json.Marshal(cmd)
	if err != nil {
		t.Fatalf("failed to marshal StopSessionCommand: %v", err)
	}
	return body
}

// TestHandleStopSession_ReturnsBeforeHandlerCompletes proves handleStopSession
// dispatches HandleStopSession asynchronously: it must return (so the RMQ
// ack/reply can fire) well before the underlying handler finishes, even when
// that handler blocks arbitrarily long (e.g. a slow container stop).
func TestHandleStopSession_ReturnsBeforeHandlerCompletes(t *testing.T) {
	handler := newFakeCommandHandler()
	handler.stopBlock = make(chan struct{})

	c := &Consumer{handler: handler}
	body := mustMarshalStopSessionCommand(t, StopSessionCommand{SessionID: 42})

	returned := make(chan error, 1)
	go func() {
		returned <- c.handleStopSession(context.Background(), rmq.Message{
			RoutingKey: "command.host.1.session.stop",
			Body:       body,
		})
	}()

	select {
	case err := <-returned:
		if err != nil {
			t.Fatalf("handleStopSession returned error: %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("handleStopSession did not return promptly; it appears to be blocking on HandleStopSession (dispatch is not async)")
	}

	// The handler should have been invoked (dispatch happened) even though it
	// hasn't completed yet, since it's still blocked on stopBlock.
	deadline := time.After(200 * time.Millisecond)
	for !handler.wasStopCalled() {
		select {
		case <-deadline:
			t.Fatal("HandleStopSession was never invoked")
		case <-time.After(time.Millisecond):
		}
	}

	// Unblock the handler and confirm it actually completes.
	close(handler.stopBlock)
	select {
	case <-handler.stopDone:
	case <-time.After(time.Second):
		t.Fatal("HandleStopSession never completed after being unblocked")
	}
}

// TestHandleStopSession_HandlerErrorDoesNotFailAck proves that when the
// underlying HandleStopSession fails (e.g. session not found), the failure is
// logged rather than propagated to the RMQ ack path -- handleStopSession
// still returns nil.
func TestHandleStopSession_HandlerErrorDoesNotFailAck(t *testing.T) {
	handler := newFakeCommandHandler()
	handler.stopErr = errors.New("session 42 not found")

	c := &Consumer{handler: handler}
	body := mustMarshalStopSessionCommand(t, StopSessionCommand{SessionID: 42})

	err := c.handleStopSession(context.Background(), rmq.Message{
		RoutingKey: "command.host.1.session.stop",
		Body:       body,
	})
	if err != nil {
		t.Fatalf("handleStopSession returned error %v; ack path should be unaffected by async handler failures", err)
	}

	// Wait for the background goroutine to actually run and observe the
	// error (proving it wasn't silently dropped -- it was dispatched and
	// completed, just off the ack path).
	select {
	case <-handler.stopDone:
	case <-time.After(time.Second):
		t.Fatal("HandleStopSession was never invoked/completed asynchronously")
	}
}

// TestHandleStopSession_UnmarshalError_StillFailsSynchronously proves the
// unmarshal-failure path is unchanged: a malformed message body fails
// handleStopSession synchronously, before any dispatch occurs.
func TestHandleStopSession_UnmarshalError_StillFailsSynchronously(t *testing.T) {
	handler := newFakeCommandHandler()

	c := &Consumer{handler: handler}

	err := c.handleStopSession(context.Background(), rmq.Message{
		RoutingKey: "command.host.1.session.stop",
		Body:       []byte("not valid json"),
	})
	if err == nil {
		t.Fatal("expected handleStopSession to return an error for a malformed message body")
	}

	// Give any stray goroutine a moment to run, then confirm the handler was
	// never dispatched -- the unmarshal error must short-circuit before
	// HandleStopSession is called.
	time.Sleep(50 * time.Millisecond)
	if handler.wasStopCalled() {
		t.Fatal("HandleStopSession was invoked despite an unmarshal error")
	}
}
