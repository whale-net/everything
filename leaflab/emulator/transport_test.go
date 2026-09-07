package main

import (
	"errors"
	"fmt"
	"sync"
)

// errInjectedSubscribeFailure is returned by fakeTransport.Subscribe while
// an injected failure count (failSubscribeNext) is still outstanding for
// the given topic -- config_apply_test.go's subscribeConfig bounded-retry
// coverage (#2024) uses this to simulate the transient post-takeover
// resubscribe race subscribeRetryAttempts exists to ride out.
var errInjectedSubscribeFailure = errors.New("fakeTransport: injected subscribe failure")

// publishRecord captures one fakeTransport.Publish call.
type publishRecord struct {
	topic    string
	qos      byte
	retained bool
	payload  []byte
}

// subscribeRecord captures one fakeTransport.Subscribe call -- used by
// config_apply_test.go to assert re-subscription happens on every
// reconnect (FR18's reconnect-convergence requirement), not just the first
// connect.
type subscribeRecord struct {
	topic string
	qos   byte
}

// fakeTransport is an in-memory Transport recording every publish and
// subscribe, for broker-free unit testing of the board runtime
// (manifest_test.go, runner_test.go, config_apply_test.go land in the
// Testing phase). Subscribe records the call and keeps the handler so
// config_apply_test.go can drive Runner.handleConfig the same way a real
// broker would -- via deliver -- instead of calling it directly. Disconnect
// just marks the transport disconnected.
type fakeTransport struct {
	mu           sync.Mutex
	publishes    []publishRecord
	subscribes   []subscribeRecord
	handlers     map[string]func(topic string, payload []byte)
	disconnected bool
	// subscribeFailures, keyed by topic, counts down remaining injected
	// Subscribe failures for that topic (see failSubscribeNext below).
	subscribeFailures map[string]int
}

func newFakeTransport() *fakeTransport {
	return &fakeTransport{}
}

// failSubscribeNext arranges for the next n calls to Subscribe(topic, ...)
// to fail with errInjectedSubscribeFailure (recorded in subscribes but with
// no handler registered, matching a real paho Subscribe failure) before
// succeeding normally -- config_apply_test.go's subscribeConfig
// bounded-retry coverage (#2024).
func (f *fakeTransport) failSubscribeNext(topic string, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.subscribeFailures == nil {
		f.subscribeFailures = make(map[string]int)
	}
	f.subscribeFailures[topic] = n
}

func (f *fakeTransport) Publish(topic string, qos byte, retained bool, payload []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.publishes = append(f.publishes, publishRecord{topic: topic, qos: qos, retained: retained, payload: payload})
	return nil
}

func (f *fakeTransport) Subscribe(topic string, qos byte, cb func(topic string, payload []byte)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subscribes = append(f.subscribes, subscribeRecord{topic: topic, qos: qos})
	if remaining := f.subscribeFailures[topic]; remaining > 0 {
		f.subscribeFailures[topic] = remaining - 1
		return errInjectedSubscribeFailure
	}
	if f.handlers == nil {
		f.handlers = make(map[string]func(string, []byte))
	}
	f.handlers[topic] = cb
	return nil
}

func (f *fakeTransport) Disconnect() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.disconnected = true
}

// deliver invokes the handler most recently subscribed for topic, simulating
// an incoming broker message -- how config_apply_test.go drives
// Runner.handleConfig without a real broker. Panics if nothing is
// subscribed to topic, since that would silently no-op a test's config push
// instead of failing loudly.
func (f *fakeTransport) deliver(topic string, payload []byte) {
	f.mu.Lock()
	cb := f.handlers[topic]
	f.mu.Unlock()
	if cb == nil {
		panic(fmt.Sprintf("fakeTransport: deliver to topic %q with no subscriber", topic))
	}
	cb(topic, payload)
}
