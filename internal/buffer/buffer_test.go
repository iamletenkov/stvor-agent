package buffer

import (
	"math"
	"os"
	"strings"
	"testing"
	"time"
)

// Online, every tick is sent and acknowledged: nothing ever reaches the disk
// (eMMC wear).
func TestOnlineAckedRecordsNeverHitDisk(t *testing.T) {
	t.Parallel()
	dir, clk := t.TempDir(), newClock()
	b := openBuffer(t, testConfig(dir, clk))
	for range 360 { // one hour of 10-s ticks
		tick(t, b, clk)
		c := mustNext(t, b)
		if c == nil || c.Kind != ChunkFresh || c.Backfill {
			t.Fatalf("chunk = %+v, want fresh", c)
		}
		mustAck(t, b, c)
	}
	if got := segFiles(t, dir); len(got) != 0 {
		t.Fatalf("segments written while online: %v", got)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if got := segFiles(t, dir); len(got) != 0 {
		t.Fatalf("segments written on close with nothing pending: %v", got)
	}
}

// Offline, records stay in RAM and reach the disk once per FlushEvery, not
// every tick.
func TestFlushOnlyEveryInterval(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		every time.Duration
		ticks int
		want  int
	}{
		{"9m50s — still in RAM", 10 * time.Minute, 59, 0},
		{"10m — one segment", 10 * time.Minute, 60, 1},
		{"30m — three segments", 10 * time.Minute, 180, 3},
		{"5m interval, 30m — six segments", 5 * time.Minute, 180, 6},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, clk := t.TempDir(), newClock()
			cfg := testConfig(dir, clk)
			cfg.FlushEvery = tc.every
			b := openBuffer(t, cfg)
			for range tc.ticks {
				tick(t, b, clk)
			}
			if got := segFiles(t, dir); len(got) != tc.want {
				t.Fatalf("segments = %d (%v), want %d", len(got), got, tc.want)
			}
		})
	}
}

// A segment carries stream, boot_id, clock_synced, monotonic offsets and the
// seq range; a reboot or a change of clock sync starts a new segment.
func TestSegmentCarriesClockAndBoot(t *testing.T) {
	t.Parallel()
	dir, clk := t.TempDir(), newClock()
	b := openBuffer(t, testConfig(dir, clk))
	const boot2 = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	for i := range 30 {
		clk.advance(10 * time.Second)
		boot, synced := testBoot, i >= 10
		if i >= 20 {
			boot = boot2
		}
		if err := b.Add(record(clk, boot, synced)); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Flush(); err != nil {
		t.Fatal(err)
	}
	names := segFiles(t, dir)
	if len(names) != 3 {
		t.Fatalf("segments = %v, want 3 (unsynced, synced, new boot)", names)
	}
	want := []struct {
		boot   string
		synced bool
	}{{testBoot, false}, {testBoot, true}, {boot2, true}}
	var prevLast uint64
	for i, name := range names {
		s, err := readSegment(join(dir, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if s.BootID != want[i].boot || s.ClockSynced != want[i].synced {
			t.Errorf("%s: boot=%s synced=%v, want %+v", name, s.BootID, s.ClockSynced, want[i])
		}
		if s.Stream != b.Stream() || len(s.Stream) != 26 {
			t.Errorf("%s: stream %q, buffer %q", name, s.Stream, b.Stream())
		}
		if s.SeqFirst <= prevLast || s.SeqLast < s.SeqFirst || s.SeqLast-s.SeqFirst != 9 {
			t.Errorf("%s: seq %d..%d after %d", name, s.SeqFirst, s.SeqLast, prevLast)
		}
		prevLast = s.SeqLast
		if s.StartMonoMS <= 0 || s.RollupS != 0 {
			t.Errorf("%s: start_mono=%d rollup=%d", name, s.StartMonoMS, s.RollupS)
		}
		for _, ser := range s.Series {
			if len(ser.Points) != 10 {
				t.Errorf("%s %s: %d points, want 10", name, ser.Name, len(ser.Points))
			}
			for j, p := range ser.Points {
				if p.Off != int64(j)*10_000 {
					t.Errorf("%s %s: point %d offset %d", name, ser.Name, j, p.Off)
				}
			}
		}
	}
}

// Drain order (contract retry.order): fresh points → events → backlog from
// the oldest to the newest; the in-flight chunk is repeated until acked; new
// fresh data overtakes the backlog.
func TestDrainOrder(t *testing.T) {
	t.Parallel()
	dir, clk := t.TempDir(), newClock()
	b := openBuffer(t, testConfig(dir, clk))
	for range 120 { // two flushed segments
		tick(t, b, clk)
	}
	for _, kind := range []string{"unit.failed", "disk.threshold"} {
		if err := b.AddEvent(Event{Kind: kind, Severity: "warning", At: clk.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	tick(t, b, clk) // fresh, in RAM

	c := mustNext(t, b)
	if c.Kind != ChunkFresh || c.Backfill || pointCount(c.Segment) != 6 {
		t.Fatalf("1st = %+v, want fresh with one tick", c)
	}
	if again := mustNext(t, b); again != c {
		t.Fatal("unacked chunk not repeated")
	}
	mustAck(t, b, c)

	c = mustNext(t, b)
	if c.Kind != ChunkEvents || len(c.Events) != 2 || c.Events[0].Kind != "unit.failed" {
		t.Fatalf("2nd = %+v, want both events oldest first", c)
	}
	for _, e := range c.Events {
		if len(e.ID) != 26 {
			t.Fatalf("event id %q is not a ULID", e.ID)
		}
	}
	mustAck(t, b, c)

	c = mustNext(t, b)
	if c.Kind != ChunkBacklog || !c.Backfill {
		t.Fatalf("3rd = %+v, want backlog", c)
	}
	first := c.Segment.StartAt
	mustAck(t, b, c)

	tick(t, b, clk) // fresh data overtakes the backlog
	if c = mustNext(t, b); c.Kind != ChunkFresh {
		t.Fatalf("after new tick = %v, want fresh", c.Kind)
	}
	mustAck(t, b, c)

	c = mustNext(t, b)
	if c.Kind != ChunkBacklog || !c.Segment.StartAt.After(first) {
		t.Fatalf("4th = %+v, want the newer backlog segment", c)
	}
	mustAck(t, b, c)
	if c = mustNext(t, b); c != nil {
		t.Fatalf("drained buffer returned %+v", c)
	}
	if got := segFiles(t, dir); len(got) != 0 {
		t.Fatalf("acked segments left on disk: %v", got)
	}
	if exists(join(dir, eventsFile)) {
		t.Fatal("acked events left on disk")
	}
}

// After a long outage the RAM ring holds more than one chunk: the newest
// window goes first as fresh data, the older part becomes backlog.
func TestFreshIsNewestWindow(t *testing.T) {
	t.Parallel()
	dir, clk := t.TempDir(), newClock()
	cfg := testConfig(dir, clk)
	cfg.FlushEvery = time.Hour
	cfg.ChunkRecords = 10
	b := openBuffer(t, cfg)
	for range 25 {
		tick(t, b, clk)
	}
	c := mustNext(t, b)
	if c.Kind != ChunkFresh || c.Segment.SeqLast-c.Segment.SeqFirst != 9 || !c.Segment.EndAt.Equal(clk.Now()) {
		t.Fatalf("fresh = %+v, want the last 10 ticks", c.Segment)
	}
	mustAck(t, b, c)
	var backlog int
	for c = mustNext(t, b); c != nil; c = mustNext(t, b) {
		if c.Kind != ChunkBacklog {
			t.Fatalf("kind %v, want backlog", c.Kind)
		}
		backlog += int(c.Segment.SeqLast - c.Segment.SeqFirst + 1)
		mustAck(t, b, c)
	}
	if backlog != 15 {
		t.Fatalf("backlog records = %d, want 15", backlog)
	}
}

// Points older than 30 days are never sent (the portal rejects them), and
// raw data older than the full window is rolled up even without pressure.
func TestAgeLimits(t *testing.T) {
	t.Parallel()
	t.Run("30 days cutoff", func(t *testing.T) {
		t.Parallel()
		dir, clk := t.TempDir(), newClock()
		b := openBuffer(t, testConfig(dir, clk))
		for range 60 {
			tick(t, b, clk)
		}
		// Cut in the middle of the segment: its older half is dropped.
		clk.advance(MaxAge - 5*time.Minute)
		c := mustNext(t, b)
		if c == nil || c.Kind != ChunkBacklog {
			t.Fatalf("chunk = %+v, want backlog", c)
		}
		cut := clk.Now().Add(-MaxAge)
		for _, ser := range c.Segment.Series {
			for _, p := range ser.Points {
				if at := c.Segment.StartAt.Add(time.Duration(p.Off) * time.Millisecond); at.Before(cut) {
					t.Fatalf("point at %v older than cutoff %v", at, cut)
				}
			}
		}
		if n := pointCount(c.Segment); n == 0 || n >= 360 {
			t.Fatalf("points = %d, want the newer half", n)
		}
		mustAck(t, b, c)

		for range 60 {
			tick(t, b, clk)
		}
		clk.advance(MaxAge + time.Hour)
		if err := b.Flush(); err != nil {
			t.Fatal(err)
		}
		if got := segFiles(t, dir); len(got) != 0 {
			t.Fatalf("segments older than 30 days kept: %v", got)
		}
		if c := mustNext(t, b); c != nil {
			t.Fatalf("chunk %+v from expired data", c)
		}
	})
	t.Run("autonomy limit from portal", func(t *testing.T) {
		t.Parallel()
		dir, clk := t.TempDir(), newClock()
		b := openBuffer(t, testConfig(dir, clk))
		for range 60 {
			tick(t, b, clk)
		}
		clk.advance(3 * 24 * time.Hour)
		if err := b.SetLimits(0, 2*24*time.Hour); err != nil {
			t.Fatal(err)
		}
		if got := segFiles(t, dir); len(got) != 0 {
			t.Fatalf("segments beyond autonomy kept: %v", got)
		}
	})
	t.Run("raw rolled up after 7 days", func(t *testing.T) {
		t.Parallel()
		dir, clk := t.TempDir(), newClock()
		b := openBuffer(t, testConfig(dir, clk))
		for range 60 {
			tick(t, b, clk)
		}
		clk.advance(DefaultFullFor + time.Hour)
		if err := b.Flush(); err != nil {
			t.Fatal(err)
		}
		names := segFiles(t, dir)
		if len(names) != 1 || !strings.Contains(names[0], "-r60-") {
			t.Fatalf("segments = %v, want one rolled to 60 s", names)
		}
	})
}

// Three days of 10-s points under a tight quota: usage fits, the last day
// stays raw, older days are rolled up (1 min, then 5 min) before anything is
// deleted.
func TestQuotaPressureRollsUpKeepsFreshDay(t *testing.T) {
	t.Parallel()
	dir, clk := t.TempDir(), newClock()
	b := openBuffer(t, testConfig(dir, clk))
	const perDay = 24 * 360
	for range perDay {
		tick(t, b, clk)
	}
	if err := b.Flush(); err != nil {
		t.Fatal(err)
	}
	day := b.Stats().UsedBytes
	quota := day * 3 / 2
	if err := b.SetLimits(quota, 0); err != nil {
		t.Fatal(err)
	}
	for range 2 * perDay {
		tick(t, b, clk)
	}
	if err := b.Flush(); err != nil {
		t.Fatal(err)
	}
	st := b.Stats()
	if st.UsedBytes > quota {
		t.Fatalf("used %d > quota %d", st.UsedBytes, quota)
	}
	if st.RolledUp == 0 {
		t.Fatal("nothing rolled up under pressure")
	}
	if want := clk.Now().Add(-3 * 24 * time.Hour); st.OldestAt.After(want.Add(time.Hour)) {
		t.Fatalf("oldest %v: first day deleted although roll-up would fit", st.OldestAt)
	}
	freshCut := clk.Now().Add(-FreshFull)
	var raw, r60, r300 int
	for _, m := range b.segs {
		switch {
		case !m.end.Before(freshCut) && m.rollup != 0:
			t.Fatalf("segment %s within the last 24 h is rolled up", m.name)
		case m.rollup == 0:
			raw++
		case m.rollup == 60:
			r60++
		case m.rollup == 300:
			r300++
		}
	}
	if raw < 140 || r60+r300 == 0 {
		t.Fatalf("raw=%d r60=%d r300=%d", raw, r60, r300)
	}
	if r300 == 0 {
		t.Fatalf("no 5-min segments: r60=%d", r60)
	}
}

// Agent events are never rolled up and go only after every metric segment
// is gone; the last 24 h are deleted last.
func TestEventsOutliveMetrics(t *testing.T) {
	t.Parallel()
	dir, clk := t.TempDir(), newClock()
	b := openBuffer(t, testConfig(dir, clk))
	for range 360 * 26 { // 26 h
		tick(t, b, clk)
	}
	if err := b.AddEvent(Event{Kind: "unit.failed", Severity: "warning", At: clk.Now(), Params: map[string]any{"unit": "x.service"}}); err != nil {
		t.Fatal(err)
	}
	if err := b.Flush(); err != nil {
		t.Fatal(err)
	}
	evSize := fileSize(t, join(dir, eventsFile))
	// Room for the events and one fresh segment only.
	if err := b.SetLimits(evSize+b.segs[len(b.segs)-1].size, 0); err != nil {
		t.Fatal(err)
	}
	if !exists(join(dir, eventsFile)) {
		t.Fatal("events dropped while metrics existed")
	}
	if len(b.segs) == 0 || len(b.segs) > 2 {
		t.Fatalf("segments left = %d", len(b.segs))
	}
	last := b.segs[len(b.segs)-1]
	if last.rollup != 0 || !last.end.Equal(clk.Now()) {
		t.Fatalf("newest raw segment not kept: %+v", last)
	}
	// Smaller than the events themselves: metrics go first, then events.
	if err := b.SetLimits(1, 0); err != nil {
		t.Fatal(err)
	}
	if len(b.segs) != 0 || exists(join(dir, eventsFile)) {
		t.Fatalf("over a 1-byte quota: %d segments, events file %v", len(b.segs), exists(join(dir, eventsFile)))
	}
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Size()
}

// Values the portal rejects never enter the buffer; the ring is bounded.
func TestAddSanitizesAndBoundsRing(t *testing.T) {
	t.Parallel()
	dir, clk := t.TempDir(), newClock()
	cfg := testConfig(dir, clk)
	cfg.RingRecords = 5
	cfg.FlushEvery = time.Hour
	b := openBuffer(t, cfg)
	for range 8 {
		clk.advance(10 * time.Second)
		r := record(clk, testBoot, true)
		r.Samples = append(r.Samples,
			Sample{Name: "g", Kind: Gauge, Value: math.NaN()},
			Sample{Name: "g", Kind: Gauge, Value: math.Inf(1)},
			Sample{Name: "c", Kind: Counter, Value: -1})
		if err := b.Add(r); err != nil {
			t.Fatal(err)
		}
	}
	if st := b.Stats(); st.Pending != 5 || st.Dropped != 3 {
		t.Fatalf("stats = %+v, want 5 pending and 3 dropped", st)
	}
	c := mustNext(t, b)
	if n := pointCount(c.Segment); n != 5*6 {
		t.Fatalf("points = %d, want only valid samples", n)
	}
}

// A param JSON cannot carry (NaN) is dropped at AddEvent: it would make
// events.json unwritable for every later event.
func TestEventParamsNotJSONDropped(t *testing.T) {
	t.Parallel()
	dir, clk := t.TempDir(), newClock()
	b := openBuffer(t, testConfig(dir, clk))
	if err := b.AddEvent(Event{Kind: "disk.threshold", Severity: "warning", At: clk.Now(),
		Params: map[string]any{"mountpoint": "/", "used_pct": math.NaN()}}); err != nil {
		t.Fatalf("AddEvent: %v", err)
	}
	if err := b.AddEvent(Event{Kind: "unit.failed", Severity: "warning", At: clk.Now()}); err != nil {
		t.Fatalf("a later event: %v", err)
	}
	if p := b.events[0].Params; len(p) != 1 || p["mountpoint"] != "/" {
		t.Errorf("params %v", p)
	}
}
