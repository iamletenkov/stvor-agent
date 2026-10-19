package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

// Lock of "one operation at a time" (spec §5.2): the service tick, sync,
// apply, enable, disable.
const (
	DefaultLockPath = "/run/stvor-agent.lock"
	// CLILockWait is how long a CLI command waits for the lock.
	CLILockWait = 120 * time.Second
	// lockPoll is the retry step of a waiting Lock.
	lockPoll = 250 * time.Millisecond
)

// Errors of the lock. Match with errors.Is.
var (
	ErrLocked      = errors.New("блокировка агента занята другой операцией")
	ErrLockTimeout = errors.New("блокировка агента не освободилась за отведённое время")
)

// Locker is the non-blocking lock the service takes around a tick.
type Locker interface {
	// TryLock takes the lock or returns ErrLocked (held by another operation)
	// or another error (the lock file cannot be opened).
	TryLock() (unlock func(), err error)
}

// WaitLocker is the waiting lock CLI commands take.
type WaitLocker interface {
	// Lock waits up to wait for the lock; ErrLockTimeout when it stays busy.
	Lock(ctx context.Context, wait time.Duration) (unlock func(), err error)
}

// FileLock is flock(2) on Path. The descriptor is close-on-exec, so a child
// `cream run` never inherits the lock. Locks of two FileLock values on one
// path exclude each other in one process too (separate open files).
type FileLock struct {
	Path string
	// Now and After are the clock of a waiting Lock; nil — time.Now and
	// time.After.
	Now   func() time.Time
	After func(time.Duration) <-chan time.Time
}

// TryLock implements Locker.
func (l FileLock) TryLock() (func(), error) {
	f, err := os.OpenFile(l.Path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("файл блокировки %s: %w", l.Path, err)
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		f.Close() //nolint:errcheck // nothing was written
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("flock %s: %w", l.Path, err)
	}
	// Closing the descriptor releases the lock.
	return func() { f.Close() }, nil //nolint:errcheck // closing releases the flock; nothing was written
}

// Lock implements WaitLocker: retries TryLock every lockPoll until wait runs out.
func (l FileLock) Lock(ctx context.Context, wait time.Duration) (func(), error) {
	now, after := l.Now, l.After
	if now == nil {
		now = time.Now
	}
	if after == nil {
		after = time.After
	}
	deadline := now().Add(wait)
	for {
		unlock, err := l.TryLock()
		if !errors.Is(err, ErrLocked) {
			return unlock, err
		}
		left := deadline.Sub(now())
		if left <= 0 {
			return nil, fmt.Errorf("%w (%s, %v)", ErrLockTimeout, l.Path, wait)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-after(min(lockPoll, left)):
		}
	}
}
