package buffer

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
)

var errCrashed = errors.New("crashed")

// faults injects I/O errors into the file operations of a buffer. A crash
// freezes the disk: the matching operation and every later one fail, as if
// the process died there; the test then reopens the directory.
type faults struct {
	mu      sync.Mutex
	crashed bool
	crash   bool
	match   func(op, name string) bool
}

func (f *faults) hook(op, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.crashed {
		return errCrashed
	}
	if f.match != nil && f.match(op, name) {
		if f.crash {
			f.crashed = true
			return errCrashed
		}
		return syscall.EIO
	}
	return nil
}

func (f *faults) set(crash bool, match func(op, name string) bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.crash, f.match = crash, match
}

func (f *faults) clear() { f.set(false, nil) }

// nthSegWrite matches the n-th segment write from now on (1-based).
func nthSegWrite(n int) func(op, name string) bool {
	seen := 0
	return func(op, name string) bool {
		if op != "write" || !strings.HasPrefix(name, segPrefix) {
			return false
		}
		seen++
		return seen == n
	}
}

func faultConfig(dir string, clk *fakeClock, f *faults) Config {
	cfg := testConfig(dir, clk)
	cfg.fault = f.hook
	return cfg
}

// diskChunk fills a buffer with an offline flush worth of ticks and hands
// out the disk segment.
func diskChunk(t *testing.T, b *Buffer, clk *fakeClock) *Chunk {
	t.Helper()
	for range 60 {
		tick(t, b, clk)
	}
	c := mustNext(t, b)
	if c == nil || c.seg == nil {
		t.Fatalf("chunk = %+v, want a disk segment", c)
	}
	return c
}

// A crash while a Split writes its halves loses nothing and sends nothing
// twice: with the halves incomplete, the present ones go and the original
// stays, sent whole once.
func TestSplitCrashWhileWritingHalves(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		crash int // the segment write that crashes
	}{
		{"sidecar written, no halves", 1},
		{"one half written", 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, clk, f := t.TempDir(), newClock(), &faults{}
			b := openBuffer(t, faultConfig(dir, clk, f))
			c := diskChunk(t, b, clk)
			total := pointCount(c.Segment)
			f.set(true, nthSegWrite(tc.crash))
			if _, _, err := b.Split(c); err == nil {
				t.Fatal("split survived the crash")
			}
			if got := segFiles(t, dir); len(got) != tc.crash {
				t.Fatalf("on disk after the crash %v, want the original and %d half", got, tc.crash-1)
			}
			b2 := openBuffer(t, testConfig(dir, clk))
			if got := segFiles(t, dir); len(got) != 1 || got[0] != c.seg.name {
				t.Fatalf("after restart %v, want only the original %s", got, c.seg.name)
			}
			r := mustNext(t, b2)
			if r == nil || pointCount(r.Segment) != total {
				t.Fatalf("after restart %+v, want the whole original (%d points)", r, total)
			}
			mustAck(t, b2, r)
			if r := mustNext(t, b2); r != nil {
				t.Fatalf("left %+v", r)
			}
		})
	}
}

// A failed unlink in Ack keeps the batch in flight with its identity; a
// crash between the unlink and the sidecar write sends nothing again.
func TestAckFaults(t *testing.T) {
	t.Parallel()
	t.Run("unlink EIO keeps identity", func(t *testing.T) {
		t.Parallel()
		dir, clk, f := t.TempDir(), newClock(), &faults{}
		b := openBuffer(t, faultConfig(dir, clk, f))
		c := diskChunk(t, b, clk)
		f.set(false, func(op, name string) bool { return op == "remove" && name == c.seg.name })
		if err := b.Ack(c); !errors.Is(err, syscall.EIO) {
			t.Fatalf("Ack err = %v, want EIO", err)
		}
		r := mustNext(t, b)
		if r != c || r.Seq != c.Seq || r.BatchID != c.BatchID {
			t.Fatalf("retry %+v, want the same batch seq %d", r, c.Seq)
		}
		f.clear()
		mustAck(t, b, r)
		if got := segFiles(t, dir); len(got) != 0 {
			t.Fatalf("acked segment left: %v", got)
		}
		if r := mustNext(t, b); r != nil {
			t.Fatalf("left %+v", r)
		}
	})
	t.Run("crash between unlink and sidecar", func(t *testing.T) {
		t.Parallel()
		dir, clk, f := t.TempDir(), newClock(), &faults{}
		b := openBuffer(t, faultConfig(dir, clk, f))
		c := diskChunk(t, b, clk)
		f.set(true, func(_, name string) bool { return name == inflightFile })
		if err := b.Ack(c); err == nil {
			t.Fatal("Ack survived the crash")
		}
		if !exists(join(dir, inflightFile)) || exists(join(dir, c.seg.name)) {
			t.Fatal("want the identity without its data on disk")
		}
		b2 := openBuffer(t, testConfig(dir, clk))
		if r := mustNext(t, b2); r != nil {
			t.Fatalf("acked data sent again: %+v", r)
		}
		if exists(join(dir, inflightFile)) {
			t.Fatal("stale identity kept")
		}
	})
}

// A failed unlink of the Split original leaves a tombstone: the original is
// never handed out again (neither now nor after a restart) and goes once
// the unlink succeeds.
func TestSplitOriginalUnlinkEIO(t *testing.T) {
	t.Parallel()
	dir, clk, f := t.TempDir(), newClock(), &faults{}
	b := openBuffer(t, faultConfig(dir, clk, f))
	c := diskChunk(t, b, clk)
	orig := c.seg.name
	f.set(false, func(op, name string) bool { return op == "remove" && name == orig })
	a, h, err := b.Split(c)
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	if !exists(join(dir, orig)) {
		t.Fatal("the original is gone, want it kept by the failing unlink")
	}
	var st pinnedState
	data, err := os.ReadFile(join(dir, inflightFile))
	if err != nil || json.Unmarshal(data, &st) != nil || len(st.Tombstones) != 1 || st.Tombstones[0] != orig {
		t.Fatalf("sidecar %s (%v), want the original tombstoned", data, err)
	}
	for _, want := range []*Chunk{a, h} {
		r := mustNext(t, b)
		if r != want {
			t.Fatalf("next %+v, want seq %d", r, want.Seq)
		}
		mustAck(t, b, r)
	}
	if r := mustNext(t, b); r != nil {
		t.Fatalf("the original came back: %+v", r)
	}
	// Crash and restart with the unlink still failing.
	b2 := openBuffer(t, faultConfig(dir, clk, f))
	if r := mustNext(t, b2); r != nil {
		t.Fatalf("the original came back after restart: %+v", r)
	}
	if !exists(join(dir, orig)) || !exists(join(dir, inflightFile)) {
		t.Fatal("want the original and its tombstone kept while the unlink fails")
	}
	f.clear()
	if r := mustNext(t, b2); r != nil {
		t.Fatalf("left %+v", r)
	}
	if exists(join(dir, orig)) || exists(join(dir, inflightFile)) {
		t.Fatal("tombstone not reaped after the unlink recovered")
	}
}
