// Package daemon is the service loop of `stvor-agent run` (spec §5.2): the
// first tick after U[0, 20 s), then "tick → sleep" with jitter and backoff,
// SIGHUP — tick now, SIGTERM/SIGINT — finish the tick and return, one
// operation at a time under the agent lock.
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
	Tick(ctx context.Context, manual bool) reconcile.Result
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
// rereads the token itself).
//
// A tick is never interrupted: signals are only read between ticks, so a
// stop waits for the running tick and its child `cream run` (spec §5.8). The
// caller must not cancel ctx on SIGTERM (signal.Notify into sigs, not
// signal.NotifyContext), or the tick's commands would be cut.
func (d *Daemon) Run(ctx context.Context, sigs <-chan os.Signal) {
	log := d.log()
	interval := d.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	_ = d.Ticker.CheckActions(ctx) // soft check: the Ticker logs and acts on the error itself

	sleep := jitter(d.Rand)
	n := 0
	for {
		log.Debug("сон до следующего тика", "event", "sleep", "next_in", sleep)
		if !d.wait(ctx, sigs, sleep) {
			log.Info("остановка службы", "event", "stop")
			return
		}
		start := time.Now()
		res := d.tick(ctx)
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

// wait sleeps for d; false — stop (SIGTERM/SIGINT or ctx done), true — tick
// (the timer fired or SIGHUP came).
func (d *Daemon) wait(ctx context.Context, sigs <-chan os.Signal, dur time.Duration) bool {
	after := d.After
	if after == nil {
		after = time.After
	}
	timer := after(dur)
	for {
		select {
		case <-ctx.Done():
			return false
		case <-timer:
			return true
		case s := <-sigs:
			if s == syscall.SIGHUP {
				d.log().Info("SIGHUP — тик сейчас", "event", "signal", "signal", "SIGHUP")
				return true
			}
			if s == syscall.SIGTERM || s == os.Interrupt {
				return false
			}
		}
	}
}

// tick runs one service tick under the non-blocking lock.
func (d *Daemon) tick(ctx context.Context) reconcile.Result {
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
	return d.Ticker.Tick(ctx, false)
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
	return t.Tick(ctx, true), nil
}
