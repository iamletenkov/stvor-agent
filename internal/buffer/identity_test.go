package buffer

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A RAM chunk in flight when the ring is flushed keeps its identity: its
// records go to their own segment, Next repeats the same Seq and BatchID, and
// a late Ack is honoured.
func TestFlushKeepsInflightIdentity(t *testing.T) {
	t.Parallel()
	dir, clk := t.TempDir(), newClock()
	b := openBuffer(t, testConfig(dir, clk))
	tick(t, b, clk)
	c := mustNext(t, b)
	seq, id := c.Seq, c.BatchID
	if err := b.Flush(); err != nil {
		t.Fatal(err)
	}
	if got := segFiles(t, dir); len(got) != 1 {
		t.Fatalf("segments = %v, want the in-flight batch on disk", got)
	}
	again := mustNext(t, b)
	if again.Seq != seq || again.BatchID != id || pointCount(again.Segment) != 6 {
		t.Fatalf("after flush seq=%d id=%s, want %d %s", again.Seq, again.BatchID, seq, id)
	}
	mustAck(t, b, c) // late ack of the chunk handed out before the flush
	if got := segFiles(t, dir); len(got) != 0 {
		t.Fatalf("acked batch left on disk: %v", got)
	}
	if exists(join(dir, inflightFile)) {
		t.Fatal("sidecar left after ack")
	}
	if c := mustNext(t, b); c != nil {
		t.Fatalf("drained buffer returned %+v", c)
	}
}

// A batch handed out before a restart goes out first after it, with the same
// identity and stream — before any fresh data with a higher seq.
func TestRestartResendsInflightFirst(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		ticks int
		crash bool // no Close: RAM is lost, disk and the sidecar stay
	}{
		{"RAM batch, clean stop", 5, false},
		{"disk batch, clean stop", 60, false},
		{"disk batch, crash", 60, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, clk := t.TempDir(), newClock()
			b := openBuffer(t, testConfig(dir, clk))
			for range tc.ticks {
				tick(t, b, clk)
			}
			c := mustNext(t, b)
			if c == nil {
				t.Fatal("nothing to send")
			}
			if !tc.crash {
				if err := b.Close(); err != nil {
					t.Fatal(err)
				}
			}
			b2 := openBuffer(t, testConfig(dir, clk))
			tick(t, b2, clk) // fresh data must not overtake the old batch
			r := mustNext(t, b2)
			if r == nil || r.Seq != c.Seq || r.BatchID != c.BatchID || r.Stream != c.Stream ||
				r.Segment.SeqFirst != c.Segment.SeqFirst || r.Segment.SeqLast != c.Segment.SeqLast {
				t.Fatalf("after restart %+v, want seq %d id %s", r, c.Seq, c.BatchID)
			}
			mustAck(t, b2, r)
			if n := mustNext(t, b2); n == nil || n.Seq <= c.Seq {
				t.Fatalf("next batch %+v must have a higher seq than %d", n, c.Seq)
			}
		})
	}
}

// Roll-up never replaces the segment in flight: a retry keeps the data and
// the identity.
func TestRollSkipsInflightSegment(t *testing.T) {
	t.Parallel()
	dir, clk := t.TempDir(), newClock()
	b := openBuffer(t, testConfig(dir, clk))
	for range 120 {
		tick(t, b, clk)
	}
	c := mustNext(t, b)
	if c.Kind != ChunkBacklog || c.seg == nil {
		t.Fatalf("chunk = %+v, want a disk segment", c)
	}
	clk.advance(DefaultFullFor + time.Hour)
	if err := b.Flush(); err != nil {
		t.Fatal(err)
	}
	if !exists(join(dir, c.seg.name)) || c.seg.rollup != 0 {
		t.Fatalf("in-flight segment %s rolled up", c.seg.name)
	}
	if b.Stats().RolledUp != 1 {
		t.Fatalf("rolled = %d, want only the other segment", b.Stats().RolledUp)
	}
	if again := mustNext(t, b); again != c || again.Seq != c.Seq {
		t.Fatalf("retry %+v, want the same chunk", again)
	}
}

// Split for a 413: two halves with their own identities that survive a
// flush and a restart; the halves carry all points of the chunk.
func TestSplit(t *testing.T) {
	t.Parallel()
	t.Run("disk segment survives a crash", func(t *testing.T) {
		t.Parallel()
		dir, clk := t.TempDir(), newClock()
		b := openBuffer(t, testConfig(dir, clk))
		for range 60 {
			tick(t, b, clk)
		}
		c := mustNext(t, b)
		orig, err := os.ReadFile(join(dir, c.seg.name))
		if err != nil {
			t.Fatal(err)
		}
		a, h, err := b.Split(c)
		if err != nil {
			t.Fatal(err)
		}
		if exists(join(dir, c.seg.name)) {
			t.Fatal("split original left on disk")
		}
		// Crash before the original was removed: it must not come back.
		writeFile(t, join(dir, c.seg.name), orig)
		if a.Seq <= c.Seq || h.Seq <= a.Seq || a.BatchID == h.BatchID || a.Stream != c.Stream {
			t.Fatalf("halves seq %d, %d after %d", a.Seq, h.Seq, c.Seq)
		}
		if pointCount(a.Segment)+pointCount(h.Segment) != pointCount(c.Segment) || pointCount(a.Segment) == 0 {
			t.Fatalf("points %d + %d, want %d", pointCount(a.Segment), pointCount(h.Segment), pointCount(c.Segment))
		}
		if a.Segment.SeqLast >= h.Segment.SeqFirst {
			t.Fatalf("half ranges overlap: %d..%d and %d..%d", a.Segment.SeqFirst, a.Segment.SeqLast,
				h.Segment.SeqFirst, h.Segment.SeqLast)
		}
		if n := mustNext(t, b); n != a {
			t.Fatalf("next = %+v, want the first half", n)
		}
		// Crash: no Close.
		b2 := openBuffer(t, testConfig(dir, clk))
		tick(t, b2, clk)
		for _, want := range []*Chunk{a, h} {
			r := mustNext(t, b2)
			if r == nil || r.Seq != want.Seq || r.BatchID != want.BatchID || pointCount(r.Segment) != pointCount(want.Segment) {
				t.Fatalf("after restart %+v, want seq %d", r, want.Seq)
			}
			mustAck(t, b2, r)
		}
		if r := mustNext(t, b2); r == nil || r.Kind != ChunkFresh {
			t.Fatalf("after halves %+v, want the fresh tick", r)
		}
		if exists(join(dir, c.seg.name)) {
			t.Fatal("split original survived the restart")
		}
	})
	t.Run("RAM chunk survives a flush", func(t *testing.T) {
		t.Parallel()
		dir, clk := t.TempDir(), newClock()
		b := openBuffer(t, testConfig(dir, clk))
		for range 10 {
			tick(t, b, clk)
		}
		c := mustNext(t, b)
		a, h, err := b.Split(c)
		if err != nil {
			t.Fatal(err)
		}
		if err := b.Flush(); err != nil {
			t.Fatal(err)
		}
		for _, want := range []*Chunk{a, h} {
			r := mustNext(t, b)
			if r != want {
				t.Fatalf("next %+v, want seq %d", r, want.Seq)
			}
			mustAck(t, b, r)
		}
		if r := mustNext(t, b); r != nil {
			t.Fatalf("left %+v", r)
		}
	})
	t.Run("events", func(t *testing.T) {
		t.Parallel()
		dir, clk := t.TempDir(), newClock()
		b := openBuffer(t, testConfig(dir, clk))
		for range 5 {
			if err := b.AddEvent(Event{Kind: "unit.failed", Severity: "warning"}); err != nil {
				t.Fatal(err)
			}
		}
		c := mustNext(t, b)
		a, h, err := b.Split(c)
		if err != nil || len(a.Events) != 2 || len(h.Events) != 3 || h.Seq <= a.Seq {
			t.Fatalf("split events: %v, %+v, %+v", err, a, h)
		}
		mustAck(t, b, mustNext(t, b))
		if r := mustNext(t, b); r != h {
			t.Fatalf("next %+v, want the second half", r)
		}
	})
	t.Run("one record is unsplittable", func(t *testing.T) {
		t.Parallel()
		dir, clk := t.TempDir(), newClock()
		b := openBuffer(t, testConfig(dir, clk))
		tick(t, b, clk)
		c := mustNext(t, b)
		if _, _, err := b.Split(c); !errors.Is(err, ErrUnsplittable) {
			t.Fatalf("err = %v, want ErrUnsplittable", err)
		}
		if _, _, err := b.Split(&Chunk{}); err == nil {
			t.Fatal("split of a chunk not in flight")
		}
	})
}

// A chunk names its stream: after stream.json is recreated, the restored
// batch keeps the old stream and new batches use the new one.
func TestChunkStream(t *testing.T) {
	t.Parallel()
	dir, clk := t.TempDir(), newClock()
	b := openBuffer(t, testConfig(dir, clk))
	for range 60 {
		tick(t, b, clk)
	}
	c := mustNext(t, b)
	if c.Stream != b.Stream() || len(c.Stream) != ulidLen {
		t.Fatalf("chunk stream %q, buffer %q", c.Stream, b.Stream())
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(join(dir, streamFile)); err != nil {
		t.Fatal(err)
	}
	clk.advance(time.Second)
	b2 := openBuffer(t, testConfig(dir, clk))
	if b2.Stream() == c.Stream {
		t.Fatal("stream not recreated")
	}
	r := mustNext(t, b2)
	if r.Stream != c.Stream || r.Seq != c.Seq {
		t.Fatalf("restored batch stream %q seq %d, want %q %d", r.Stream, r.Seq, c.Stream, c.Seq)
	}
	mustAck(t, b2, r)
	tick(t, b2, clk)
	if n := mustNext(t, b2); n.Stream != b2.Stream() {
		t.Fatalf("new batch stream %q, want %q", n.Stream, b2.Stream())
	}
}

// Without a synced clock (a Jetson without an RTC battery boots in 2018 or
// 1970) the age is the buffer uptime since the write, not the point wall
// clock: a jump of the clock neither drops nor rolls the data; the uptime
// survives a reboot.
func TestUnsyncedAgeByUptime(t *testing.T) {
	t.Parallel()
	for _, start := range []time.Time{
		time.Date(2018, 1, 28, 0, 0, 0, 0, time.UTC),
		time.Unix(0, 0).UTC(),
	} {
		t.Run(start.Format("2006"), func(t *testing.T) {
			t.Parallel()
			dir, clk := t.TempDir(), newClock()
			clk.now = start
			b := openBuffer(t, testConfig(dir, clk))
			for range 60 {
				clk.advance(10 * time.Second)
				if err := b.Add(record(clk, testBoot, false)); err != nil {
					t.Fatal(err)
				}
			}
			clk.now = time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC) // NTP sync
			clk.mono += 1000
			if err := b.Flush(); err != nil {
				t.Fatal(err)
			}
			names := segFiles(t, dir)
			if len(names) != 1 || b.segs[0].rollup != 0 {
				t.Fatalf("segments = %v, want one raw", names)
			}
			if err := b.Close(); err != nil {
				t.Fatal(err)
			}
			clk.mono = 500 // reboot
			b = openBuffer(t, testConfig(dir, clk))
			c := mustNext(t, b)
			if c == nil || pointCount(c.Segment) != 360 {
				t.Fatalf("chunk = %+v, want all 360 points", c)
			}
			if err := b.Close(); err != nil {
				t.Fatal(err)
			}
			b = openBuffer(t, testConfig(dir, clk))
			clk.advance(2 * 24 * time.Hour)
			if err := b.Flush(); err != nil {
				t.Fatal(err)
			}
			if got := segFiles(t, dir); len(got) != 1 {
				t.Fatalf("segments after 2 days = %v", got)
			}
			clk.advance(MaxAge)
			if err := b.Flush(); err != nil {
				t.Fatal(err)
			}
			if got := segFiles(t, dir); len(got) != 0 {
				t.Fatalf("segments older than 30 days of uptime kept: %v", got)
			}
		})
	}
}

// Quarantine counts toward the quota, keeps at most maxQuarantineFiles files
// no older than MaxAge, and goes first under pressure.
func TestQuarantineQuotaAndPrune(t *testing.T) {
	t.Parallel()
	dir, clk := t.TempDir(), newClock()
	qdir := join(dir, quarantineDir)
	if err := os.MkdirAll(qdir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := range maxQuarantineFiles + 5 {
		p := filepath.Join(qdir, fmt.Sprintf("seg-bad-%02d", i))
		writeFile(t, p, make([]byte, 1000))
		mt := clk.Now().Add(time.Duration(i-maxQuarantineFiles-5) * time.Minute)
		if i == maxQuarantineFiles+4 {
			mt = clk.Now().Add(-MaxAge - time.Hour) // the newest name, but too old
		}
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	b := openBuffer(t, testConfig(dir, clk))
	ents, err := os.ReadDir(qdir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != maxQuarantineFiles {
		t.Fatalf("quarantine files = %d, want %d", len(ents), maxQuarantineFiles)
	}
	if exists(filepath.Join(qdir, fmt.Sprintf("seg-bad-%02d", maxQuarantineFiles+4))) ||
		exists(filepath.Join(qdir, "seg-bad-00")) {
		t.Fatal("old quarantine files kept")
	}
	if st := b.Stats(); st.UsedBytes != int64(maxQuarantineFiles)*1000 {
		t.Fatalf("used = %d, quarantine not counted", st.UsedBytes)
	}
	for range 60 {
		tick(t, b, clk)
	}
	if err := b.SetLimits(b.segs[0].size+10, 0); err != nil {
		t.Fatal(err)
	}
	if ents, _ := os.ReadDir(qdir); len(ents) != 0 || len(b.segs) != 1 {
		t.Fatalf("under pressure: quarantine %d files, segments %d", len(ents), len(b.segs))
	}
}

// Random runs of Add / Next / delivery / lost ack / failure / late ack /
// Flush / roll-up / restart / crash / Split against a model portal that
// accepts a batch only when seq > last_seq of its stream: no tick is ever
// accepted twice; without crashes and roll-ups every tick arrives and no
// batch the portal never saw is answered as a duplicate.
func TestBatchIdentityProperty(t *testing.T) {
	t.Parallel()
	for seed := range 80 {
		lossless := seed%2 == 0
		t.Run(fmt.Sprintf("seed %d lossless %v", seed, lossless), func(t *testing.T) {
			t.Parallel()
			runIdentityModel(t, uint64(seed), lossless)
		})
	}
}

// modelPortal tells ticks apart by the cumulative counter of the record (a
// roll-up keeps the last value of a window, so a rolled point is still one
// original tick).
type modelPortal struct {
	lossless bool
	last     map[string]uint64  // stream → last_seq
	seen     map[string]bool    // batches received
	ticks    map[float64]string // counter value → batch that brought it
}

const tickSeries = "system.network.io"

func (p *modelPortal) receive(t *testing.T, c *Chunk) {
	t.Helper()
	key := fmt.Sprintf("%s/%d", c.Stream, c.Seq)
	if c.Seq <= p.last[c.Stream] {
		if p.lossless && !p.seen[key] && c.Segment != nil {
			t.Fatalf("batch %s never received is answered as a duplicate: last_seq %d", key, p.last[c.Stream])
		}
		return // 200 duplicate
	}
	p.seen[key] = true
	p.last[c.Stream] = c.Seq
	if c.Segment == nil {
		return
	}
	for _, ser := range c.Segment.Series {
		if ser.Name != tickSeries {
			continue
		}
		for _, pt := range ser.Points {
			if prev, ok := p.ticks[pt.V]; ok {
				t.Fatalf("tick %v accepted twice: in %s and %s", pt.V, prev, key)
			}
			p.ticks[pt.V] = key
		}
	}
}

func runIdentityModel(t *testing.T, seed uint64, lossless bool) {
	rng := rand.New(rand.NewPCG(seed, seed*7+1))
	dir, clk := t.TempDir(), newClock()
	cfg := testConfig(dir, clk)
	if !lossless {
		cfg.FullFor = 2 * time.Minute
	}
	cfg.RingRecords = 100_000
	cfg.ChunkRecords = 1 + rng.IntN(20)
	b := openBuffer(t, cfg)
	p := &modelPortal{lossless: lossless, last: map[string]uint64{}, seen: map[string]bool{}, ticks: map[float64]string{}}
	var added []float64
	var unacked *Chunk // received by the portal, the answer not yet delivered
	for range 400 {
		switch op := rng.IntN(13); op {
		case 0, 1, 2, 3:
			tick(t, b, clk)
			added = append(added, float64(clk.mono)*13)
		case 4: // delivered and acked
			if c := mustNext(t, b); c != nil {
				p.receive(t, c)
				mustAck(t, b, c)
			}
		case 5: // delivered, the answer lost
			if c := mustNext(t, b); c != nil {
				p.receive(t, c)
				unacked = c
			}
		case 6: // the late answer arrives
			if unacked != nil {
				mustAck(t, b, unacked)
				unacked = nil
			}
		case 7: // not delivered (Nack): the client retries later
			mustNext(t, b)
		case 8:
			if err := b.Flush(); err != nil {
				t.Fatal(err)
			}
		case 9: // roll-up of raw data older than FullFor
			if lossless {
				continue
			}
			clk.advance(5 * time.Minute)
			if err := b.Flush(); err != nil {
				t.Fatal(err)
			}
		case 10:
			if err := b.Close(); err != nil {
				t.Fatal(err)
			}
			b, unacked = openBuffer(t, cfg), nil
		case 11: // crash: RAM lost
			if lossless {
				continue
			}
			b, unacked = openBuffer(t, cfg), nil
		case 12: // 413; a batch the portal already holds is answered as a duplicate first
			c := mustNext(t, b)
			switch {
			case c == nil:
			case c.Seq <= p.last[c.Stream]:
				p.receive(t, c)
				mustAck(t, b, c)
			default:
				if _, _, err := b.Split(c); err != nil && !errors.Is(err, ErrUnsplittable) {
					t.Fatal(err)
				}
			}
		}
	}
	for range 100_000 {
		c := mustNext(t, b)
		if c == nil {
			break
		}
		p.receive(t, c)
		mustAck(t, b, c)
	}
	if c := mustNext(t, b); c != nil {
		t.Fatal("buffer not drained")
	}
	if !lossless {
		return
	}
	for _, v := range added {
		if _, ok := p.ticks[v]; !ok {
			t.Fatalf("tick %v lost", v)
		}
	}
	if len(p.ticks) != len(added) {
		t.Fatalf("ticks accepted %d, added %d", len(p.ticks), len(added))
	}
}

// The events file never outgrows the read limit: the oldest events go, the
// tail stays and loads after a restart instead of going to quarantine whole.
func TestEventsFileKeepsTail(t *testing.T) {
	t.Parallel()
	dir, clk := t.TempDir(), newClock()
	b := openBuffer(t, testConfig(dir, clk))
	big := strings.Repeat("a", 7<<20)
	for i := range 3 {
		if err := b.AddEvent(Event{Kind: "unit.failed", Severity: "warning", Params: map[string]any{"i": i, "x": big}}); err != nil {
			t.Fatal(err)
		}
	}
	if size := fileSize(t, join(dir, eventsFile)); size > maxEventsBytes {
		t.Fatalf("events file %d bytes > %d", size, maxEventsBytes)
	}
	b2 := openBuffer(t, testConfig(dir, clk))
	c := mustNext(t, b2)
	if c == nil || c.Kind != ChunkEvents || len(c.Events) != 2 || c.Events[1].Params["i"] != float64(2) {
		t.Fatalf("after restart %+v, want the two newest events", c)
	}
}
