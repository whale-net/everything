package monty

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	pb "github.com/whale-net/everything/libs/go/monty/protos"
)

// newPoolWithConns builds a pool whose transport is a fake, handing the test
// the list of connections it handed out so it can script and inspect each one.
// The i-th call to NewConn gets scripts[i]; once the scripts run out the last
// one repeats.
func newPoolWithConns(t *testing.T, cfg Config, scripts ...[]*pb.ChildEvent) (*Pool, *[]*fakeConn) {
	t.Helper()
	made := []*fakeConn{}
	cfg.NewConn = func(context.Context, *Config) (Conn, error) {
		script := scripts[len(scripts)-1]
		if len(made) < len(scripts) {
			script = scripts[len(made)]
		}
		c := newFakeConn(script...)
		made = append(made, c)
		return c, nil
	}
	p, err := NewPool(cfg)
	require.NoError(t, err)
	return p, &made
}

// TestNewPoolNoWorkerBinary: with nothing to resolve, NewPool must fail rather
// than hand back a pool whose first Checkout dies.
func TestNewPoolNoWorkerBinary(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("MONTY_BIN", "")

	p, err := NewPool(Config{})
	require.Error(t, err)
	assert.Nil(t, p)
	assert.Contains(t, err.Error(), "no worker binary")
	assert.Contains(t, err.Error(), "Config.Binary")
}

func TestNewPoolMissingExplicitBinary(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	p, err := NewPool(Config{Binary: filepath.Join(t.TempDir(), "monty")})
	require.Error(t, err)
	assert.Nil(t, p)
	assert.Contains(t, err.Error(), "worker binary")
}

// TestNewPoolCallerTransportIsNotReusable: a caller-supplied transport is
// opaque and may be single-use, so the pool must never hand a spent connection
// to a later session.
func TestNewPoolCallerTransportIsNotReusable(t *testing.T) {
	p, _ := newPoolWithConns(t, Config{}, []*pb.ChildEvent{evtOK()})

	assert.False(t, p.reusable)
}

func TestPoolCloseIsIdempotent(t *testing.T) {
	p, _ := newPoolWithConns(t, Config{}, []*pb.ChildEvent{evtOK()})
	require.NoError(t, p.Close())
	require.NoError(t, p.Close())
}

// TestCheckoutSendsConfigure: the Configure a session starts with has to carry
// this client's protocol version and every limit the caller set, because a zero
// limit field means "the worker's choice" -- not "unlimited".
func TestCheckoutSendsConfigure(t *testing.T) {
	p, conns := newPoolWithConns(t, Config{
		ScriptName: "app.py",
		TypeCheck:  true,
		Limits: Limits{
			MaxMemoryBytes:    64 << 20,
			MaxFeedDuration:   2 * time.Second,
			MaxTurnDuration:   5 * time.Second,
			MaxRecursionDepth: 32,
			MaxSuspensions:    7,
			MaxTotalSleep:     time.Second,
		},
	}, []*pb.ChildEvent{evtOK()})

	s, err := p.Checkout(context.Background())
	require.NoError(t, err)
	require.NotNil(t, s)

	conf := (*conns)[0].requests()[0].GetConfigure()
	require.NotNil(t, conf)
	assert.Equal(t, uint32(ProtocolVersion), conf.GetProtocolVersion())
	assert.Equal(t, "app.py", conf.GetScriptName())
	assert.True(t, conf.GetTypeCheck())

	limits := conf.GetLimits()
	require.NotNil(t, limits)
	require.NotNil(t, limits.MaxMemoryBytes)
	assert.Equal(t, uint64(64<<20), limits.GetMaxMemoryBytes())
	require.NotNil(t, limits.MaxFeedDurationMicros)
	assert.Equal(t, uint64(2_000_000), limits.GetMaxFeedDurationMicros())
	require.NotNil(t, limits.MaxTurnDurationMicros)
	assert.Equal(t, uint64(5_000_000), limits.GetMaxTurnDurationMicros())
	require.NotNil(t, limits.MaxRecursionDepth)
	assert.Equal(t, uint64(32), limits.GetMaxRecursionDepth())
	require.NotNil(t, limits.MaxSuspensions)
	assert.Equal(t, uint64(7), limits.GetMaxSuspensions())
	require.NotNil(t, limits.MaxTotalSleepMicros)
	assert.Equal(t, uint64(1_000_000), limits.GetMaxTotalSleepMicros())
}

func TestCheckoutOmitsUnsetLimits(t *testing.T) {
	p, conns := newPoolWithConns(t, Config{}, []*pb.ChildEvent{evtOK()})

	s, err := p.Checkout(context.Background())
	require.NoError(t, err)

	limits := (*conns)[0].requests()[0].GetConfigure().GetLimits()
	require.NotNil(t, limits)
	assert.Nil(t, limits.MaxMemoryBytes, "an unset limit stays unset, not zero")
	assert.Nil(t, limits.MaxFeedDurationMicros)
	assert.Nil(t, limits.MaxTurnDurationMicros)
	assert.Nil(t, limits.MaxRecursionDepth)
	assert.Nil(t, limits.MaxTotalSleepMicros)
	// The suspension budget is the one limit this client always sets, because
	// the host's limit is what protects a host function with side effects.
	require.NotNil(t, limits.MaxSuspensions)
	assert.Equal(t, uint64(defaultSuspensionLimit), limits.GetMaxSuspensions())

	require.NoError(t, s.Close())
}

func TestCheckoutPrintFlushInterval(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   time.Duration
		want *uint32
	}{
		// Zero is the line-buffering sentinel on the wire, so an unset
		// interval has to travel as "absent" and a negative one as an
		// explicit 0.
		{"unset", 0, nil},
		{"explicit", 250 * time.Millisecond, ptrU32(250)},
		{"line buffered", -1, ptrU32(0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, conns := newPoolWithConns(t, Config{PrintFlushInterval: tc.in}, []*pb.ChildEvent{evtOK()})
			s, err := p.Checkout(context.Background())
			require.NoError(t, err)
			defer s.Close()

			got := (*conns)[0].requests()[0].GetConfigure().PrintFlushIntervalMs
			if tc.want == nil {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, *tc.want, *got)
		})
	}
}

func TestCheckoutFatalErrorIsVersionMismatch(t *testing.T) {
	p, conns := newPoolWithConns(t, Config{},
		[]*pb.ChildEvent{evtFatal("worker speaks protocol version 3, not 5")})

	s, err := p.Checkout(context.Background())
	require.Error(t, err)
	assert.Nil(t, s)
	assert.ErrorIs(t, err, ErrVersion)
	assert.Contains(t, err.Error(), "protocol version 3")
	assert.True(t, (*conns)[0].closed, "a worker that refused Configure is not reusable")
}

func TestCheckoutUnexpectedConfigureReplyIsProtocolViolation(t *testing.T) {
	arena, roots := mustArena(t, int64(1))
	p, _ := newPoolWithConns(t, Config{}, []*pb.ChildEvent{evtComplete(roots[0], arena)})

	s, err := p.Checkout(context.Background())
	require.Error(t, err)
	assert.Nil(t, s)
	assert.ErrorIs(t, err, ErrProtocol)
	assert.Contains(t, err.Error(), "Configure")
}

func TestCheckoutSendFailure(t *testing.T) {
	conn := newFakeConn()
	conn.closed = true
	p, err := NewPool(Config{NewConn: func(context.Context, *Config) (Conn, error) { return conn, nil }})
	require.NoError(t, err)

	_, err = p.Checkout(context.Background())
	assert.ErrorIs(t, err, ErrClosed)
}

func TestCheckoutNewConnError(t *testing.T) {
	p, err := NewPool(Config{
		NewConn: func(context.Context, *Config) (Conn, error) {
			return nil, assert.AnError
		},
	})
	require.NoError(t, err)

	_, err = p.Checkout(context.Background())
	assert.ErrorIs(t, err, assert.AnError)
}

// TestCheckoutFailureReleasesItsSlot: a cancelled checkout must not leave the
// pool oversized, so a failed NewConn has to give its slot back.
func TestCheckoutFailureReleasesItsSlot(t *testing.T) {
	calls := 0
	p, err := NewPool(Config{
		MaxWorkers: 1,
		NewConn: func(context.Context, *Config) (Conn, error) {
			calls++
			if calls == 1 {
				return nil, assert.AnError
			}
			return newFakeConn(evtOK()), nil
		},
	})
	require.NoError(t, err)

	_, err = p.Checkout(context.Background())
	require.Error(t, err)
	_, err = p.Checkout(context.Background())
	require.NoError(t, err, "the failed checkout released its slot")
}

// TestPoolMaxWorkersSerialises: a pool of one hands out one session at a time,
// so a second Checkout blocks until the first is closed.
func TestPoolMaxWorkersSerialises(t *testing.T) {
	p, conns := newPoolWithConns(t, Config{MaxWorkers: 1},
		[]*pb.ChildEvent{evtOK()}, []*pb.ChildEvent{evtOK()})

	first, err := p.Checkout(context.Background())
	require.NoError(t, err)

	type result struct {
		s   *Session
		err error
	}
	secondCh := make(chan result, 1)
	go func() {
		s, err := p.Checkout(context.Background())
		secondCh <- result{s, err}
	}()

	select {
	case r := <-secondCh:
		t.Fatalf("second Checkout returned early (err=%v)", r.err)
	case <-time.After(100 * time.Millisecond):
	}

	require.NoError(t, first.Close())

	var second *Session
	select {
	case r := <-secondCh:
		require.NoError(t, r.err)
		require.NotNil(t, r.s)
		second = r.s
	case <-time.After(5 * time.Second):
		t.Fatal("second Checkout did not unblock after the first session closed")
	}
	defer second.Close()
	assert.NotSame(t, first, second)
	assert.Len(t, (*conns)[0].requests(), 1, "only the first Configure was sent")
}

func TestPoolCheckoutTimeout(t *testing.T) {
	p, _ := newPoolWithConns(t, Config{MaxWorkers: 1, CheckoutTimeout: 50 * time.Millisecond},
		[]*pb.ChildEvent{evtOK()})

	held, err := p.Checkout(context.Background())
	require.NoError(t, err)
	defer held.Close()

	start := time.Now()
	_, err = p.Checkout(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), 5*time.Second)
}

func TestPoolCheckoutHonoursCallerContext(t *testing.T) {
	p, _ := newPoolWithConns(t, Config{MaxWorkers: 1}, []*pb.ChildEvent{evtOK()})

	held, err := p.Checkout(context.Background())
	require.NoError(t, err)
	defer held.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = p.Checkout(ctx)
	assert.ErrorIs(t, err, context.Canceled)
}

// TestPoolCapacitySurvivesAnUnblockingClose: a close that wakes a checkout
// waiting for capacity must leave the pool with its capacity intact, so the
// session that was just handed out can also be closed.
func TestPoolCapacitySurvivesAnUnblockingClose(t *testing.T) {
	p, _ := newPoolWithConns(t, Config{MaxWorkers: 1}, []*pb.ChildEvent{evtOK()})

	first, err := p.Checkout(context.Background())
	require.NoError(t, err)

	type result struct {
		s   *Session
		err error
	}
	secondCh := make(chan result, 1)
	go func() {
		s, err := p.Checkout(context.Background())
		secondCh <- result{s, err}
	}()
	select {
	case r := <-secondCh:
		t.Fatalf("second Checkout returned early (err=%v)", r.err)
	case <-time.After(100 * time.Millisecond):
	}

	require.NoError(t, first.Close())

	var second *Session
	select {
	case r := <-secondCh:
		require.NoError(t, r.err)
		second = r.s
	case <-time.After(5 * time.Second):
		t.Fatal("second Checkout did not unblock after the first session closed")
	}

	closed := make(chan error, 1)
	go func() { closed <- second.Close() }()
	select {
	case err := <-closed:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("closing the second session blocked: the first close consumed the pool's capacity")
	}
}

// TestPoolCloseClosesIdleWorkers: a worker the pool is holding must not survive
// the pool.
func TestPoolCloseClosesIdleWorkers(t *testing.T) {
	p, _ := newPoolWithConns(t, Config{}, []*pb.ChildEvent{evtOK()})

	// A caller-supplied transport is never pooled, so the idle list is seeded
	// directly to exercise Pool.Close's own bookkeeping.
	idle := newFakeConn()
	p.mu.Lock()
	p.idle = append(p.idle, idle)
	p.mu.Unlock()

	require.NoError(t, p.Close())
	assert.True(t, idle.closed)
	assert.False(t, idle.Alive())
}

// TestPoolCloseRefusesFurtherCheckouts: a closed pool hands out no workers.
func TestPoolCloseRefusesFurtherCheckouts(t *testing.T) {
	p, conns := newPoolWithConns(t, Config{}, []*pb.ChildEvent{evtOK()})
	require.NoError(t, p.Close())

	s, err := p.Checkout(context.Background())
	require.Error(t, err, "Pool.Close documents that it refuses further checkouts")
	assert.Nil(t, s)
	assert.ErrorIs(t, err, ErrClosed)
	assert.Empty(t, *conns, "no worker is started for a closed pool")
}

func TestSessionCloseDiscardsSingleUseConn(t *testing.T) {
	p, conns := newPoolWithConns(t, Config{}, []*pb.ChildEvent{evtOK()})

	s, err := p.Checkout(context.Background())
	require.NoError(t, err)
	require.NoError(t, s.Close())

	assert.True(t, s.Closed())
	assert.True(t, (*conns)[0].closed, "a single-use worker is discarded, not pooled")
	assert.Len(t, (*conns)[0].requests(), 1, "no Reset is sent to a worker that is being discarded")
}

func TestSessionCloseIsIdempotent(t *testing.T) {
	p, conns := newPoolWithConns(t, Config{}, []*pb.ChildEvent{evtOK()})

	s, err := p.Checkout(context.Background())
	require.NoError(t, err)
	require.NoError(t, s.Close())
	require.NoError(t, s.Close())
	assert.True(t, (*conns)[0].closed)
}

// TestSessionCloseReleasesTheSlot: closing a session is what lets the pool
// hand the worker to the next caller, so the slot has to come back too.
func TestSessionCloseReleasesTheSlot(t *testing.T) {
	p, _ := newPoolWithConns(t, Config{MaxWorkers: 1},
		[]*pb.ChildEvent{evtOK()}, []*pb.ChildEvent{evtOK()})

	first, err := p.Checkout(context.Background())
	require.NoError(t, err)
	require.NoError(t, first.Close())

	// Not closed again: see the slot-accounting note in
	// TestPoolMaxWorkersSerialises.
	_, err = p.Checkout(context.Background())
	require.NoError(t, err, "the released slot is reusable")
}

// TestSessionCloseResetsAReusableWorker: a worker that goes back to the pool is
// reset first, so the next session does not inherit the previous one's globals.
func TestSessionCloseResetsAReusableWorker(t *testing.T) {
	p, conns := newPoolWithConns(t, Config{
		MaxWorkers:      1,
		CheckoutTimeout: 2 * time.Second,
	}, []*pb.ChildEvent{evtOK(), evtOK(), evtOK()}, []*pb.ChildEvent{evtOK()})
	p.reusable = true

	first, err := p.Checkout(context.Background())
	require.NoError(t, err)
	require.NoError(t, first.Close())

	require.Len(t, (*conns)[0].requests(), 2)
	assert.NotNil(t, (*conns)[0].requests()[1].GetReset_(),
		"a pooled worker is reset before reuse")

	second, err := p.Checkout(context.Background())
	require.NoError(t, err, "the reset worker is handed straight back out")
	defer second.Close()
	assert.Same(t, (*conns)[0], second.conn,
		"the pool reuses the connection it is holding, rather than opening a new one")
	assert.Len(t, *conns, 1, "no second worker is started")
}

// TestAcquireReplacesADeadIdleWorker: a worker that died while idle is discarded
// rather than handed to a session whose Configure would fail on a dead pipe.
func TestAcquireReplacesADeadIdleWorker(t *testing.T) {
	dead := newFakeConn()
	dead.alive = false
	fresh := newFakeConn(evtOK())

	p, err := NewPool(Config{
		NewConn: func(context.Context, *Config) (Conn, error) { return fresh, nil },
	})
	require.NoError(t, err)
	p.reusable = true
	p.idle = append(p.idle, dead)

	conn := acquireWithin(t, p)
	assert.Same(t, fresh, conn)
	assert.True(t, dead.closed, "the dead worker is shut down, not returned")
}

func TestAcquirePrefersAnIdleWorker(t *testing.T) {
	idle := newFakeConn()
	opened := 0
	p, err := NewPool(Config{
		NewConn: func(context.Context, *Config) (Conn, error) {
			opened++
			return newFakeConn(), nil
		},
	})
	require.NoError(t, err)
	p.reusable = true
	p.idle = append(p.idle, idle)

	conn := acquireWithin(t, p)
	assert.Same(t, idle, conn)
	assert.Zero(t, opened, "an idle worker is reused rather than starting a new one")
}

// acquireWithin calls acquire on another goroutine so a pool that never
// returns reports as a test failure instead of wedging the whole binary.
func acquireWithin(t *testing.T, p *Pool) Conn {
	t.Helper()
	type result struct {
		conn Conn
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		conn, err := p.acquire(context.Background())
		ch <- result{conn, err}
	}()
	select {
	case r := <-ch:
		require.NoError(t, r.err)
		return r.conn
	case <-time.After(5 * time.Second):
		t.Fatal("Pool.acquire did not return for a pool holding an idle worker")
		return nil
	}
}

func ptrU32(v uint32) *uint32 { return &v }
