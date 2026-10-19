// Package daemon is the service loop of `stvor-agent run` (spec §5.2): the
// first tick after U[0, 20 s), then "tick → sleep" with jitter and backoff,
// SIGHUP — tick now, SIGTERM/SIGINT — finish the tick within the stop budget
// and return, one operation at a time under the agent lock.
//
// The failure counter and the backoff live only here, derived from
// reconcile.Result.Outcome. Time, randomness, signals and the lock are
// injected; nothing is global.
package daemon

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"syscall"
	"time"

	"github.com/iamletenkov/stvor-agent/internal/reconcile"
)

// Ticker is what the loop needs from reconcile; *reconcile.Ticker implements it.
type Ticker interface {
	// Tick runs one tick; stop is closed when the service is asked to stop
	// (the tick then starts no set-managed, spec §5.8).
	Tick(ctx context.Context, manual bool, stop <-chan struct{}) reconcile.Result
	CheckActions(ctx context.Context) error
}

// Daemon is the service loop. Zero optional fields take real defaults.
type Daemon struct {
	Ticker Ticker
	// Lock is taken without waiting around every tick; busy — the tick is
	// skipped with one journal line (outcome skipped_locked).
	Lock Locker
	// Interval between ticks; zero — DefaultInterval.
	Interval time.Duration
	// After is the sleep; nil — time.After.
	After func(time.Duration) <-chan time.Time
	// Rand draws from [0, n) for the jitter; nil — math/rand/v2.
	Rand func(n int64) int64
	// Log gets the journal lines; nil — discarded.
	Log *slog.Logger
}

// Run checks the teleport actions once (softly: the Ticker decides what a
// failure means), then ticks until SIGTERM/SIGINT on sigs or ctx is done, and
// returns — the service exits 0. SIGHUP cuts a sleep short: tick now (the tick
// rereads the token itself); several SIGHUPs before the next tick are one.
//
// Signals are read all the time. A stop during a tick closes the tick's stop
// channel and is never a cancel of ctx: the tick cuts its portal poll, starts
// no set-managed, and a set-managed already running — the child `cream run`
// restarting the Teleport agent — is waited for (spec §5.8, TimeoutStopSec).
// A stop wins over pending SIGHUPs. The caller must not cancel ctx on SIGTERM
// (signal.Notify into sigs, not signal.NotifyContext).
func (d *Daemon) Run(ctx context.Context, sigs <-chan os.Signal) {
	log := d.log()
	interval := d.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	stop := make(chan struct{})
	hup := make(chan struct{}, 1)
	done := make(chan struct{})
	defer close(done)
	go pump(sigs, stop, hup, done)

	_ = d.Ticker.CheckActions(ctx) // soft check: the Ticker logs and acts on the error itself

	sleep := jitter(d.Rand)
	n := 0
	for {
		log.Debug("сон до следующего тика", "event", "sleep", "next_in", sleep)
		if !d.wait(ctx, stop, hup, sleep) {
			log.Info("остановка службы", "event", "stop")
			return
		}
		start := time.Now()
		res := d.tick(ctx, stop)
		var base time.Duration
		var withJitter bool
		n, base, withJitter = Next(interval, n, res.Outcome)
		sleep = base
		if withJitter {
			sleep += jitter(d.Rand)
		}
		log.Debug("тик завершён", "event", "tick", "outcome", res.Outcome, "failures", n,
			"took", time.Since(start))
	}
}

// pump turns signals into state: SIGTERM/SIGINT close stop (once), SIGHUP
// sets hup (one pending at most), until done.
func pump(sigs <-chan os.Signal, stop chan<- struct{}, hup chan<- struct{}, done <-chan struct{}) {
	stopped := false
	for {
		select {
		case <-done:
			return
		case s := <-sigs:
			switch {
			case s == syscall.SIGHUP:
				select {
				case hup <- struct{}{}:
				default: // one pending SIGHUP is enough
				}
			case (s == syscall.SIGTERM || s == os.Interrupt) && !stopped:
				stopped = true
				close(stop)
			}
		}
	}
}

// wait sleeps for dur; false — stop (SIGTERM/SIGINT or ctx done), true — tick
// (the timer fired or SIGHUP came). A stop is checked first and last, so it
// wins over a pending SIGHUP and a timer firing at the same moment.
func (d *Daemon) wait(ctx context.Context, stop <-chan struct{}, hup <-chan struct{}, dur time.Duration) bool {
	if isClosed(stop) || ctx.Err() != nil {
		return false
	}
	after := d.After
	if after == nil {
		after = time.After
	}
	select {
	case <-ctx.Done():
		return false
	case <-stop:
		return false
	case <-after(dur):
	case <-hup:
		d.log().Info("SIGHUP — тик сейчас", "event", "signal", "signal", "SIGHUP")
	}
	return !isClosed(stop) && ctx.Err() == nil
}

func isClosed(c <-chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}

// tick runs one service tick under the non-blocking lock.
func (d *Daemon) tick(ctx context.Context, stop <-chan struct{}) reconcile.Result {
	unlock, err := d.Lock.TryLock()
	if err != nil {
		if errors.Is(err, ErrLocked) {
			d.log().Info("тик пропущен: идёт другая операция агента", "event", "skip",
				"reason", "locked", "outcome", reconcile.OutcomeSkippedLocked)
		} else {
			d.log().Error("тик пропущен: блокировка агента не взята", "event", "skip",
				"reason", "lock_error", "outcome", reconcile.OutcomeSkippedLocked, "err", err)
		}
		return reconcile.Result{Outcome: reconcile.OutcomeSkippedLocked}
	}
	defer unlock()
	return d.Ticker.Tick(ctx, false, stop)
}

func (d *Daemon) log() *slog.Logger {
	if d.Log != nil {
		return d.Log
	}
	return slog.New(slog.DiscardHandler)
}

// Sync is one manual tick for the CLI (`sync`, the tail of `enable`): wait up
// to CLILockWait for the lock, tick with manual=true (no 3×/15 min pause),
// release. A lock that stays busy is ErrLockTimeout and no tick.
func Sync(ctx context.Context, l WaitLocker, t Ticker) (reconcile.Result, error) {
	unlock, err := l.Lock(ctx, CLILockWait)
	if err != nil {
		return reconcile.Result{}, err
	}
	defer unlock()
	return t.Tick(ctx, true, nil), nil
}
