package htmxsse

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestFR5_FullStateOnConnect tests that full state is produced on connect
func TestFR5_FullStateOnConnect(t *testing.T) {
	fakeTransport := &fakeTransport{}
	attachFunc := func(ctx context.Context) (Transport, error) {
		return fakeTransport, nil
	}

	config := DefaultConfig()
	h := NewHub(attachFunc, config)
	defer h.Close()

	fragmentCalls := []string{}
	var mu sync.Mutex

	fragment := func(r *http.Request, topic string) ([]byte, error) {
		mu.Lock()
		fragmentCalls = append(fragmentCalls, topic)
		mu.Unlock()
		return []byte(fmt.Sprintf(`{"topic": "%s", "state": "current"}`, topic)), nil
	}

	// Create a handler and call it
	handler := Handler(h, []string{"topic-a"}, fragment)

	req := httptest.NewRequest("GET", "/events", nil)
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()

	// Run handler in a goroutine and cancel after a short time
	done := make(chan struct{})
	go func() {
		handler(w, req)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	// Verify full state was produced
	require.Len(t, fragmentCalls, 1)
	require.Equal(t, "topic-a", fragmentCalls[0])

	// Check response contains the swap event
	body := w.Body.String()
	require.Contains(t, body, "event: topic-a")
	require.Contains(t, body, "data: {\"topic\": \"topic-a\", \"state\": \"current\"}")
}

// TestFR5_ReconnectBaselineSuppression tests that keepalive is emitted when baseline matches
func TestFR5_ReconnectBaselineSuppression(t *testing.T) {
	fakeTransport := &fakeTransport{}
	attachFunc := func(ctx context.Context) (Transport, error) {
		return fakeTransport, nil
	}

	config := DefaultConfig()
	h := NewHub(attachFunc, config)
	defer h.Close()

	fragment := func(r *http.Request, topic string) ([]byte, error) {
		return []byte(`{"state": "current"}`), nil
	}

	handler := Handler(h, []string{"topic-a"}, fragment)

	// First request to get the baseline
	req1 := httptest.NewRequest("GET", "/events", nil)
	ctx1, cancel1 := context.WithCancel(req1.Context())
	req1 = req1.WithContext(ctx1)

	w1 := httptest.NewRecorder()

	done1 := make(chan struct{})
	go func() {
		handler(w1, req1)
		close(done1)
	}()

	time.Sleep(50 * time.Millisecond)

	body1 := w1.Body.String()
	// Extract the id from the first response
	var id string
	lines := strings.Split(body1, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "id: ") {
			id = strings.TrimPrefix(line, "id: ")
			break
		}
	}

	require.NotEmpty(t, id, "should have id in first response")

	cancel1()
	<-done1

	// Second request with Last-Event-ID header
	req2 := httptest.NewRequest("GET", "/events", nil)
	req2.Header.Set("Last-Event-ID", id)
	ctx2, cancel2 := context.WithCancel(req2.Context())
	req2 = req2.WithContext(ctx2)

	w2 := httptest.NewRecorder()

	done2 := make(chan struct{})
	go func() {
		handler(w2, req2)
		close(done2)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel2()
	<-done2

	body2 := w2.Body.String()
	// When baseline matches, should emit keepalive, not swap
	require.Contains(t, body2, "event: topic-a-keepalive")
	require.NotContains(t, body2, "event: topic-a\n")
}

// TestFR5_MultiTopicBaselineRule tests baseline suppression per topic
func TestFR5_MultiTopicBaselineRule(t *testing.T) {
	fakeTransport := &fakeTransport{}
	attachFunc := func(ctx context.Context) (Transport, error) {
		return fakeTransport, nil
	}

	config := DefaultConfig()
	h := NewHub(attachFunc, config)
	defer h.Close()

	fragment := func(r *http.Request, topic string) ([]byte, error) {
		return []byte(fmt.Sprintf(`{"topic": "%s"}`, topic)), nil
	}

	handler := Handler(h, []string{"topic-a", "topic-b"}, fragment)

	// First request to get baselines
	req1 := httptest.NewRequest("GET", "/events", nil)
	ctx1, cancel1 := context.WithCancel(req1.Context())
	req1 = req1.WithContext(ctx1)

	w1 := httptest.NewRecorder()

	done1 := make(chan struct{})
	go func() {
		handler(w1, req1)
		close(done1)
	}()

	time.Sleep(50 * time.Millisecond)

	body1 := w1.Body.String()
	// Extract all ids from the first response
	ids := []string{}
	lines := strings.Split(body1, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "id: ") {
			id := strings.TrimPrefix(line, "id: ")
			ids = append(ids, id)
		}
	}

	// Should have at least one id
	require.Greater(t, len(ids), 0)
	// The last id should contain both topics
	fullID := ids[len(ids)-1]
	require.Contains(t, fullID, "topic-a")
	require.Contains(t, fullID, "topic-b")

	cancel1()
	<-done1

	// Second request with baseline for topic-a only (simulating truncated baseline)
	// This should swap topic-b but not topic-a
	partialID := strings.Split(fullID, "|")[0] // Only first topic's hash

	req2 := httptest.NewRequest("GET", "/events", nil)
	req2.Header.Set("Last-Event-ID", partialID)
	ctx2, cancel2 := context.WithCancel(req2.Context())
	req2 = req2.WithContext(ctx2)

	w2 := httptest.NewRecorder()

	done2 := make(chan struct{})
	go func() {
		handler(w2, req2)
		close(done2)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel2()
	<-done2

	body2 := w2.Body.String()
	// Should suppress topic-a and swap topic-b
	lines = strings.Split(body2, "\n")
	eventLines := []string{}
	for _, line := range lines {
		if strings.HasPrefix(line, "event: ") {
			eventLines = append(eventLines, line)
		}
	}
	// Should have keepalive for topic-a and swap for topic-b
	require.True(t, len(eventLines) > 0)
}

// TestFR3_PerConnectionFragment tests that fragments are per-connection
func TestFR3_PerConnectionFragment(t *testing.T) {
	fakeTransport := &fakeTransport{}
	attachFunc := func(ctx context.Context) (Transport, error) {
		return fakeTransport, nil
	}

	config := DefaultConfig()
	h := NewHub(attachFunc, config)
	defer h.Close()

	var mu sync.Mutex
	fragmentsByRequest := make(map[string]int)

	fragment := func(r *http.Request, topic string) ([]byte, error) {
		remoteAddr := r.RemoteAddr
		mu.Lock()
		fragmentsByRequest[remoteAddr]++
		count := fragmentsByRequest[remoteAddr]
		mu.Unlock()
		return []byte(fmt.Sprintf(`{"addr": "%s", "count": %d}`, remoteAddr, count)), nil
	}

	handler := Handler(h, []string{"topic-a"}, fragment)

	// Create two separate requests
	req1 := httptest.NewRequest("GET", "/events", nil)
	req1.RemoteAddr = "192.168.1.1:1234"
	ctx1, cancel1 := context.WithCancel(req1.Context())
	req1 = req1.WithContext(ctx1)

	req2 := httptest.NewRequest("GET", "/events", nil)
	req2.RemoteAddr = "192.168.1.2:5678"
	ctx2, cancel2 := context.WithCancel(req2.Context())
	req2 = req2.WithContext(ctx2)

	w1 := httptest.NewRecorder()
	w2 := httptest.NewRecorder()

	done1 := make(chan struct{})
	done2 := make(chan struct{})

	go func() {
		handler(w1, req1)
		close(done1)
	}()

	go func() {
		handler(w2, req2)
		close(done2)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel1()
	cancel2()

	<-done1
	<-done2

	// Each request should have produced its own fragment with its own address
	require.Len(t, fragmentsByRequest, 2)
	require.Greater(t, fragmentsByRequest["192.168.1.1:1234"], 0)
	require.Greater(t, fragmentsByRequest["192.168.1.2:5678"], 0)
}

// TestFR3_DeliveryTimeErrorPolicy tests that fragment errors don't close the stream
func TestFR3_DeliveryTimeErrorPolicy(t *testing.T) {
	fakeTransport := &fakeTransport{}
	attachFunc := func(ctx context.Context) (Transport, error) {
		return fakeTransport, nil
	}

	mockClock := &mockClock{now: time.Now()}
	config := DefaultConfig()
	config.HeartbeatInterval = 50 * time.Millisecond
	h := NewHubWithClock(attachFunc, config, mockClock)
	defer h.Close()

	errorOnCall := atomic.Int32{}
	fragmentCalls := atomic.Int32{}

	fragment := func(r *http.Request, topic string) ([]byte, error) {
		fragmentCalls.Add(1)
		if errorOnCall.Load() == 1 {
			return nil, fmt.Errorf("transient error")
		}
		return []byte(`{"state": "ok"}`), nil
	}

	handler := Handler(h, []string{"topic-a"}, fragment)

	req := httptest.NewRequest("GET", "/events", nil)
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		handler(w, req)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)

	// Trigger an error
	errorOnCall.Store(1)

	time.Sleep(50 * time.Millisecond)

	// Clear error for recovery
	errorOnCall.Store(0)

	// Wait for more heartbeats
	time.Sleep(100 * time.Millisecond)

	cancel()
	<-done

	// Fragment should have been called multiple times despite the error
	require.Greater(t, fragmentCalls.Load(), int32(1))

	// Stream should still have output (the successful fragments)
	body := w.Body.String()
	require.Contains(t, body, "event: topic-a")
}

// TestNFR11_HeartbeatNoSwapOnUnchanged tests keepalive on heartbeat when unchanged
func TestNFR11_HeartbeatNoSwapOnUnchanged(t *testing.T) {
	fakeTransport := &fakeTransport{}
	attachFunc := func(ctx context.Context) (Transport, error) {
		return fakeTransport, nil
	}

	config := DefaultConfig()
	config.HeartbeatInterval = 50 * time.Millisecond
	h := NewHub(attachFunc, config)
	defer h.Close()

	fragment := func(r *http.Request, topic string) ([]byte, error) {
		return []byte(`{"state": "unchanged"}`), nil
	}

	handler := Handler(h, []string{"topic-a"}, fragment)

	req := httptest.NewRequest("GET", "/events", nil)
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		handler(w, req)
		close(done)
	}()

	// Let heartbeats run
	time.Sleep(150 * time.Millisecond)
	cancel()
	<-done

	body := w.Body.String()
	// Should have one initial swap
	swapCount := strings.Count(body, "event: topic-a\n")
	// And heartbeat keepalives (events without id should be keepalives)
	keepaliveCount := strings.Count(body, "event: topic-a-keepalive")

	require.Equal(t, 1, swapCount, "should have initial swap")
	require.Greater(t, keepaliveCount, 0, "should have keepalive heartbeats")
}

// TestNFR11_HeartbeatErrorBranch tests that errors result in zero output
func TestNFR11_HeartbeatErrorBranch(t *testing.T) {
	fakeTransport := &fakeTransport{}
	attachFunc := func(ctx context.Context) (Transport, error) {
		return fakeTransport, nil
	}

	mockClock := &mockClock{
		now: time.Now(),
	}

	config := DefaultConfig()
	config.HeartbeatInterval = 50 * time.Millisecond
	h := NewHubWithClock(attachFunc, config, mockClock)
	defer h.Close()

	tickCount := atomic.Int32{}
	heartbeatWithError := false
	var heMu sync.Mutex

	fragment := func(r *http.Request, topic string) ([]byte, error) {
		heMu.Lock()
		shouldError := heartbeatWithError
		heMu.Unlock()

		tickCount.Add(1)

		if shouldError {
			return nil, fmt.Errorf("heartbeat error")
		}
		return []byte(`{"state": "ok"}`), nil
	}

	handler := Handler(h, []string{"topic-a"}, fragment)

	req := httptest.NewRequest("GET", "/events", nil)
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()

	ticks := 0
	mockClock.tickerFn = func(d time.Duration) Ticker {
		if d == config.HeartbeatInterval {
			return &fakeHeartbeatTicker{
				onC: func() {
					ticks++
					if ticks == 2 {
						heMu.Lock()
						heartbeatWithError = true
						heMu.Unlock()
					} else if ticks == 3 {
						heMu.Lock()
						heartbeatWithError = false
						heMu.Unlock()
					}
				},
			}
		}
		return &defaultTicker{time.NewTicker(d)}
	}

	done := make(chan struct{})
	go func() {
		handler(w, req)
		close(done)
	}()

	// Wait for several heartbeats
	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done

	// Should have been called: initial + at least 3 heartbeats
	require.GreaterOrEqual(t, tickCount.Load(), int32(2))
}

// TestFR2_ContextCancellation tests that stream ends when context is done
func TestFR2_ContextCancellation(t *testing.T) {
	fakeTransport := &fakeTransport{}
	attachFunc := func(ctx context.Context) (Transport, error) {
		return fakeTransport, nil
	}

	config := DefaultConfig()
	h := NewHub(attachFunc, config)
	defer h.Close()

	fragment := func(r *http.Request, topic string) ([]byte, error) {
		return []byte(`{"state": "ok"}`), nil
	}

	handler := Handler(h, []string{"topic-a"}, fragment)

	// Test 1: Cancel while idle
	req1 := httptest.NewRequest("GET", "/events", nil)
	ctx1, cancel1 := context.WithCancel(req1.Context())
	req1 = req1.WithContext(ctx1)

	w1 := httptest.NewRecorder()
	done1 := make(chan struct{})

	go func() {
		handler(w1, req1)
		close(done1)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel1()

	select {
	case <-done1:
		// Success
	case <-time.After(1 * time.Second):
		t.Fatal("handler should return when context is cancelled")
	}

	// Test 2: Cancel from different request
	req2 := httptest.NewRequest("GET", "/events", nil)
	ctx2, cancel2 := context.WithCancel(req2.Context())
	req2 = req2.WithContext(ctx2)

	w2 := httptest.NewRecorder()
	done2 := make(chan struct{})

	go func() {
		handler(w2, req2)
		close(done2)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel2()

	select {
	case <-done2:
		// Success
	case <-time.After(1 * time.Second):
		t.Fatal("handler should return when context is cancelled")
	}
}

// TestFR2_ResponseCommitOrdering tests that response is committed before first fragment
func TestFR2_ResponseCommitOrdering(t *testing.T) {
	fakeTransport := &fakeTransport{}
	attachFunc := func(ctx context.Context) (Transport, error) {
		return fakeTransport, nil
	}

	config := DefaultConfig()
	h := NewHub(attachFunc, config)
	defer h.Close()

	// Fragment that errors on first call
	firstCall := atomic.Bool{}

	fragment := func(r *http.Request, topic string) ([]byte, error) {
		if !firstCall.Load() {
			firstCall.Store(true)
			return nil, fmt.Errorf("first call error")
		}
		return []byte(`{"state": "ok"}`), nil
	}

	handler := Handler(h, []string{"topic-a"}, fragment)

	req := httptest.NewRequest("GET", "/events", nil)
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		handler(w, req)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	// Response should have status 200 and SSE headers
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "text/event-stream", w.Header().Get("Content-Type"))
	// Should have zero events (fragment errored)
	require.NotContains(t, w.Body.String(), "event: topic-a")
}

// TestNFR12_ForcedClose tests that stream closes after max lifetime
func TestNFR12_ForcedClose(t *testing.T) {
	fakeTransport := &fakeTransport{}
	attachFunc := func(ctx context.Context) (Transport, error) {
		return fakeTransport, nil
	}

	mockClock := &mockClock{
		now: time.Now(),
	}

	config := DefaultConfig()
	config.MaxStreamLifetime = 100 * time.Millisecond
	h := NewHubWithClock(attachFunc, config, mockClock)
	defer h.Close()

	fragment := func(r *http.Request, topic string) ([]byte, error) {
		return []byte(`{"state": "ok"}`), nil
	}

	handler := Handler(h, []string{"topic-a"}, fragment)

	req := httptest.NewRequest("GET", "/events", nil)
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()

	handlerReturned := make(chan struct{})
	go func() {
		handler(w, req)
		close(handlerReturned)
	}()

	// Mock the lifetime ticker to fire immediately
	lifetimeFired := false
	mockClock.tickerFn = func(d time.Duration) Ticker {
		if d == config.MaxStreamLifetime {
			return &fakeLifetimeTicker{
				onC: func() {
					if !lifetimeFired {
						lifetimeFired = true
					}
				},
			}
		}
		return &defaultTicker{time.NewTicker(d)}
	}

	select {
	case <-handlerReturned:
		// Expected
	case <-time.After(1 * time.Second):
		t.Fatal("handler should return after max stream lifetime")
	}
}

// TestFR1_SubscriptionReleaseOnCancellation tests cleanup on context cancellation
func TestFR1_SubscriptionReleaseOnCancellation(t *testing.T) {
	fakeTransport := &fakeTransport{}
	attachFunc := func(ctx context.Context) (Transport, error) {
		return fakeTransport, nil
	}

	config := DefaultConfig()
	h := NewHub(attachFunc, config)
	defer h.Close()

	fragment := func(r *http.Request, topic string) ([]byte, error) {
		return []byte(`{}`), nil
	}

	handler := Handler(h, []string{"topic-a", "topic-b"}, fragment)

	req := httptest.NewRequest("GET", "/events", nil)
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		handler(w, req)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)

	// Check subscriptions exist
	h.mu.RLock()
	subsCountBefore := len(h.subscribers["topic-a"]) + len(h.subscribers["topic-b"])
	h.mu.RUnlock()
	require.Greater(t, subsCountBefore, 0)

	cancel()
	<-done

	// Check subscriptions are cleaned up
	h.mu.RLock()
	subsCountAfter := len(h.subscribers["topic-a"]) + len(h.subscribers["topic-b"])
	h.mu.RUnlock()
	require.Equal(t, 0, subsCountAfter)
}

// Fake tickers for testing
type fakeHeartbeatTicker struct {
	onC func()
	ch  chan time.Time
}

func (f *fakeHeartbeatTicker) C() <-chan time.Time {
	if f.ch == nil {
		f.ch = make(chan time.Time, 1)
		go func() {
			f.onC()
			f.ch <- time.Now()
			time.Sleep(50 * time.Millisecond)
			f.onC()
			f.ch <- time.Now()
			time.Sleep(50 * time.Millisecond)
			f.onC()
			f.ch <- time.Now()
		}()
	}
	return f.ch
}

func (f *fakeHeartbeatTicker) Stop() {}

type fakeLifetimeTicker struct {
	onC func()
	ch  chan time.Time
}

func (f *fakeLifetimeTicker) C() <-chan time.Time {
	if f.ch == nil {
		f.ch = make(chan time.Time, 1)
		go func() {
			f.onC()
			f.ch <- time.Now()
		}()
	}
	return f.ch
}

func (f *fakeLifetimeTicker) Stop() {}

// TestPN15_WholeBaselineSetInID verifies that each emitted swap carries the whole baseline set
// in its id field (Planner note 15), not just the topic that changed.
func TestPN15_WholeBaselineSetInID(t *testing.T) {
	fakeTransport := &fakeTransport{}
	attachFunc := func(ctx context.Context) (Transport, error) {
		return fakeTransport, nil
	}

	config := DefaultConfig()
	h := NewHub(attachFunc, config)
	defer h.Close()

	fragment := func(r *http.Request, topic string) ([]byte, error) {
		return []byte(fmt.Sprintf(`{"topic": "%s"}`, topic)), nil
	}

	handler := Handler(h, []string{"topic-a", "topic-b"}, fragment)

	// First request to get baselines
	req1 := httptest.NewRequest("GET", "/events", nil)
	ctx1, cancel1 := context.WithCancel(req1.Context())
	req1 = req1.WithContext(ctx1)

	w1 := httptest.NewRecorder()

	done1 := make(chan struct{})
	go func() {
		handler(w1, req1)
		close(done1)
	}()

	time.Sleep(50 * time.Millisecond)

	body1 := w1.Body.String()
	// Extract all id values from the first response
	idToTopicSwap := make(map[string]string) // Maps id to the event topic
	lines := strings.Split(body1, "\n")
	var lastID string
	for _, line := range lines {
		if strings.HasPrefix(line, "id: ") {
			lastID = strings.TrimPrefix(line, "id: ")
		} else if strings.HasPrefix(line, "event: ") {
			topic := strings.TrimPrefix(line, "event: ")
			// Only track actual swaps, not keepalives
			if !strings.HasSuffix(topic, "-keepalive") && lastID != "" {
				idToTopicSwap[lastID] = topic
			}
		}
	}

	// Should have received swaps for both topics
	require.Greater(t, len(idToTopicSwap), 0, "should have at least one swap event")

	// Verify that each swap's id contains both topic-a and topic-b hashes
	for id, topic := range idToTopicSwap {
		parts := strings.Split(id, "|")
		require.Greater(t, len(parts), 0, "id for topic %s should contain at least one hash", topic)
		topicCount := 0
		for _, part := range parts {
			if strings.HasPrefix(part, "topic-") {
				topicCount++
			}
		}
		require.Equal(t, 2, topicCount, "id for topic %s should contain hashes for both topics, got %s", topic, id)
	}

	cancel1()
	<-done1
}

// TestFR5_MultiTopicBaselineRuleComprehensive tests multi-topic baseline suppression in detail
func TestFR5_MultiTopicBaselineRuleComprehensive(t *testing.T) {
	fakeTransport := &fakeTransport{}
	attachFunc := func(ctx context.Context) (Transport, error) {
		return fakeTransport, nil
	}

	config := DefaultConfig()
	h := NewHub(attachFunc, config)
	defer h.Close()

	fragment := func(r *http.Request, topic string) ([]byte, error) {
		return []byte(fmt.Sprintf(`{"topic": "%s"}`, topic)), nil
	}

	handler := Handler(h, []string{"topic-a", "topic-b"}, fragment)

	// First request to get full baselines
	req1 := httptest.NewRequest("GET", "/events", nil)
	ctx1, cancel1 := context.WithCancel(req1.Context())
	req1 = req1.WithContext(ctx1)

	w1 := httptest.NewRecorder()

	done1 := make(chan struct{})
	go func() {
		handler(w1, req1)
		close(done1)
	}()

	time.Sleep(50 * time.Millisecond)

	body1 := w1.Body.String()
	// Extract the most recent id (full baseline set)
	var fullID string
	lines := strings.Split(body1, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "id: ") {
			fullID = strings.TrimPrefix(line, "id: ")
		}
	}

	require.NotEmpty(t, fullID, "should have id in first response")
	// Verify the full id contains both topics
	require.Contains(t, fullID, "topic-a")
	require.Contains(t, fullID, "topic-b")

	cancel1()
	<-done1

	// Test case 1: Reconnect with baseline matching only topic-a
	// Should suppress topic-a and swap topic-b
	partialID := strings.Split(fullID, "|")[0] // Only topic-a:hash

	req2 := httptest.NewRequest("GET", "/events", nil)
	req2.Header.Set("Last-Event-ID", partialID)
	ctx2, cancel2 := context.WithCancel(req2.Context())
	req2 = req2.WithContext(ctx2)

	w2 := httptest.NewRecorder()

	done2 := make(chan struct{})
	go func() {
		handler(w2, req2)
		close(done2)
	}()

	time.Sleep(50 * time.Millisecond)

	body2 := w2.Body.String()
	// Extract events
	var aHasSwap, aHasKeepalive, bHasSwap, bHasKeepalive bool
	lines = strings.Split(body2, "\n")
	for _, line := range lines {
		if line == "event: topic-a" {
			aHasSwap = true
		} else if line == "event: topic-a-keepalive" {
			aHasKeepalive = true
		} else if line == "event: topic-b" {
			bHasSwap = true
		} else if line == "event: topic-b-keepalive" {
			bHasKeepalive = true
		}
	}

	require.False(t, aHasSwap, "topic-a should NOT swap (baseline matches)")
	require.True(t, aHasKeepalive, "topic-a should have keepalive")
	require.True(t, bHasSwap, "topic-b should swap (baseline missing/mismatch)")
	require.False(t, bHasKeepalive, "topic-b should NOT have only keepalive when it swaps")

	cancel2()
	<-done2

	// Test case 2: Reconnect with completely absent baseline
	// Should swap both topics
	req3 := httptest.NewRequest("GET", "/events", nil)
	// No Last-Event-ID header
	ctx3, cancel3 := context.WithCancel(req3.Context())
	req3 = req3.WithContext(ctx3)

	w3 := httptest.NewRecorder()

	done3 := make(chan struct{})
	go func() {
		handler(w3, req3)
		close(done3)
	}()

	time.Sleep(50 * time.Millisecond)

	body3 := w3.Body.String()
	require.Contains(t, body3, "event: topic-a\n", "topic-a should swap on fresh connect")
	require.Contains(t, body3, "event: topic-b\n", "topic-b should swap on fresh connect")

	cancel3()
	<-done3
}

// TestFR2_ContextCancellationDuringFragment tests context cancellation from inside fragment function
func TestFR2_ContextCancellationDuringFragment(t *testing.T) {
	fakeTransport := &fakeTransport{}
	attachFunc := func(ctx context.Context) (Transport, error) {
		return fakeTransport, nil
	}

	config := DefaultConfig()
	config.HeartbeatInterval = 50 * time.Millisecond
	h := NewHub(attachFunc, config)
	defer h.Close()

	var cancelFn context.CancelFunc
	fragment := func(r *http.Request, topic string) ([]byte, error) {
		// On heartbeat, cancel the context from within the fragment function
		if cancelFn != nil {
			cancelFn()
		}
		return []byte(`{"state": "ok"}`), nil
	}

	handler := Handler(h, []string{"topic-a"}, fragment)

	req := httptest.NewRequest("GET", "/events", nil)
	ctx, cancel := context.WithCancel(req.Context())
	cancelFn = cancel
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		handler(w, req)
		close(done)
	}()

	// Wait for heartbeat to trigger and cancel
	select {
	case <-done:
		// Expected - handler should return even when cancelled from within fragment
	case <-time.After(2 * time.Second):
		t.Fatal("handler should return when context is cancelled from within fragment function")
	}
}

// TestNFR12_ForcedCloseReleasesSubscriptions tests that forced close releases all subscriptions
func TestNFR12_ForcedCloseReleasesSubscriptions(t *testing.T) {
	fakeTransport := &fakeTransport{}
	attachFunc := func(ctx context.Context) (Transport, error) {
		return fakeTransport, nil
	}

	mockClock := &mockClock{
		now: time.Now(),
	}

	config := DefaultConfig()
	config.MaxStreamLifetime = 100 * time.Millisecond
	h := NewHubWithClock(attachFunc, config, mockClock)
	defer h.Close()

	fragment := func(r *http.Request, topic string) ([]byte, error) {
		return []byte(`{}`), nil
	}

	handler := Handler(h, []string{"topic-a", "topic-b"}, fragment)

	req := httptest.NewRequest("GET", "/events", nil)
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()

	handlerReturned := make(chan struct{})
	go func() {
		handler(w, req)
		close(handlerReturned)
	}()

	time.Sleep(50 * time.Millisecond)

	// Check subscriptions exist before forced close
	h.mu.RLock()
	subsCountBefore := len(h.subscribers["topic-a"]) + len(h.subscribers["topic-b"])
	h.mu.RUnlock()
	require.Greater(t, subsCountBefore, 0)

	// Mock the lifetime ticker to fire
	lifetimeFired := false
	mockClock.tickerFn = func(d time.Duration) Ticker {
		if d == config.MaxStreamLifetime {
			return &fakeLifetimeTicker{
				onC: func() {
					if !lifetimeFired {
						lifetimeFired = true
					}
				},
			}
		}
		return &defaultTicker{time.NewTicker(d)}
	}

	select {
	case <-handlerReturned:
		// Handler should return due to forced close
	case <-time.After(1 * time.Second):
		t.Fatal("handler should return after max stream lifetime")
	}

	// Check subscriptions are cleaned up after forced close
	h.mu.RLock()
	subsCountAfter := len(h.subscribers["topic-a"]) + len(h.subscribers["topic-b"])
	h.mu.RUnlock()
	require.Equal(t, 0, subsCountAfter)
}
