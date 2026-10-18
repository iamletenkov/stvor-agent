package daemon

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/iamletenkov/stvor-agent/internal/reconcile"
)

const testJitter = 5 * time.Second

// fakeTicker scripts tick results and records calls.
type fakeTicker struct {
	mu       sync.Mutex
	results  []reconcile.Result // popped per tick; empty — OutcomeOK
	manual   []bool
	checks   int
	checkErr error
	// ticksAtCheck is how many ticks ran before CheckActions.
	ticksAtCheck int
	// started gets a value when a tick starts; release, when non-nil, is
	// waited on before the tick returns.
	started chan struct{}
	release chan struct{}
	// stops are the stop channels the ticks got.
	stops []<-chan struct{}
}

func (f *fakeTicker) Tick(_ context.Context, manual bool, stop <-chan struct{}) reconcile.Result {
	f.mu.Lock()
	f.manual = append(f.manual, manual)
	f.stops = append(f.stops, stop)
	res := reconcile.Result{Outcome: reconcile.OutcomeOK, HTTPStatus: 200}
	if len(f.results) > 0 {
		res, f.results = f.results[0], f.results[1:]
	}
	started, release := f.started, f.release
	f.mu.Unlock()
	if started != nil {
		started <- struct{}{}
	}
	if release != nil {
		<-release
	}
	return res
}

func (f *fakeTicker) CheckActions(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checks++
	f.ticksAtCheck = len(f.manual)
	return f.checkErr
}

func (f *fakeTicker) ticks() []bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]bool(nil), f.manual...)
}

// fakeLock is a Locker: busy — ErrLocked, otherwise counts lock/unlock.
type fakeLock struct {
	mu      sync.Mutex
	busy    []bool // popped per TryLock; empty — free
	err     error  // returned instead of ErrLocked when busy and set
	locks   int
	unlocks int
}

func (l *fakeLock) TryLock() (func(), error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	busy := false
	if len(l.busy) > 0 {
		busy, l.busy = l.busy[0], l.busy[1:]
	}
	if busy {
		if l.err != nil {
			return nil, l.err
		}
		return nil, ErrLocked
	}
	l.locks++
	return func() {
		l.mu.Lock()
		l.unlocks++
		l.mu.Unlock()
	}, nil
}

func (l *fakeLock) counts() (int, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.locks, l.unlocks
}

type sleepReq struct {
	d    time.Duration
	fire chan time.Time
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// harness runs Daemon.Run in a goroutine; every sleep the daemon asks for
// shows up on sleeps, and nothing fires until the test says so.
type harness struct {
	t      *testing.T
	ticker *fakeTicker
	lock   *fakeLock
	sigs   chan os.Signal
	sleeps chan sleepReq
	log    *syncBuffer
	done   chan struct{}
	cancel context.CancelFunc
}

func start(t *testing.T, ticker *fakeTicker, lock *fakeLock) *harness {
	t.Helper()
	h := &harness{
		t: t, ticker: ticker, lock: lock,
		sigs:   make(chan os.Signal, 4),
		sleeps: make(chan sleepReq, 16),
		log:    &syncBuffer{},
		done:   make(chan struct{}),
	}
	d := &Daemon{
		Ticker:   ticker,
		Lock:     lock,
		Interval: time.Minute,
		After: func(d time.Duration) <-chan time.Time {
			c := make(chan time.Time, 1)
			h.sleeps <- sleepReq{d: d, fire: c}
			return c
		},
		Rand: func(int64) int64 { return int64(testJitter) },
		Log:  slog.New(slog.NewTextHandler(h.log, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() {
		defer close(h.done)
		d.Run(ctx, h.sigs)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-h.done:
		case <-time.After(10 * time.Second):
			t.Error("Run did not return after the test")
		}
	})
	return h
}

// next returns the next sleep the daemon asked for.
func (h *harness) next() sleepReq {
	h.t.Helper()
	select {
	case r := <-h.sleeps:
		return r
	case <-time.After(10 * time.Second):
		h.t.Fatal("daemon asked for no sleep")
		return sleepReq{}
	}
}

// expectSleep takes the next sleep, checks its length and fires it.
func (h *harness) expectSleep(want time.Duration) {
	h.t.Helper()
	r := h.next()
	if r.d != want {
		h.t.Fatalf("sleep %v, want %v (log:\n%s)", r.d, want, h.log.String())
	}
	r.fire <- time.Time{}
}

func (h *harness) waitDone() {
	h.t.Helper()
	select {
	case <-h.done:
	case <-time.After(10 * time.Second):
		h.t.Fatal("Run did not return")
	}
}

func TestRunFirstTickAfterJitterThenInterval(t *testing.T) {
	t.Parallel()
	tk := &fakeTicker{}
	h := start(t, tk, &fakeLock{})

	r := h.next()
	if r.d != testJitter {
		t.Fatalf("first sleep %v, want jitter %v", r.d, testJitter)
	}
	if n := len(tk.ticks()); n != 0 {
		t.Fatalf("%d ticks before the first sleep", n)
	}
	r.fire <- time.Time{}
	h.expectSleep(time.Minute + testJitter)
	h.next() // third sleep: the second tick ran
	if got := tk.ticks(); len(got) != 2 || got[0] || got[1] {
		t.Fatalf("ticks = %v, want two service (non-manual) ticks", got)
	}
	h.sigs <- syscall.SIGTERM
	h.waitDone()
}

func TestRunChecksActionsOnceBeforeFirstTick(t *testing.T) {
	t.Parallel()
	for _, checkErr := range []error{nil, errors.New("cream actions: exit 1")} {
		tk := &fakeTicker{checkErr: checkErr}
		h := start(t, tk, &fakeLock{})
		h.expectSleep(testJitter)
		h.expectSleep(time.Minute + testJitter)
		h.next()
		tk.mu.Lock()
		checks, at := tk.checks, tk.ticksAtCheck
		tk.mu.Unlock()
		if checks != 1 || at != 0 {
			t.Fatalf("checkErr %v: CheckActions called %d times, after %d ticks; want once, before ticks", checkErr, checks, at)
		}
		if n := len(tk.ticks()); n != 2 {
			t.Fatalf("checkErr %v: %d ticks, want 2 (the check is soft)", checkErr, n)
		}
		h.sigs <- syscall.SIGTERM
		h.waitDone()
	}
}

func TestRunBackoff(t *testing.T) {
	t.Parallel()
	net := reconcile.Result{Outcome: reconcile.OutcomeNetwork}
	tests := []struct {
		name    string
		results []reconcile.Result
		lock    *fakeLock
		sleeps  []time.Duration // after the first (jitter) sleep
	}{
		{
			name:    "failures grow, success resets",
			results: []reconcile.Result{net, net, net, net, {Outcome: reconcile.OutcomeNotModified}, net},
			sleeps: []time.Duration{
				2*time.Minute + testJitter, 4*time.Minute + testJitter, 8*time.Minute + testJitter,
				8*time.Minute + testJitter, time.Minute + testJitter, 2*time.Minute + testJitter,
			},
		},
		{
			name:    "401 sleeps 600 s and keeps n",
			results: []reconcile.Result{net, {Outcome: reconcile.OutcomeUnauthorized, HTTPStatus: 401}, net},
			sleeps:  []time.Duration{2*time.Minute + testJitter, 10 * time.Minute, 4*time.Minute + testJitter},
		},
		{
			name:    "a busy lock skips the tick at the plain interval and keeps n",
			results: []reconcile.Result{net, net},
			lock:    &fakeLock{busy: []bool{false, true, false}},
			sleeps:  []time.Duration{2*time.Minute + testJitter, time.Minute + testJitter, 4*time.Minute + testJitter},
		},
		{
			name:    "disabled ticks at the plain interval",
			results: []reconcile.Result{{Outcome: reconcile.OutcomeDisabled}, {Outcome: reconcile.OutcomeDisabled}},
			sleeps:  []time.Duration{time.Minute + testJitter, time.Minute + testJitter},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			lock := tc.lock
			if lock == nil {
				lock = &fakeLock{}
			}
			h := start(t, &fakeTicker{results: tc.results}, lock)
			h.expectSleep(testJitter)
			for _, d := range tc.sleeps {
				h.expectSleep(d)
			}
			h.next()
			h.sigs <- syscall.SIGTERM
			h.waitDone()
		})
	}
}

func TestRunBusyLockSkipsTickWithOneLine(t *testing.T) {
	t.Parallel()
	tk := &fakeTicker{}
	lock := &fakeLock{busy: []bool{true}}
	h := start(t, tk, lock)
	h.expectSleep(testJitter)
	h.next()
	if n := len(tk.ticks()); n != 0 {
		t.Fatalf("%d ticks under a busy lock", n)
	}
	if got := strings.Count(h.log.String(), "event=skip"); got != 1 || !strings.Contains(h.log.String(), "outcome=skipped_locked") {
		t.Fatalf("skip lines = %d, want 1; log:\n%s", got, h.log.String())
	}
	h.sigs <- syscall.SIGTERM
	h.waitDone()
}

func TestRunLockErrorSkipsTick(t *testing.T) {
	t.Parallel()
	tk := &fakeTicker{}
	lock := &fakeLock{busy: []bool{true}, err: errors.New("open /run/stvor-agent.lock: read-only file system")}
	h := start(t, tk, lock)
	h.expectSleep(testJitter)
	h.next()
	if n := len(tk.ticks()); n != 0 {
		t.Fatalf("%d ticks without the lock", n)
	}
	if !strings.Contains(h.log.String(), "read-only file system") {
		t.Fatalf("lock error not logged:\n%s", h.log.String())
	}
	h.sigs <- syscall.SIGTERM
	h.waitDone()
}

func TestRunReleasesLockAfterEachTick(t *testing.T) {
	t.Parallel()
	lock := &fakeLock{}
	h := start(t, &fakeTicker{}, lock)
	h.expectSleep(testJitter)
	h.expectSleep(time.Minute + testJitter)
	h.next()
	if l, u := lock.counts(); l != 2 || u != 2 {
		t.Fatalf("locks %d, unlocks %d; want 2 and 2", l, u)
	}
	h.sigs <- syscall.SIGTERM
	h.waitDone()
}

func TestRunSIGHUPTicksNow(t *testing.T) {
	t.Parallel()
	tk := &fakeTicker{}
	h := start(t, tk, &fakeLock{})
	h.expectSleep(testJitter)
	r := h.next() // sleeping interval+jitter; the timer never fires
	if r.d != time.Minute+testJitter {
		t.Fatalf("sleep %v", r.d)
	}
	h.sigs <- syscall.SIGHUP
	r = h.next()
	if n := len(tk.ticks()); n != 2 {
		t.Fatalf("ticks after SIGHUP = %d, want 2", n)
	}
	if r.d != time.Minute+testJitter {
		t.Fatalf("sleep after the SIGHUP tick %v", r.d)
	}
	h.sigs <- syscall.SIGTERM
	h.waitDone()
	if n := len(tk.ticks()); n != 2 {
		t.Fatalf("ticks after SIGTERM = %d, want 2", n)
	}
}

func TestRunSIGHUPDuringFirstJitterTicksNow(t *testing.T) {
	t.Parallel()
	tk := &fakeTicker{}
	h := start(t, tk, &fakeLock{})
	h.next() // first jitter sleep, not fired
	h.sigs <- syscall.SIGHUP
	h.next()
	if n := len(tk.ticks()); n != 1 {
		t.Fatalf("ticks = %d, want 1", n)
	}
	h.sigs <- syscall.SIGTERM
	h.waitDone()
}

func TestRunStopSignalDuringSleepExits(t *testing.T) {
	t.Parallel()
	for _, sig := range []os.Signal{syscall.SIGTERM, os.Interrupt} {
		tk := &fakeTicker{}
		h := start(t, tk, &fakeLock{})
		h.next()
		h.sigs <- sig
		h.waitDone()
		if n := len(tk.ticks()); n != 0 {
			t.Fatalf("%v: %d ticks, want 0", sig, n)
		}
	}
}

func TestRunSIGTERMDuringTickWaitsForIt(t *testing.T) {
	t.Parallel()
	tk := &fakeTicker{started: make(chan struct{}, 1), release: make(chan struct{})}
	lock := &fakeLock{}
	h := start(t, tk, lock)
	h.expectSleep(testJitter)
	<-tk.started
	h.sigs <- syscall.SIGTERM
	select {
	case <-h.done:
		t.Fatal("Run returned while the tick was running")
	case <-time.After(50 * time.Millisecond):
	}
	tk.mu.Lock()
	stop := tk.stops[0]
	tk.mu.Unlock()
	select {
	case <-stop:
	case <-time.After(5 * time.Second):
		t.Fatal("SIGTERM during the tick did not close the tick's stop channel")
	}
	close(tk.release)
	h.waitDone()
	if l, u := lock.counts(); l != 1 || u != 1 {
		t.Fatalf("locks %d, unlocks %d after stop; want 1 and 1", l, u)
	}
	if n := len(tk.ticks()); n != 1 {
		t.Fatalf("ticks = %d, want 1", n)
	}
}

// waitDrained waits until the daemon has read every signal sent so far.
func (h *harness) waitDrained() {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(h.sigs) > 0 {
		if time.Now().After(deadline) {
			h.t.Fatal("signals not read")
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond) // the last one is handled, not only received
}

// A stop wins over SIGHUPs that came before it: no tick after the stop.
func TestRunStopWinsOverPendingSIGHUP(t *testing.T) {
	t.Parallel()
	tk := &fakeTicker{started: make(chan struct{}, 4), release: make(chan struct{})}
	h := start(t, tk, &fakeLock{})
	h.expectSleep(testJitter)
	<-tk.started
	h.sigs <- syscall.SIGHUP
	h.sigs <- syscall.SIGHUP
	h.sigs <- syscall.SIGTERM
	h.waitDrained()
	close(tk.release)
	h.waitDone()
	if n := len(tk.ticks()); n != 1 {
		t.Fatalf("ticks = %d, want 1: SIGHUP before SIGTERM must not tick again", n)
	}
}

// SIGHUPs during a tick are one: one more tick, then the plain sleep.
func TestRunSIGHUPsDuringTickAreOne(t *testing.T) {
	t.Parallel()
	tk := &fakeTicker{started: make(chan struct{}, 8), release: make(chan struct{})}
	h := start(t, tk, &fakeLock{})
	h.expectSleep(testJitter)
	<-tk.started
	for range 3 {
		h.sigs <- syscall.SIGHUP
	}
	h.waitDrained()
	close(tk.release)
	<-tk.started // the SIGHUP tick
	// Each wait asks for a sleep, the one cut by the SIGHUP included: two
	// sleeps after the first tick, then nothing — no third tick.
	h.next()
	h.next()
	select {
	case r := <-h.sleeps:
		t.Fatalf("another sleep %v: SIGHUPs were not coalesced", r.d)
	case <-time.After(100 * time.Millisecond):
	}
	if n := len(tk.ticks()); n != 2 {
		t.Fatalf("ticks = %d, want 2", n)
	}
	h.sigs <- syscall.SIGTERM
	h.waitDone()
}

func TestRunContextCancelExits(t *testing.T) {
	t.Parallel()
	h := start(t, &fakeTicker{}, &fakeLock{})
	h.next()
	h.cancel()
	h.waitDone()
}

// fakeWaitLock is a WaitLocker for Sync.
type fakeWaitLock struct {
	err     error
	wait    time.Duration
	unlocks int
}

func (l *fakeWaitLock) Lock(_ context.Context, wait time.Duration) (func(), error) {
	l.wait = wait
	if l.err != nil {
		return nil, l.err
	}
	return func() { l.unlocks++ }, nil
}

func TestSync(t *testing.T) {
	t.Parallel()
	tk := &fakeTicker{results: []reconcile.Result{{Outcome: reconcile.OutcomeNotModified, HTTPStatus: 304}}}
	lock := &fakeWaitLock{}
	res, err := Sync(context.Background(), lock, tk)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if res.Outcome != reconcile.OutcomeNotModified {
		t.Fatalf("result %+v", res)
	}
	if got := tk.ticks(); len(got) != 1 || !got[0] {
		t.Fatalf("ticks = %v, want one manual tick", got)
	}
	if lock.wait != CLILockWait || lock.unlocks != 1 {
		t.Fatalf("lock wait %v, unlocks %d; want %v and 1", lock.wait, lock.unlocks, CLILockWait)
	}
}

func TestSyncLockTimeout(t *testing.T) {
	t.Parallel()
	tk := &fakeTicker{}
	lock := &fakeWaitLock{err: ErrLockTimeout}
	if _, err := Sync(context.Background(), lock, tk); !errors.Is(err, ErrLockTimeout) {
		t.Fatalf("err = %v, want ErrLockTimeout", err)
	}
	if n := len(tk.ticks()); n != 0 {
		t.Fatalf("%d ticks without the lock", n)
	}
}
