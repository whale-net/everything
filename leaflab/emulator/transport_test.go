package main

import "sync"

// publishRecord captures one fakeTransport.Publish call.
type publishRecord struct {
	topic    string
	qos      byte
	retained bool
	payload  []byte
}

// fakeTransport is an in-memory Transport recording every publish, for
// broker-free unit testing of the board runtime (manifest_test.go,
// runner_test.go land in the Testing phase). Subscribe is a no-op;
// Disconnect just marks the transport disconnected.
type fakeTransport struct {
	mu           sync.Mutex
	publishes    []publishRecord
	disconnected bool
}

func newFakeTransport() *fakeTransport {
	return &fakeTransport{}
}

func (f *fakeTransport) Publish(topic string, qos byte, retained bool, payload []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.publishes = append(f.publishes, publishRecord{topic: topic, qos: qos, retained: retained, payload: payload})
	return nil
}

func (f *fakeTransport) Subscribe(topic string, qos byte, cb func(topic string, payload []byte)) error {
	return nil
}

func (f *fakeTransport) Disconnect() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.disconnected = true
}
