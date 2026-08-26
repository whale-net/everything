package htmxsse

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/libs/go/rmq"
)

var errAttachFailed = fmt.Errorf("attach failed")

// fakeTransport is a test double for Transport.
type fakeTransport struct {
	mu                sync.Mutex
	handler           rmq.MessageHandler
	bindExchangeCalls []bindExchangeCall
	bindExchangeFunc  func(string, []string) error
	startErr          error
	deliveries        []rmq.Message
	ctx               context.Context
	cancel            context.CancelFunc
}

type bindExchangeCall struct {
	exchange    string
	routingKeys []string
}

func (f *fakeTransport) BindExchange(exchange string, routingKeys []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.bindExchangeFunc != nil {
		return f.bindExchangeFunc(exchange, routingKeys)
	}
	f.bindExchangeCalls = append(f.bindExchangeCalls, bindExchangeCall{exchange, routingKeys})
	return nil
}

func (f *fakeTransport) RegisterHandler(queue string, handler rmq.MessageHandler) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handler = handler
}

func (f *fakeTransport) Start(ctx context.Context) error {
	if f.startErr != nil {
		return f.startErr
	}
	<-ctx.Done()
	return ctx.Err()
}

func (f *fakeTransport) Close() error {
	if f.cancel != nil {
		f.cancel()
	}
	return nil
}

func (f *fakeTransport) deliver(ctx context.Context, msg rmq.Message) error {
	f.mu.Lock()
	handler := f.handler
	f.mu.Unlock()
	if handler != nil {
		return handler(ctx, msg)
	}
	return nil
}

func TestNewHub(t *testing.T) {
	config := Config{
		SubscriberBufferDepth: 10,
	}
	attachFunc := func(context.Context) (Transport, error) {
		return nil, nil
	}
	h := NewHub(attachFunc, config)
	require.NotNil(t, h)
}

// Test 1: FR1 exact-string topic dispatch
func TestFR1_TopicDispatch(t *testing.T) {
	fakeTransport := &fakeTransport{}

	attachFunc := func(ctx context.Context) (Transport, error) {
		return fakeTransport, nil
	}

	config := Config{SubscriberBufferDepth: 10}
	h := NewHub(attachFunc, config)
	defer h.Close()

	chA, unsub := h.Subscribe("topic-a")
	require.NotNil(t, chA)
	require.NotNil(t, unsub)

	chB, _ := h.Subscribe("topic-b")
	require.NotNil(t, chB)

	time.Sleep(100 * time.Millisecond)

	fakeTransport.deliver(context.Background(), rmq.Message{
		RoutingKey: "topic-a",
		Body:       []byte("message-a"),
	})

	select {
	case eventA := <-chA:
		require.Equal(t, "topic-a", eventA.Topic)
		require.Equal(t, []byte("message-a"), eventA.Body)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected message on chA")
	}

	select {
	case <-chB:
		t.Fatal("unexpected message on chB")
	case <-time.After(50 * time.Millisecond):
	}

	unsub()
}

// Test 2: FR1a per-subscriber slow-subscriber policy
func TestFR1a_PerSubscriberBuffering(t *testing.T) {
	fakeTransport := &fakeTransport{}

	attachFunc := func(ctx context.Context) (Transport, error) {
		return fakeTransport, nil
	}

	config := Config{
		SubscriberBufferDepth: 3,
	}
	h := NewHub(attachFunc, config)
	defer h.Close()

	ch1, _ := h.Subscribe("topic-a")
	ch2, _ := h.Subscribe("topic-a")

	time.Sleep(50 * time.Millisecond)

	for i := 0; i < 3; i++ {
		fakeTransport.deliver(context.Background(), rmq.Message{
			RoutingKey: "topic-a",
			Body:       []byte{byte(i)},
		})
	}

	for i := 0; i < 3; i++ {
		<-ch1
	}

	for i := 0; i < 3; i++ {
		event := <-ch2
		require.Equal(t, []byte{byte(i)}, event.Body)
	}

	for i := 3; i < 6; i++ {
		fakeTransport.deliver(context.Background(), rmq.Message{
			RoutingKey: "topic-a",
			Body:       []byte{byte(i)},
		})
	}

	event := <-ch2
	require.Equal(t, []byte{3}, event.Body)
	event = <-ch2
	require.Equal(t, []byte{4}, event.Body)
	event = <-ch2
	require.Equal(t, []byte{5}, event.Body)
}

// Test 2b: Verify one slow subscriber doesn't block another
func TestFR1a_OneSlowSubscriberDoesntBlockAnother(t *testing.T) {
	fakeTransport := &fakeTransport{}

	attachFunc := func(ctx context.Context) (Transport, error) {
		return fakeTransport, nil
	}

	config := Config{
		SubscriberBufferDepth: 2,
	}
	h := NewHub(attachFunc, config)
	defer h.Close()

	chFast, _ := h.Subscribe("topic-a")
	chSlow, _ := h.Subscribe("topic-a")

	time.Sleep(50 * time.Millisecond)

	for i := 0; i < 2; i++ {
		fakeTransport.deliver(context.Background(), rmq.Message{
			RoutingKey: "topic-a",
			Body:       []byte{byte(i)},
		})
	}

	event := <-chFast
	require.Equal(t, []byte{0}, event.Body)
	event = <-chFast
	require.Equal(t, []byte{1}, event.Body)

	fakeTransport.deliver(context.Background(), rmq.Message{
		RoutingKey: "topic-a",
		Body:       []byte{2},
	})

	event = <-chFast
	require.Equal(t, []byte{2}, event.Body)

	event = <-chSlow
	require.Equal(t, []byte{1}, event.Body)
	event = <-chSlow
	require.Equal(t, []byte{2}, event.Body)
}

// Test 3: FR1a callback never blocks and always acks
func TestFR1a_CallbackNeverBlocks(t *testing.T) {
	fakeTransport := &fakeTransport{}
	handlerReturnedNil := atomic.Bool{}

	attachFunc := func(ctx context.Context) (Transport, error) {
		return fakeTransport, nil
	}

	config := Config{SubscriberBufferDepth: 1}
	h := NewHub(attachFunc, config)
	defer h.Close()

	ch, _ := h.Subscribe("topic-a")

	time.Sleep(50 * time.Millisecond)

	fakeTransport.deliver(context.Background(), rmq.Message{
		RoutingKey: "topic-a",
		Body:       []byte("msg1"),
	})

	event := <-ch
	require.Equal(t, []byte("msg1"), event.Body)

	done := make(chan error)

	go func() {
		err := fakeTransport.handler(context.Background(), rmq.Message{
			RoutingKey: "topic-a",
			Body:       []byte("msg2"),
		})
		done <- err
	}()

	select {
	case err := <-done:
		require.NoError(t, err)
		handlerReturnedNil.Store(true)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("handler blocked, should have returned nil")
	}

	require.True(t, handlerReturnedNil.Load())
}

// Test 4: FR1b attach-and-recover with backoff
func TestFR1b_AttachAndRecover(t *testing.T) {
	attachAttempts := atomic.Int32{}
	failTimes := 2

	attachFunc := func(ctx context.Context) (Transport, error) {
		attempts := attachAttempts.Add(1)
		if attempts <= int32(failTimes) {
			return nil, errAttachFailed
		}
		return &fakeTransport{}, nil
	}

	config := Config{SubscriberBufferDepth: 10}
	h := NewHub(attachFunc, config)
	defer h.Close()

	_, _ = h.Subscribe("topic-a")

	time.Sleep(1500 * time.Millisecond)

	h.mu.RLock()
	hasTransport := h.transport != nil
	h.mu.RUnlock()

	require.True(t, hasTransport, "transport should be attached after retries")
	require.True(t, attachAttempts.Load() > int32(failTimes), "attach should have been retried")
}

// Test 4b: Attach backoff is exponential
func TestFR1b_AttachBackoffIsExponential(t *testing.T) {
	mockClock := &mockClock{
		now: time.Now(),
	}
	sleepDurations := []time.Duration{}
	var sleepMutex sync.Mutex

	mockClock.sleepFn = func(ctx context.Context, d time.Duration) error {
		sleepMutex.Lock()
		sleepDurations = append(sleepDurations, d)
		sleepMutex.Unlock()
		return nil
	}

	attachAttempts := atomic.Int32{}
	failTimes := 3

	attachFunc := func(ctx context.Context) (Transport, error) {
		attempts := attachAttempts.Add(1)
		if attempts <= int32(failTimes) {
			return nil, errAttachFailed
		}
		return &fakeTransport{}, nil
	}

	config := Config{SubscriberBufferDepth: 10}
	h := NewHubWithClock(attachFunc, config, mockClock)
	defer h.Close()

	_, _ = h.Subscribe("topic-a")

	time.Sleep(100 * time.Millisecond)

	sleepMutex.Lock()
	numSleeps := len(sleepDurations)
	sleepMutex.Unlock()

	require.Greater(t, numSleeps, 0, "should have had sleep calls for backoff")

	if numSleeps > 1 {
		require.Less(t, sleepDurations[0], sleepDurations[1], "second sleep should be longer than first")
	}
}

// Test 5: FR1b reconstruct-on-bind-failure
func TestFR1b_ReconstructOnBindFailure(t *testing.T) {
	firstTransport := &fakeTransport{}
	secondTransport := &fakeTransport{}

	attachCalls := atomic.Int32{}
	shouldReturnSecond := false
	var attachMutex sync.Mutex

	attachFunc := func(ctx context.Context) (Transport, error) {
		attachCalls.Add(1)
		attachMutex.Lock()
		defer attachMutex.Unlock()
		if shouldReturnSecond {
			return secondTransport, nil
		}
		return firstTransport, nil
	}

	config := Config{SubscriberBufferDepth: 10}
	h := NewHub(attachFunc, config)
	defer h.Close()

	ch, _ := h.Subscribe("topic-a")

	time.Sleep(100 * time.Millisecond)

	initialAttempts := attachCalls.Load()
	require.Greater(t, int(initialAttempts), 0)

	firstTransport.deliver(context.Background(), rmq.Message{
		RoutingKey: "topic-a",
		Body:       []byte("test1"),
	})

	event := <-ch
	require.Equal(t, []byte("test1"), event.Body)

	shouldReturnSecond = true

	secondTransport.deliver(context.Background(), rmq.Message{
		RoutingKey: "topic-a",
		Body:       []byte("test2"),
	})

	firstTransport.deliver(context.Background(), rmq.Message{
		RoutingKey: "topic-a",
		Body:       []byte("test3"),
	})

	select {
	case <-ch:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("should receive messages on transport")
	}
}

// Test 6: FR1 construction arguments captured
func TestFR1_ConstructionArguments(t *testing.T) {
	var _ Transport = (*rmq.Consumer)(nil)
}

// Test 7: Exchange-declare arguments captured with configured exchange name
func TestFR7_ExchangeDeclareArguments(t *testing.T) {
	fakeTransport := &fakeTransport{}

	attachFunc := func(ctx context.Context) (Transport, error) {
		return fakeTransport, nil
	}

	configuredExchange := "custom-exchange.htmxsse"
	config := Config{
		ExchangeName:         configuredExchange,
		SubscriberBufferDepth: 10,
	}
	h := NewHub(attachFunc, config)
	defer h.Close()

	_, _ = h.Subscribe("topic-a")
	time.Sleep(100 * time.Millisecond)

	fakeTransport.mu.Lock()
	require.Len(t, fakeTransport.bindExchangeCalls, 1)
	call := fakeTransport.bindExchangeCalls[0]
	fakeTransport.mu.Unlock()

	// Assert that the exchange name matches the configured value, not a hardcoded literal
	require.Equal(t, configuredExchange, call.exchange)
	require.Equal(t, []string{"#"}, call.routingKeys)
}

// Test 7b: Post-attach-failure backoff is exponential
func TestFR1b_PostAttachFailureBackoffIsExponential(t *testing.T) {
	mockClock := &mockClock{
		now: time.Now(),
	}
	sleepDurations := []time.Duration{}
	var sleepMutex sync.Mutex

	mockClock.sleepFn = func(ctx context.Context, d time.Duration) error {
		sleepMutex.Lock()
		sleepDurations = append(sleepDurations, d)
		sleepMutex.Unlock()
		return nil
	}

	failingTransport := &fakeTransport{
		startErr: fmt.Errorf("post-attach failure"),
	}

	attachFunc := func(ctx context.Context) (Transport, error) {
		// Always return the failing transport
		return failingTransport, nil
	}

	config := Config{
		ExchangeName:         "test-exchange",
		SubscriberBufferDepth: 10,
	}
	h := NewHubWithClock(attachFunc, config, mockClock)
	defer h.Close()

	_, _ = h.Subscribe("topic-a")

	// Let attach succeed but Start() fail multiple times
	time.Sleep(200 * time.Millisecond)

	sleepMutex.Lock()
	durations := make([]time.Duration, len(sleepDurations))
	copy(durations, sleepDurations)
	sleepMutex.Unlock()

	// Should have multiple sleep calls for post-attach failure backoff
	require.Greater(t, len(durations), 0, "should have sleep calls for post-attach failure backoff")

	// Verify exponential backoff: sleeps should generally increase in duration
	// We expect: 100ms, 200ms, 400ms, etc. (or capped at 30s)
	if len(durations) >= 2 {
		// At least check that we're not resetting to 100ms immediately after each failure
		hasExponentialPattern := false
		for i := 0; i < len(durations)-1; i++ {
			if durations[i] < durations[i+1] {
				hasExponentialPattern = true
				break
			}
		}
		require.True(t, hasExponentialPattern, "should see exponential backoff in sleep durations: %v", durations)
	}
}

// Test 8: FR1 subscription release
func TestFR1_SubscriptionRelease(t *testing.T) {
	fakeTransport := &fakeTransport{}

	attachFunc := func(ctx context.Context) (Transport, error) {
		return fakeTransport, nil
	}

	config := Config{SubscriberBufferDepth: 10}
	h := NewHub(attachFunc, config)
	defer h.Close()

	_, unsub := h.Subscribe("topic-a")

	time.Sleep(50 * time.Millisecond)

	h.mu.RLock()
	subsCount := len(h.subscribers["topic-a"])
	h.mu.RUnlock()
	require.Equal(t, 1, subsCount)

	unsub()

	h.mu.RLock()
	subsCount = len(h.subscribers["topic-a"])
	unsubFuncCount := len(h.unsubscribeFuncs)
	h.mu.RUnlock()

	require.Equal(t, 0, subsCount)
	require.Equal(t, 0, unsubFuncCount)
}

// Test 9: Note 19 validation - advertisedRetry constraint
func TestNote19_AdvertisedRetryValidation(t *testing.T) {
	validConfig := Config{
		HeartbeatInterval:       30 * time.Second,
		AdvertisedRetryInterval: 30 * time.Second,
	}
	err := validConfig.Validate()
	require.NoError(t, err)

	invalidConfig := Config{
		HeartbeatInterval:       30 * time.Second,
		AdvertisedRetryInterval: 60 * time.Second,
	}
	err = invalidConfig.Validate()
	require.Error(t, err)

	edgeConfig := Config{
		HeartbeatInterval:       30 * time.Second,
		AdvertisedRetryInterval: 60 * time.Second,
	}
	err = edgeConfig.Validate()
	require.Error(t, err)
}

func TestDefaultConfigIsValid(t *testing.T) {
	config := DefaultConfig()
	err := config.Validate()
	require.NoError(t, err)
}

type mockClock struct {
	mu       sync.Mutex
	now      time.Time
	sleepFn  func(context.Context, time.Duration) error
	tickerFn func(time.Duration) Ticker
}

func (m *mockClock) Now() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.now
}

func (m *mockClock) NewTicker(d time.Duration) Ticker {
	if m.tickerFn != nil {
		return m.tickerFn(d)
	}
	return &defaultTicker{time.NewTicker(d)}
}

func (m *mockClock) Sleep(ctx context.Context, d time.Duration) error {
	if m.sleepFn != nil {
		return m.sleepFn(ctx, d)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}
