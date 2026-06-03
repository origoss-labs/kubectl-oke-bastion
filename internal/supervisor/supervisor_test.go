package supervisor

import (
	"bytes"
	"context"
	"errors"
	"strings"
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

// fakeSession is a bastion session whose validity is scripted: alive(callIndex)
// decides each Alive result, so a test can make a session "expire".
type fakeSession struct {
	id     string
	alive  func(call int) bool
	aliveN int
	closed int
}

func (s *fakeSession) Alive(context.Context) bool {
	c := s.aliveN
	s.aliveN++
	if s.alive == nil {
		return true
	}
	return s.alive(c)
}

func (s *fakeSession) Close(context.Context) error {
	s.closed++
	return nil
}

// fakeTunnel breaks breakN times (Wait returns nil), then blocks until ctx is
// cancelled, so a test can make a tunnel "break" a controlled number of times.
type fakeTunnel struct {
	port   int
	breakN int
	waitN  int
	closed int
	// blocked, if set, is closed when Wait enters its block-until-cancel phase,
	// letting a test wait until the tunnel is steady before cancelling.
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

func (t *fakeTunnel) Close() error {
	t.closed++
	return nil
}

func (t *fakeTunnel) LocalPort() int { return t.port }

// fakeBuilder hands out scripted sessions and tunnels and records how it was
// driven (call counts, the localPort passed to each OpenTunnel).
type fakeBuilder struct {
	sessions   []*fakeSession
	newSessN   int
	newSessErr error

	tunnels   []*fakeTunnel
	openN     int
	openErr   error
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
	if b.openErr != nil {
		return nil, b.openErr
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

func (w *fakeWiring) Unwire() error {
	w.unwired++
	return nil
}

// TestRun_CancelTearsDownOnce is the tracer bullet: a healthy tunnel held until
// the context is cancelled returns cleanly and tears everything down once.
func TestRun_CancelTearsDownOnce(t *testing.T) {
	sess := &fakeSession{id: "s1"}
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

// A break with the session still valid must be recovered by redialing the
// tunnel on the same local port — no new session.
func TestRun_BreakRedialsWithoutNewSession(t *testing.T) {
	sess := &fakeSession{id: "s1"} // alive == true always
	broken := &fakeTunnel{port: 18443, breakN: 1}
	redialed := &fakeTunnel{port: 18443, blocked: make(chan struct{})}
	b := &fakeBuilder{
		sessions: []*fakeSession{sess},
		tunnels:  []*fakeTunnel{broken, redialed},
	}
	w := &fakeWiring{}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, b, w, &bytes.Buffer{}, WithBackoff(0, 0)) }()
	<-redialed.blocked // wait until the redial is up and steady
	cancel()
	<-done

	if b.newSessN != 1 {
		t.Errorf("NewSession called %d times, want 1 (a break must not create a session)", b.newSessN)
	}
	if b.openN != 2 {
		t.Errorf("OpenTunnel called %d times, want 2 (initial + redial)", b.openN)
	}
	if len(b.openPorts) == 2 && b.openPorts[1] != 18443 {
		t.Errorf("redial used local port %d, want the stable port 18443", b.openPorts[1])
	}
	if w.wired != 1 {
		t.Errorf("Wire called %d times, want 1 (the context is wired once and survives a redial)", w.wired)
	}
}

// A break with a dead/expired session must be recovered by creating a NEW
// session and then a new tunnel.
func TestRun_ExpiryRecreatesSession(t *testing.T) {
	expired := &fakeSession{id: "old", alive: func(int) bool { return false }}
	fresh := &fakeSession{id: "new"}
	broken := &fakeTunnel{port: 18443, breakN: 1}
	recreated := &fakeTunnel{port: 18443, blocked: make(chan struct{})}
	b := &fakeBuilder{
		sessions: []*fakeSession{expired, fresh},
		tunnels:  []*fakeTunnel{broken, recreated},
	}
	w := &fakeWiring{}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, b, w, &bytes.Buffer{}, WithBackoff(0, 0)) }()
	<-recreated.blocked
	cancel()
	<-done

	if b.newSessN != 2 {
		t.Errorf("NewSession called %d times, want 2 (initial + recreate on expiry)", b.newSessN)
	}
	if expired.closed != 1 {
		t.Errorf("expired session Close called %d times, want 1 (deleted on expiry)", expired.closed)
	}
	if fresh.closed != 1 {
		t.Errorf("fresh session Close called %d times, want 1 (deleted at teardown)", fresh.closed)
	}
	if b.openN != 2 {
		t.Errorf("OpenTunnel called %d times, want 2", b.openN)
	}
}

// A build failure the tunnel's own dial-retry could not absorb (e.g. an NSG
// blocks the path) must stop the loop with the error, not spin forever.
func TestRun_NonRetryableTunnelErrorStops(t *testing.T) {
	blocked := errors.New("dial bastion: connection refused (NSG?)")
	sess := &fakeSession{id: "s1"}
	b := &fakeBuilder{sessions: []*fakeSession{sess}, openErr: blocked}
	w := &fakeWiring{}

	err := Run(context.Background(), b, w, &bytes.Buffer{}, WithBackoff(0, 0))
	if !errors.Is(err, blocked) {
		t.Fatalf("Run returned %v, want the build error", err)
	}
	if len(b.openPorts) != 1 {
		t.Errorf("OpenTunnel called %d times, want 1 (no retry spin)", len(b.openPorts))
	}
	// The context was never wired, so teardown must not try to unwire it.
	if w.unwired != 0 {
		t.Errorf("Unwire called %d times, want 0 (nothing was wired)", w.unwired)
	}
	if sess.closed != 1 {
		t.Errorf("session Close called %d times, want 1 (the created session is cleaned up)", sess.closed)
	}
}

func TestRun_NonRetryableSessionErrorStops(t *testing.T) {
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

// Status output must reflect the state transitions the operator sees: initial
// connect, active, a reconnect on break, and a recreate on expiry.
func TestRun_StatusReflectsTransitions(t *testing.T) {
	expired := &fakeSession{id: "old", alive: func(int) bool { return false }}
	fresh := &fakeSession{id: "new"}
	broken := &fakeTunnel{port: 18443, breakN: 1}
	recreated := &fakeTunnel{port: 18443, blocked: make(chan struct{})}
	b := &fakeBuilder{
		sessions: []*fakeSession{expired, fresh},
		tunnels:  []*fakeTunnel{broken, recreated},
	}
	w := &fakeWiring{}
	var out bytes.Buffer

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, b, w, &out, WithBackoff(0, 0)) }()
	<-recreated.blocked
	cancel()
	<-done

	for _, state := range []string{"connecting", "active", "reconnecting", "recreating"} {
		if !strings.Contains(out.String(), "["+state+"]") {
			t.Errorf("status output missing %q state:\n%s", state, out.String())
		}
	}
}

// After a break the supervisor must wait out a backoff before rebuilding, and
// a cancel during that wait must abort cleanly rather than open a new tunnel —
// the guard against hot-spinning on an endpoint that breaks the instant it's up.
func TestRun_BackoffGatesRebuildAndIsCancelable(t *testing.T) {
	sess := &fakeSession{id: "s1"}
	broken := &fakeTunnel{port: 18443, breakN: 1}
	never := &fakeTunnel{port: 18443} // a second open would consume this
	b := &fakeBuilder{
		sessions: []*fakeSession{sess},
		tunnels:  []*fakeTunnel{broken, never},
	}
	w := &fakeWiring{}
	out := &chanWriter{lines: make(chan string, 16)}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	// A long backoff: once the tunnel breaks the loop parks in the backoff wait,
	// where the cancel below must take effect before any rebuild.
	go func() { done <- Run(ctx, b, w, out, WithBackoff(time.Hour, time.Hour)) }()

	waitForLine(t, out.lines, "[reconnecting]")
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want context.Canceled", err)
	}
	if len(b.openPorts) != 1 {
		t.Errorf("OpenTunnel called %d times, want 1 (no rebuild during a cancelled backoff — no spin)", len(b.openPorts))
	}
}
