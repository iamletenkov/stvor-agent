package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// stepClock is a clock whose After advances the time by d and fires at
// once: lock waits run without real sleeping. onSleep runs before firing.
type stepClock struct {
	mu      sync.Mutex
	now     time.Time
	slept   time.Duration
	onSleep func()
}

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *stepClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.slept += d
	now, f := c.now, c.onSleep
	c.mu.Unlock()
	if f != nil {
		f()
	}
	ch := make(chan time.Time, 1)
	ch <- now
	return ch
}

func TestFileLockTryLock(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "stvor-agent.lock")
	a, b := FileLock{Path: path}, FileLock{Path: path}

	unlock, err := a.TryLock()
	if err != nil {
		t.Fatalf("first TryLock: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("lock file mode %v, want 0600", fi.Mode().Perm())
	}
	if _, err := b.TryLock(); !errors.Is(err, ErrLocked) {
		t.Fatalf("second TryLock while held: err = %v, want ErrLocked", err)
	}
	unlock()
	unlock2, err := b.TryLock()
	if err != nil {
		t.Fatalf("TryLock after unlock: %v", err)
	}
	unlock2()
}

func TestFileLockTryLockBadPath(t *testing.T) {
	t.Parallel()
	l := FileLock{Path: filepath.Join(t.TempDir(), "no", "such", "dir", "lock")}
	_, err := l.TryLock()
	if err == nil || errors.Is(err, ErrLocked) {
		t.Fatalf("err = %v, want an open error other than ErrLocked", err)
	}
}

func TestFileLockTryLockRefusesSymlink(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	path := filepath.Join(dir, "lock")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := (FileLock{Path: path}).TryLock(); err == nil {
		t.Fatal("TryLock followed a symlink")
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("symlink target was created: %v", err)
	}
}

func TestFileLockWaitTimesOut(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "lock")
	holder := FileLock{Path: path}
	unlock, err := holder.TryLock()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	clk := &stepClock{now: time.Unix(1_700_000_000, 0)}
	l := FileLock{Path: path, Now: clk.Now, After: clk.After}
	_, err = l.Lock(context.Background(), CLILockWait)
	if !errors.Is(err, ErrLockTimeout) {
		t.Fatalf("err = %v, want ErrLockTimeout", err)
	}
	if clk.slept < CLILockWait || clk.slept > CLILockWait+time.Second {
		t.Fatalf("waited %v on the fake clock, want about %v", clk.slept, CLILockWait)
	}
}

func TestFileLockWaitGetsLock(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "lock")
	unlockHolder, err := (FileLock{Path: path}).TryLock()
	if err != nil {
		t.Fatal(err)
	}
	clk := &stepClock{now: time.Unix(1_700_000_000, 0)}
	var once sync.Once
	clk.onSleep = func() { once.Do(unlockHolder) } // the holder leaves during the first wait
	l := FileLock{Path: path, Now: clk.Now, After: clk.After}
	unlock, err := l.Lock(context.Background(), CLILockWait)
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	unlock()
	if clk.slept == 0 || clk.slept >= CLILockWait {
		t.Fatalf("waited %v, want one short poll", clk.slept)
	}
}

func TestFileLockWaitFreeLockDoesNotSleep(t *testing.T) {
	t.Parallel()
	clk := &stepClock{}
	l := FileLock{Path: filepath.Join(t.TempDir(), "lock"), Now: clk.Now, After: clk.After}
	unlock, err := l.Lock(context.Background(), CLILockWait)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	if clk.slept != 0 {
		t.Fatalf("slept %v on a free lock", clk.slept)
	}
}

func TestFileLockWaitCancelled(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "lock")
	unlockHolder, err := (FileLock{Path: path}).TryLock()
	if err != nil {
		t.Fatal(err)
	}
	defer unlockHolder()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	never := func(time.Duration) <-chan time.Time { return nil }
	l := FileLock{Path: path, After: never}
	if _, err := l.Lock(ctx, CLILockWait); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
