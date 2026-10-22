package buffer

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeClock is the wall clock and CLOCK_BOOTTIME of a test; it moves only
// when the test advances it.
type fakeClock struct {
	now  time.Time
	mono int64
}

func newClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), mono: 1_000}
}

func (c *fakeClock) Now() time.Time { return c.now }
func (c *fakeClock) MonoMS() int64  { return c.mono }
func (c *fakeClock) advance(d time.Duration) {
	c.now = c.now.Add(d)
	c.mono += d.Milliseconds()
}

// seededRand is a deterministic reader for ULIDs.
type seededRand struct{ n byte }

func (r *seededRand) Read(p []byte) (int, error) {
	for i := range p {
		r.n = r.n*31 + 7
		p[i] = r.n
	}
	return len(p), nil
}

func testConfig(dir string, clk *fakeClock) Config {
	return Config{
		Dir:    dir,
		Now:    clk.Now,
		MonoMS: clk.MonoMS,
		Rand:   &seededRand{},
	}
}

func openBuffer(t *testing.T, cfg Config) *Buffer {
	t.Helper()
	b, err := Open(cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return b
}

const testBoot = "11111111-2222-3333-4444-555555555555"

// record is one collector tick: a few gauges and counters whose values move
// so that gzip cannot fold them away.
func record(clk *fakeClock, boot string, synced bool) Record {
	ms := float64(clk.mono)
	return Record{
		At:          clk.now,
		MonoMS:      clk.mono,
		BootID:      boot,
		ClockSynced: synced,
		Samples: []Sample{
			{Name: "system.memory.usage", Kind: Gauge, Labels: []Label{{"state", "used"}}, Value: 1e9 + math.Mod(ms*7919, 1e7)},
			{Name: "system.memory.usage", Kind: Gauge, Labels: []Label{{"state", "free"}}, Value: 2e9 - math.Mod(ms*104729, 1e7)},
			{Name: "system.cpu.load_average.1m", Kind: Gauge, Value: math.Mod(ms/997, 4)},
			{Name: "system.cpu.time", Kind: Counter, Labels: []Label{{"mode", "user"}}, Value: ms / 3},
			{Name: "system.cpu.time", Kind: Counter, Labels: []Label{{"mode", "system"}}, Value: ms / 11},
			{Name: "system.network.io", Kind: Counter, Labels: []Label{{"iface", "eth0"}, {"direction", "receive"}}, Value: ms * 13},
		},
	}
}

// tick advances the clock by one 10-s fast interval and adds one record.
func tick(t *testing.T, b *Buffer, clk *fakeClock) {
	t.Helper()
	clk.advance(10 * time.Second)
	if err := b.Add(record(clk, testBoot, true)); err != nil {
		t.Fatalf("Add: %v", err)
	}
}

// segFiles lists the segment files in dir.
func segFiles(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "seg-") {
			out = append(out, e.Name())
		}
	}
	return out
}

func mustNext(t *testing.T, b *Buffer) *Chunk {
	t.Helper()
	c, err := b.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	return c
}

func mustAck(t *testing.T, b *Buffer, c *Chunk) {
	t.Helper()
	if err := b.Ack(c); err != nil {
		t.Fatalf("Ack: %v", err)
	}
}

// pointCount sums the points of a segment.
func pointCount(s *Segment) int {
	n := 0
	for _, ser := range s.Series {
		n += len(ser.Points)
	}
	return n
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func join(dir, name string) string { return filepath.Join(dir, name) }
