package supervisor

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// chanWriter forwards each status write to a channel so a test can wait for a
// specific transition before acting.
type chanWriter struct{ lines chan string }

func (c *chanWriter) Write(p []byte) (int, error) {
	c.lines <- string(p)
	return len(p), nil
}

func waitForLine(t *testing.T, lines <-chan string, substr string) {
	t.Helper()
	for {
		select {
		case line := <-lines:
			if strings.Contains(line, substr) {
				return
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for a status line containing %q", substr)
		}
	}
}

// fakeClock is a deterministic Clock: Now returns a fixed (advanceable) time and
// every After call registers a fresh delivery channel, routed by duration into
// one of two FIFO queues — the proactive tick (defaultTickInterval) and the
// rebuild backoff (anything else). fireTick / fireBackoff each pull the next
// waiter of that kind (blocking until one exists, so they never fire ahead of
// the loop reaching its select) and deliver to it. No real-time sleep, and the
// two kinds never contend, so the time-based tests stay unambiguous.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	ticks   chan chan time.Time
	backoff chan chan time.Time
	afterN  int
}

func newFakeClock(now time.Time) *fakeClock {
	return &fakeClock{
		now:     now,
		ticks:   make(chan chan time.Time, 16),
		backoff: make(chan chan time.Time, 16),
	}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	c.afterN++
	c.mu.Unlock()
	ch := make(chan time.Time, 1)
	if d == defaultTickInterval {
		c.ticks <- ch
	} else {
		c.backoff <- ch
	}
	return ch
}

func (c *fakeClock) deliver(q chan chan time.Time) {
	ch := <-q
	c.mu.Lock()
	now := c.now
	c.mu.Unlock()
	ch <- now
}

// fireTick wakes the next pending proactive-tick waiter.
func (c *fakeClock) fireTick() { c.deliver(c.ticks) }

// fireBackoff wakes the next pending rebuild-backoff waiter.
func (c *fakeClock) fireBackoff() { c.deliver(c.backoff) }

func (c *fakeClock) set(now time.Time) {
	c.mu.Lock()
	c.now = now
	c.mu.Unlock()
}

// fakeSession is a bastion session whose deadline the test controls; Alive is no
// longer consulted by the unified rebuild loop but kept so the interface is
// satisfied and any leftover caller sees a healthy session.
type fakeSession struct {
	id       string
	deadline time.Time
	closed   int
}

func (s *fakeSession) Alive(context.Context) bool  { return true }
func (s *fakeSession) Deadline() time.Time         { return s.deadline }
func (s *fakeSession) Close(context.Context) error { s.closed++; return nil }

// fakeTunnel breaks breakN times (Wait returns nil), then blocks until ctx is
// cancelled, so a test can make a tunnel "break" a controlled number of times.
type fakeTunnel struct {
	port   int
	breakN int
	waitN  int
	closed int
	// blocked, if set, is closed when Wait enters its block-until-cancel phase,
	// letting a test wait until the tunnel is steady before acting.
	blocked chan struct{}
}

func (t *fakeTunnel) Wait(ctx context.Context) error {
	c := t.waitN
	t.waitN++
	if c < t.breakN {
		return nil // a break
	}
	if t.blocked != nil {
		close(t.blocked)
	}
	<-ctx.Done()
	return ctx.Err()
}

func (t *fakeTunnel) Close() error   { t.closed++; return nil }
func (t *fakeTunnel) LocalPort() int { return t.port }

// fakeBuilder hands out scripted sessions and tunnels and records how it was
// driven (call counts, the localPort passed to each OpenTunnel).
type fakeBuilder struct {
	sessions   []*fakeSession
	newSessN   int
	newSessErr error

	tunnels  []*fakeTunnel
	openN    int   // index of the next tunnel to hand out (advances only on success)
	openCall int   // total OpenTunnel calls (advances on failure too)
	openErr  error // if set, every OpenTunnel fails with it (a non-retryable build)
	// openFails scripts per-call OpenTunnel failures: openFails[i]==true makes the
	// i-th call fail (a transient open error) without consuming a tunnel slot, so
	// a rebuild can be made to fail N times then succeed.
	openFails []bool
	openPorts []int
}

func (b *fakeBuilder) NewSession(context.Context) (Session, error) {
	if b.newSessErr != nil {
		return nil, b.newSessErr
	}
	s := b.sessions[b.newSessN]
	b.newSessN++
	return s, nil
}

func (b *fakeBuilder) OpenTunnel(_ context.Context, _ Session, localPort int) (Tunnel, error) {
	b.openPorts = append(b.openPorts, localPort)
	call := b.openCall
	b.openCall++
	if b.openErr != nil {
		return nil, b.openErr
	}
	if call < len(b.openFails) && b.openFails[call] {
		return nil, errors.New("transient open failure")
	}
	t := b.tunnels[b.openN]
	b.openN++
	return t, nil
}

// fakeWiring records kubeconfig wire/unwire calls.
type fakeWiring struct {
	wired    int
	wirePort int
	unwired  int
	wireErr  error
}

func (w *fakeWiring) Wire(localPort int) error {
	w.wired++
	w.wirePort = localPort
	if w.wireErr != nil {
		return w.wireErr
	}
	return nil
}

func (w *fakeWiring) Unwire() error { w.unwired++; return nil }

// recordingObserver captures every Report the supervisor emits so a test can
// assert restart-count, last-error, and deadline reach the daemon state path.
type recordingObserver struct {
	mu      sync.Mutex
	reports []Report
}

func (o *recordingObserver) observe(r Report) {
	o.mu.Lock()
	o.reports = append(o.reports, r)
	o.mu.Unlock()
}

func (o *recordingObserver) last() (Report, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.reports) == 0 {
		return Report{}, false
	}
	return o.reports[len(o.reports)-1], true
}

func (o *recordingObserver) maxRestart() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	m := 0
	for _, r := range o.reports {
		if r.RestartCount > m {
			m = r.RestartCount
		}
	}
	return m
}

// TestRun_CancelTearsDownOnce is the tracer bullet: a healthy tunnel held until
// the context is cancelled returns cleanly and tears everything down once.
func TestRun_CancelTearsDownOnce(t *testing.T) {
	sess := &fakeSession{id: "s1", deadline: time.Now().Add(3 * time.Hour)}
	tun := &fakeTunnel{port: 18443, blocked: make(chan struct{})}
	b := &fakeBuilder{sessions: []*fakeSession{sess}, tunnels: []*fakeTunnel{tun}}
	w := &fakeWiring{}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, b, w, &bytes.Buffer{}, WithBackoff(0, 0)) }()

	<-tun.blocked // wait until the tunnel is up and steady, then cancel
	cancel()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want nil or context.Canceled", err)
	}

	if w.unwired != 1 {
		t.Errorf("Unwire called %d times, want exactly 1", w.unwired)
	}
	if tun.closed != 1 {
		t.Errorf("tunnel Close called %d times, want exactly 1", tun.closed)
	}
	if sess.closed != 1 {
		t.Errorf("session Close called %d times, want exactly 1", sess.closed)
	}
}

// A break (forward dropped) triggers one unified rebuild: a NEW session AND a
// NEW tunnel on the same local port, with the -bastion context wired exactly
// once (the stable port survives the rebuild, so it is never re-wired).
func TestRun_BreakRebuildsSessionAndTunnel(t *testing.T) {
	first := &fakeSession{id: "s1", deadline: time.Now().Add(3 * time.Hour)}
	second := &fakeSession{id: "s2", deadline: time.Now().Add(3 * time.Hour)}
	broken := &fakeTunnel{port: 18443, breakN: 1}
	rebuilt := &fakeTunnel{port: 18443, blocked: make(chan struct{})}
	b := &fakeBuilder{
		sessions: []*fakeSession{first, second},
		tunnels:  []*fakeTunnel{broken, rebuilt},
	}
	w := &fakeWiring{}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, b, w, &bytes.Buffer{}, WithBackoff(0, 0)) }()
	<-rebuilt.blocked // wait until the rebuild is up and steady
	cancel()
	<-done

	if b.newSessN != 2 {
		t.Errorf("NewSession called %d times, want 2 (break forces a new session)", b.newSessN)
	}
	if b.openN != 2 {
		t.Errorf("OpenTunnel called %d times, want 2 (initial + rebuild)", b.openN)
	}
	if len(b.openPorts) == 2 && b.openPorts[1] != 18443 {
		t.Errorf("rebuild used local port %d, want the stable port 18443", b.openPorts[1])
	}
	if first.closed != 1 {
		t.Errorf("old session Close called %d times, want 1 (deleted as part of the rebuild)", first.closed)
	}
	if w.wired != 1 {
		t.Errorf("Wire called %d times, want 1 (the context is wired once and survives a rebuild)", w.wired)
	}
}

// Crossing the proactive margin must trigger a rebuild BEFORE the TTL deadline:
// a tick whose remaining time is below the margin rebuilds; a tick with the
// deadline far out does not.
func TestRun_ProactiveRebuildNearExpiry(t *testing.T) {
	now := time.Date(2026, 6, 4, 9, 0, 0, 0, time.UTC)
	clk := newFakeClock(now)
	// First session expires just inside the margin; the rebuild gets a fresh
	// far-out deadline so the second tick does NOT rebuild again.
	near := &fakeSession{id: "near", deadline: now.Add(defaultRebuildMargin - time.Minute)}
	fresh := &fakeSession{id: "fresh", deadline: now.Add(3 * time.Hour)}
	held := &fakeTunnel{port: 18443} // never breaks; held until ctx done
	rebuilt := &fakeTunnel{port: 18443, blocked: make(chan struct{})}
	b := &fakeBuilder{
		sessions: []*fakeSession{near, fresh},
		tunnels:  []*fakeTunnel{held, rebuilt},
	}
	w := &fakeWiring{}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, b, w, &bytes.Buffer{}, WithBackoff(0, 0), WithClock(clk)) }()

	clk.fireTick()    // the ~30s tick: remaining < margin → rebuild
	<-rebuilt.blocked // rebuild is up and steady

	if b.newSessN != 2 {
		t.Fatalf("NewSession called %d times, want 2 (proactive rebuild near expiry)", b.newSessN)
	}
	if near.closed != 1 {
		t.Errorf("near-expiry session Close called %d times, want 1 (rebuilt away)", near.closed)
	}

	// A second tick with the fresh far-out deadline must NOT rebuild. fireTick
	// blocks until the loop has re-armed the tick after the rebuild and delivers
	// to it; the loop sees remaining > margin and keeps holding. A third fireTick
	// blocks until the handler re-armed again — proving the handler ran and chose
	// not to rebuild — before we cancel.
	clk.fireTick()
	clk.fireTick()

	cancel()
	<-done
	if b.newSessN != 2 {
		t.Errorf("NewSession called %d times, want 2 (a far-out deadline tick must not rebuild)", b.newSessN)
	}
	if w.wired != 1 {
		t.Errorf("Wire called %d times, want 1 (context wired once across the rebuild)", w.wired)
	}
}

// A failing rebuild must retry with the existing capped/growing backoff routed
// through the clock — never hot-looping — and the error must be observable via
// the Report so status can surface last-error.
func TestRun_RebuildFailureBacksOffAndIsObservable(t *testing.T) {
	now := time.Date(2026, 6, 4, 9, 0, 0, 0, time.UTC)
	clk := newFakeClock(now)
	first := &fakeSession{id: "s1", deadline: now.Add(3 * time.Hour)}
	// after the break, the rebuild's NewSession succeeds but OpenTunnel fails
	// twice, then succeeds — exercising two backoff waits with growing duration.
	second := &fakeSession{id: "s2", deadline: now.Add(3 * time.Hour)}
	third := &fakeSession{id: "s3", deadline: now.Add(3 * time.Hour)}
	fourth := &fakeSession{id: "s4", deadline: now.Add(3 * time.Hour)}
	broken := &fakeTunnel{port: 18443, breakN: 1}
	rebuilt := &fakeTunnel{port: 18443, blocked: make(chan struct{})}
	b := &fakeBuilder{
		sessions: []*fakeSession{first, second, third, fourth},
		tunnels:  []*fakeTunnel{broken, rebuilt},
		// call 0 (initial bring-up) succeeds → broken; calls 1 and 2 (the first
		// two rebuild opens) fail; call 3 succeeds → rebuilt.
		openFails: []bool{false, true, true, false},
	}
	w := &fakeWiring{}
	obs := &recordingObserver{}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, b, w, &bytes.Buffer{},
			WithBackoff(time.Second, 30*time.Second), WithClock(clk), WithObserver(obs.observe))
	}()

	// The initial tunnel (broken) breaks immediately → rebuild attempt 1 fails →
	// backoff wait. Drive two backoff waits, then the third OpenTunnel succeeds.
	waitForObserverError(t, obs)
	clk.fireBackoff() // release first backoff → second attempt (also fails) → second backoff
	waitForRestartCount(t, obs, 2)
	clk.fireBackoff() // release second backoff → third attempt succeeds
	<-rebuilt.blocked

	cancel()
	<-done

	sawErr := false
	obs.mu.Lock()
	for _, r := range obs.reports {
		if r.LastErr != "" {
			sawErr = true
		}
	}
	obs.mu.Unlock()
	if !sawErr {
		t.Error("expected at least one Report carrying the rebuild error (last-error)")
	}
	if obs.maxRestart() < 2 {
		t.Errorf("max restart count observed = %d, want >= 2 (each failed rebuild increments it)", obs.maxRestart())
	}
	// Two failed attempts → at least two backoff After calls. With no hot-loop,
	// the loop never spins without a clock wait between attempts.
	if clk.afterN < 2 {
		t.Errorf("clock After called %d times, want >= 2 (each rebuild failure waits the backoff)", clk.afterN)
	}
	// No-leak invariant: every session created along the way is deleted exactly
	// once — including the intermediate sessions made during the FAILING retries
	// (second, third), which a careless rebuild could orphan. first is the
	// initial session (replaced on the break), second/third are the failed-
	// rebuild sessions (each replaced on the next retry), fourth is the live one
	// (deleted at teardown). All must read closed == 1.
	for name, s := range map[string]*fakeSession{
		"initial":           first,
		"failed-rebuild #1": second,
		"failed-rebuild #2": third,
		"live":              fourth,
	} {
		if s.closed != 1 {
			t.Errorf("%s session Close called %d times, want exactly 1 (no leak, no double-delete)", name, s.closed)
		}
	}
}

// A non-retryable build failure on the FIRST bring-up (never wired) still stops
// the loop with the error rather than spinning.
func TestRun_InitialTunnelErrorStops(t *testing.T) {
	blocked := errors.New("dial bastion: connection refused (NSG?)")
	sess := &fakeSession{id: "s1", deadline: time.Now().Add(3 * time.Hour)}
	b := &fakeBuilder{sessions: []*fakeSession{sess}, openErr: blocked}
	w := &fakeWiring{}

	err := Run(context.Background(), b, w, &bytes.Buffer{}, WithBackoff(0, 0))
	if !errors.Is(err, blocked) {
		t.Fatalf("Run returned %v, want the build error", err)
	}
	if len(b.openPorts) != 1 {
		t.Errorf("OpenTunnel called %d times, want 1 (no retry spin on the first bring-up)", len(b.openPorts))
	}
	if w.unwired != 0 {
		t.Errorf("Unwire called %d times, want 0 (nothing was wired)", w.unwired)
	}
	if sess.closed != 1 {
		t.Errorf("session Close called %d times, want 1 (the created session is cleaned up)", sess.closed)
	}
}

func TestRun_InitialSessionErrorStops(t *testing.T) {
	down := errors.New("create session: service unavailable")
	b := &fakeBuilder{newSessErr: down}
	w := &fakeWiring{}

	err := Run(context.Background(), b, w, &bytes.Buffer{}, WithBackoff(0, 0))
	if !errors.Is(err, down) {
		t.Fatalf("Run returned %v, want the session error", err)
	}
	if len(b.openPorts) != 0 {
		t.Errorf("OpenTunnel called %d times, want 0 (session never came up)", len(b.openPorts))
	}
}

// The observer must see the active local port and the session deadline so the
// daemon state can render time-remaining + local port.
func TestRun_ObserverReportsPortAndDeadline(t *testing.T) {
	deadline := time.Now().Add(3 * time.Hour)
	sess := &fakeSession{id: "s1", deadline: deadline}
	tun := &fakeTunnel{port: 18443, blocked: make(chan struct{})}
	b := &fakeBuilder{sessions: []*fakeSession{sess}, tunnels: []*fakeTunnel{tun}}
	w := &fakeWiring{}
	obs := &recordingObserver{}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, b, w, &bytes.Buffer{}, WithBackoff(0, 0), WithObserver(obs.observe)) }()
	<-tun.blocked
	cancel()
	<-done

	last, ok := obs.last()
	if !ok {
		t.Fatal("observer received no reports")
	}
	if last.LocalPort != 18443 {
		t.Errorf("Report.LocalPort = %d, want 18443", last.LocalPort)
	}
	if !last.Deadline.Equal(deadline) {
		t.Errorf("Report.Deadline = %v, want %v", last.Deadline, deadline)
	}
}

func waitForObserverError(t *testing.T, obs *recordingObserver) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		obs.mu.Lock()
		for _, r := range obs.reports {
			if r.LastErr != "" {
				obs.mu.Unlock()
				return
			}
		}
		obs.mu.Unlock()
		select {
		case <-deadline:
			t.Fatal("timed out waiting for an error Report")
		default:
		}
	}
}

func waitForRestartCount(t *testing.T, obs *recordingObserver, n int) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		if obs.maxRestart() >= n {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for restart count %d (saw %d)", n, obs.maxRestart())
		default:
		}
	}
}
