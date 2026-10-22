package buffer

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A crash leaves temp files of an unfinished write; a restart removes them,
// keeps the stream, continues seq upwards and drains the segments in order.
func TestRestartRecovery(t *testing.T) {
	t.Parallel()
	dir, clk := t.TempDir(), newClock()
	b := openBuffer(t, testConfig(dir, clk))
	for range 150 { // two flushed segments + 5 minutes in RAM
		tick(t, b, clk)
	}
	seq1, err := b.NextSeq()
	if err != nil {
		t.Fatal(err)
	}
	stream := b.Stream()
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	// Power cut in the middle of a write: temp files of segment, events and
	// stream state.
	for _, name := range []string{".seg-1-2-3-r0.json.gz.tmp-123", ".events.json.tmp-9", ".stream.json.tmp-1"} {
		writeFile(t, join(dir, name), []byte("partial"))
	}
	// Power cut between writing a rolled segment and removing its raw
	// original: the rolled one wins.
	names := segFiles(t, dir)
	raw := join(dir, names[0])
	s, err := readSegment(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writeSegment(dir, Rollup(s, 60)); err != nil {
		t.Fatal(err)
	}

	b2 := openBuffer(t, testConfig(dir, clk))
	for _, name := range []string{".seg-1-2-3-r0.json.gz.tmp-123", ".events.json.tmp-9", ".stream.json.tmp-1"} {
		if exists(join(dir, name)) {
			t.Errorf("temp file %s survived restart", name)
		}
	}
	if exists(raw) {
		t.Error("raw original of a rolled segment survived restart")
	}
	if b2.Stream() != stream {
		t.Fatalf("stream %q → %q after restart", stream, b2.Stream())
	}
	seq2, err := b2.NextSeq()
	if err != nil {
		t.Fatal(err)
	}
	if seq2 <= seq1 {
		t.Fatalf("seq after restart %d ≤ %d", seq2, seq1)
	}
	var records uint64
	var prev time.Time
	for c := mustNext(t, b2); c != nil; c = mustNext(t, b2) {
		if c.Kind != ChunkBacklog || c.Segment.StartAt.Before(prev) {
			t.Fatalf("chunk %v at %v after %v", c.Kind, c.Segment.StartAt, prev)
		}
		prev = c.Segment.StartAt
		records += c.Segment.SeqLast - c.Segment.SeqFirst + 1
		mustAck(t, b2, c)
	}
	if records != 150 {
		t.Fatalf("records after restart = %d, want 150", records)
	}
}

// A damaged segment is moved to quarantine with an event; the drain goes on.
func TestCorruptedSegmentQuarantined(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		corrupt func(t *testing.T, path string)
	}{
		{"garbage", func(t *testing.T, path string) { writeFile(t, path, []byte("not gzip")) }},
		{"truncated", func(t *testing.T, path string) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, path, data[:len(data)/2])
		}},
		{"empty", func(t *testing.T, path string) { writeFile(t, path, nil) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, clk := t.TempDir(), newClock()
			b := openBuffer(t, testConfig(dir, clk))
			for range 120 {
				tick(t, b, clk)
			}
			if err := b.Close(); err != nil {
				t.Fatal(err)
			}
			names := segFiles(t, dir)
			tc.corrupt(t, join(dir, names[0]))

			b = openBuffer(t, testConfig(dir, clk))
			c := mustNext(t, b)
			if c.Kind != ChunkEvents || len(c.Events) != 1 || c.Events[0].Kind != "buffer.quarantine" {
				t.Fatalf("chunk = %+v, want the quarantine event", c)
			}
			if got := c.Events[0].Params["segment"]; got != names[0] {
				t.Fatalf("event segment = %v, want %s", got, names[0])
			}
			mustAck(t, b, c)
			if !exists(filepath.Join(dir, quarantineDir, names[0])) {
				t.Fatal("segment not in quarantine")
			}
			c = mustNext(t, b)
			if c.Kind != ChunkBacklog || c.Segment.SeqFirst == 0 {
				t.Fatalf("chunk = %+v, want the intact segment", c)
			}
			mustAck(t, b, c)
			if c = mustNext(t, b); c != nil {
				t.Fatalf("unexpected chunk %+v", c)
			}
		})
	}
}

// The stream file reserves seq in blocks: no write per batch, and a lost
// stream file starts a new stream.
func TestStreamSeq(t *testing.T) {
	t.Parallel()
	dir, clk := t.TempDir(), newClock()
	b := openBuffer(t, testConfig(dir, clk))
	path := join(dir, streamFile)
	before := fileSize(t, path)
	st0, _ := os.Stat(path)
	var last uint64
	for range seqBlock - 1 {
		s, err := b.NextSeq()
		if err != nil {
			t.Fatal(err)
		}
		if s <= last {
			t.Fatalf("seq %d after %d", s, last)
		}
		last = s
	}
	st1, _ := os.Stat(path)
	if !os.SameFile(st0, st1) || fileSize(t, path) != before {
		t.Fatal("stream file rewritten within a reserved block")
	}
	stream := b.Stream()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	clk.advance(time.Second) // a restart happens later; the rand of the test repeats
	b2 := openBuffer(t, testConfig(dir, clk))
	if b2.Stream() == stream {
		t.Fatal("lost stream file kept the old stream")
	}
}

func TestULID(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	a, err := newULID(at, &seededRand{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := newULID(at.Add(time.Millisecond), &seededRand{})
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 26 || a[0] > '7' || a >= b {
		t.Fatalf("ulids %q %q", a, b)
	}
	for _, r := range a {
		if !containsRune(crockford, r) {
			t.Fatalf("%q: %q not Crockford base32", a, r)
		}
	}
	// 2026-10-21T12:00Z = 1792584000000 ms; the first 10 chars encode it.
	if a[:10] != "01M5F5A8G0" {
		t.Fatalf("time part %q", a[:10])
	}
}

func containsRune(s string, r rune) bool {
	for _, c := range s {
		if c == r {
			return true
		}
	}
	return false
}
