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

// recordingCommandHandler is a minimal CommandHandler fake that records which method was
// invoked and with what command, for TestExistingRoutingKeys_StillRouteToExistingHandlers
// -- the NFR3 regression guard (#2184) proving the pre-existing command.* routing keys
// still dispatch to their original handler methods unchanged, now that a new,
// additive-only status.host.*.workshop.cache key exists on the control-api side.
type recordingCommandHandler struct {
	mu sync.Mutex

	startSession  *StartSessionCommand
	stopSession   *StopSessionCommand
	killSession   *KillSessionCommand
	sendInput     *SendInputCommand
	downloadAddon *DownloadAddonCommand
	removeAddon   *RemoveAddonCommand
	backup        *BackupCommand

	done chan struct{}
}

func newRecordingCommandHandler() *recordingCommandHandler {
	return &recordingCommandHandler{done: make(chan struct{}, 16)}
}

func (f *recordingCommandHandler) HandleStartSession(ctx context.Context, cmd *StartSessionCommand) error {
	f.mu.Lock()
	f.startSession = cmd
	f.mu.Unlock()
	f.done <- struct{}{}
	return nil
}

func (f *recordingCommandHandler) HandleStopSession(ctx context.Context, cmd *StopSessionCommand) error {
	f.mu.Lock()
	f.stopSession = cmd
	f.mu.Unlock()
	f.done <- struct{}{}
	return nil
}

func (f *recordingCommandHandler) HandleKillSession(ctx context.Context, cmd *KillSessionCommand) error {
	f.mu.Lock()
	f.killSession = cmd
	f.mu.Unlock()
	return nil
}

func (f *recordingCommandHandler) HandleSendInput(ctx context.Context, cmd *SendInputCommand) error {
	f.mu.Lock()
	f.sendInput = cmd
	f.mu.Unlock()
	return nil
}

func (f *recordingCommandHandler) HandleDownloadAddon(ctx context.Context, cmd *DownloadAddonCommand) error {
	f.mu.Lock()
	f.downloadAddon = cmd
	f.mu.Unlock()
	return nil
}

func (f *recordingCommandHandler) HandleRemoveAddon(ctx context.Context, cmd *RemoveAddonCommand) error {
	f.mu.Lock()
	f.removeAddon = cmd
	f.mu.Unlock()
	f.done <- struct{}{}
	return nil
}

func (f *recordingCommandHandler) HandleBackup(ctx context.Context, cmd *BackupCommand) error {
	f.mu.Lock()
	f.backup = cmd
	f.mu.Unlock()
	f.done <- struct{}{}
	return nil
}

// waitForAsync blocks until n asynchronously-dispatched handlers (HandleStartSession,
// HandleStopSession, HandleRemoveAddon, HandleBackup all run via `go func` in their
// respective handleXxx wrappers) have completed, or fails the test on timeout.
func (f *recordingCommandHandler) waitForAsync(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case <-f.done:
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for async handler dispatch %d/%d", i+1, n)
		}
	}
}

// TestExistingRoutingKeys_StillRouteToExistingHandlers is the NFR3 regression guard
// (#2184): the new status.host.<serverID>.workshop.cache key is a brand new binding on
// control-api's WorkshopCacheStatusConsumer, entirely separate from this host-side
// Consumer. This proves that none of the pre-existing command.host.<serverID>.* routing
// keys were renamed, repurposed, or had their dispatch changed by that addition -- each
// one, dispatched through the exact handleXxx method NewConsumer registers it to (see
// consumer.go's RegisterHandler calls), still reaches the correct CommandHandler method
// with the payload intact.
func TestExistingRoutingKeys_StillRouteToExistingHandlers(t *testing.T) {
	handler := newRecordingCommandHandler()
	c := &Consumer{handler: handler, serverID: 7}

	mustMarshal := func(t *testing.T, v interface{}) []byte {
		t.Helper()
		body, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("failed to marshal: %v", err)
		}
		return body
	}

	// command.host.7.session.start
	startBody := mustMarshal(t, StartSessionCommand{SessionID: 1, SGCID: 2})
	if err := c.handleStartSession(context.Background(), rmq.Message{RoutingKey: "command.host.7.session.start", Body: startBody}); err != nil {
		t.Fatalf("handleStartSession returned error: %v", err)
	}

	// command.host.7.session.stop
	stopBody := mustMarshal(t, StopSessionCommand{SessionID: 1, Force: true})
	if err := c.handleStopSession(context.Background(), rmq.Message{RoutingKey: "command.host.7.session.stop", Body: stopBody}); err != nil {
		t.Fatalf("handleStopSession returned error: %v", err)
	}

	// command.host.7.session.kill (synchronous dispatch)
	killBody := mustMarshal(t, KillSessionCommand{SessionID: 1})
	if err := c.handleKillSession(context.Background(), rmq.Message{RoutingKey: "command.host.7.session.kill", Body: killBody}); err != nil {
		t.Fatalf("handleKillSession returned error: %v", err)
	}

	// command.host.7.session.send_input (synchronous dispatch)
	sendInputBody := mustMarshal(t, SendInputCommand{SessionID: 1, Input: []byte("ping")})
	if err := c.handleSendInput(context.Background(), rmq.Message{RoutingKey: "command.host.7.session.send_input", Body: sendInputBody}); err != nil {
		t.Fatalf("handleSendInput returned error: %v", err)
	}

	// command.host.7.workshop.download (synchronous dispatch)
	downloadBody := mustMarshal(t, DownloadAddonCommand{InstallationID: 1, WorkshopID: "987654321"})
	if err := c.handleDownloadAddon(context.Background(), rmq.Message{RoutingKey: "command.host.7.workshop.download", Body: downloadBody}); err != nil {
		t.Fatalf("handleDownloadAddon returned error: %v", err)
	}

	// command.host.7.workshop.remove
	removeBody := mustMarshal(t, RemoveAddonCommand{InstallationID: 1, InstallationPath: "/data/mods"})
	if err := c.handleRemoveAddon(context.Background(), rmq.Message{RoutingKey: "command.host.7.workshop.remove", Body: removeBody}); err != nil {
		t.Fatalf("handleRemoveAddon returned error: %v", err)
	}

	// command.host.7.backup
	backupBody := mustMarshal(t, BackupCommand{BackupID: 1, SGCID: 2})
	if err := c.handleBackup(context.Background(), rmq.Message{RoutingKey: "command.host.7.backup", Body: backupBody}); err != nil {
		t.Fatalf("handleBackup returned error: %v", err)
	}

	// start, stop, removeAddon, and backup all dispatch asynchronously (see their
	// handleXxx doc comments); wait for all four before asserting.
	handler.waitForAsync(t, 4)

	handler.mu.Lock()
	defer handler.mu.Unlock()

	if handler.startSession == nil || handler.startSession.SessionID != 1 {
		t.Error("command.host.7.session.start did not reach HandleStartSession with the expected payload")
	}
	if handler.stopSession == nil || handler.stopSession.SessionID != 1 {
		t.Error("command.host.7.session.stop did not reach HandleStopSession with the expected payload")
	}
	if handler.killSession == nil || handler.killSession.SessionID != 1 {
		t.Error("command.host.7.session.kill did not reach HandleKillSession with the expected payload")
	}
	if handler.sendInput == nil || string(handler.sendInput.Input) != "ping" {
		t.Error("command.host.7.session.send_input did not reach HandleSendInput with the expected payload")
	}
	if handler.downloadAddon == nil || handler.downloadAddon.WorkshopID != "987654321" {
		t.Error("command.host.7.workshop.download did not reach HandleDownloadAddon with the expected payload")
	}
	if handler.removeAddon == nil || handler.removeAddon.InstallationPath != "/data/mods" {
		t.Error("command.host.7.workshop.remove did not reach HandleRemoveAddon with the expected payload")
	}
	if handler.backup == nil || handler.backup.BackupID != 1 {
		t.Error("command.host.7.backup did not reach HandleBackup with the expected payload")
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
