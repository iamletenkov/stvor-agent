package buffer

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Limits and defaults (spec §6, contract limits and config.defaults).
const (
	MaxAge              = 30 * 24 * time.Hour // contract point_max_age_days
	DefaultFullFor      = 7 * 24 * time.Hour  // raw data kept unrolled
	FreshFull           = 24 * time.Hour      // never rolled up, deleted last
	DefaultQuotaBytes   = 256 << 20
	DefaultFlushEvery   = 10 * time.Minute
	DefaultChunkRecords = 60  // ten minutes of fast ticks
	DefaultRingRecords  = 720 // an hour of ticks even in the 5-s detail mode
	MaxChunkEvents      = 500 // contract events_per_request_max
)

// Config is the buffer setup; Dir, Now and MonoMS are required.
type Config struct {
	Dir          string
	Now          func() time.Time // device wall clock
	MonoMS       func() int64     // CLOCK_BOOTTIME, ms
	Rand         io.Reader        // ULIDs; nil — crypto/rand
	FlushEvery   time.Duration    // RAM → disk; 0 — DefaultFlushEvery
	ChunkRecords int              // records per RAM chunk; 0 — DefaultChunkRecords
	RingRecords  int              // RAM ring capacity; 0 — DefaultRingRecords
	QuotaBytes   int64            // disk quota; 0 — DefaultQuotaBytes
	Autonomy     time.Duration    // how long data is kept; 0 or above MaxAge — MaxAge
	FullFor      time.Duration    // raw data older than this is rolled up; 0 — DefaultFullFor

	// fault, when set, runs before each file operation of the buffer (op is
	// "remove", "write" or "sync", name the file): tests inject I/O errors and
	// crashes with it.
	fault func(op, name string) error
}

// ChunkKind is the source of a chunk, in drain order.
type ChunkKind int

// Chunk kinds.
const (
	ChunkFresh   ChunkKind = iota + 1 // the newest points from RAM
	ChunkEvents                       // agent events
	ChunkBacklog                      // older points: disk segments, then the rest of RAM
)

// Chunk is one unit to send. Next returns the same chunk until it is acked,
// so a retry keeps Stream, Seq and BatchID — also after a flush, a roll-up
// and a restart (the identity of a disk batch is kept in inflightFile).
type Chunk struct {
	Kind     ChunkKind
	Backfill bool   // contract batch.backfill
	Stream   string // the stream Seq belongs to
	Seq      uint64
	BatchID  string
	Segment  *Segment // fresh and backlog
	Events   []Event  // events

	seg       *segMeta // disk backlog
	replaces  string   // the segment a Split replaced
	ram       bool
	ramFirst  uint64
	ramLast   uint64
	eventsIDs map[string]bool
}

// Stats is the buffer state for the batch "buffer" field and status.
type Stats struct {
	UsedBytes  int64
	QuotaBytes int64
	OldestAt   time.Time // zero when empty
	Segments   int
	Events     int
	Pending    int // records in RAM
	RolledUp   int // roll-ups since Open
	Dropped    int // segments and records dropped since Open
}

// ErrClosed is returned after Close.
var ErrClosed = errors.New("буфер закрыт")

type pendingRec struct {
	seq uint64
	rec Record
}

// Buffer is the telemetry buffer. It is safe for concurrent use.
type Buffer struct {
	mu  sync.Mutex
	cfg Config

	stream            string
	nextSeq, reserved uint64

	pending    []pendingRec // RAM ring, by seq
	freshMark  uint64       // highest seq handed out as fresh
	emptySince int64        // mono ms: the RAM ring is unflushed since

	segs   []*segMeta // disk, by seq
	used   int64      // bytes of segs
	events []Event
	evSize int64

	inflight *Chunk
	queued   []*Chunk // batches with a fixed identity that go before anything new
	tombs    []string // segments replaced by a Split whose removal failed: never sent again
	lastBoot string
	synced   bool

	upBase, monoOpen int64 // buffer uptime at Open and CLOCK_BOOTTIME then
	qUsed            int64 // bytes in quarantine

	rolledUp, dropped int
	closed            bool
}

// Open opens the buffer in cfg.Dir: removes temp files of interrupted
// writes, drops raw originals of rolled-up segments, loads events and the
// stream.
func Open(cfg Config) (*Buffer, error) {
	if cfg.Dir == "" || cfg.Now == nil || cfg.MonoMS == nil {
		return nil, errors.New("буфер: нужны Dir, Now и MonoMS")
	}
	if cfg.Rand == nil {
		cfg.Rand = rand.Reader
	}
	if cfg.FlushEvery <= 0 {
		cfg.FlushEvery = DefaultFlushEvery
	}
	if cfg.ChunkRecords <= 0 {
		cfg.ChunkRecords = DefaultChunkRecords
	}
	if cfg.RingRecords <= 0 {
		cfg.RingRecords = DefaultRingRecords
	}
	if cfg.QuotaBytes <= 0 {
		cfg.QuotaBytes = DefaultQuotaBytes
	}
	if cfg.Autonomy <= 0 || cfg.Autonomy > MaxAge {
		cfg.Autonomy = MaxAge
	}
	if cfg.FullFor <= 0 {
		cfg.FullFor = DefaultFullFor
	}
	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("каталог буфера: %w", err)
	}
	b := &Buffer{cfg: cfg, emptySince: cfg.MonoMS()}
	bad, err := b.scan()
	if err != nil {
		return nil, err
	}
	if err := b.loadEvents(); err != nil {
		return nil, err
	}
	for _, name := range bad {
		if err := b.quarantineFile(name, "name"); err != nil {
			return nil, err
		}
	}
	var maxSeq uint64
	var maxUp int64
	for _, m := range b.segs {
		maxSeq, maxUp = max(maxSeq, m.seqLast), max(maxUp, m.up)
	}
	if err := b.loadStream(maxSeq+1, maxUp); err != nil {
		return nil, err
	}
	if err := b.loadPinned(); err != nil {
		return nil, err
	}
	if err := b.pruneQuarantine(); err != nil {
		return nil, err
	}
	return b, nil
}

// scan reads the directory: temp files go, segments are indexed, a raw
// segment with a rolled-up copy goes. It returns segment-like names it
// could not parse.
func (b *Buffer) scan() ([]string, error) {
	ents, err := os.ReadDir(b.cfg.Dir)
	if err != nil {
		return nil, fmt.Errorf("каталог буфера: %w", err)
	}
	var bad []string
	byRange := map[[2]uint64]*segMeta{}
	for _, e := range ents {
		name := e.Name()
		switch {
		case strings.HasPrefix(name, ".") && strings.Contains(name, ".tmp-"):
			if err := os.Remove(filepath.Join(b.cfg.Dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
		case strings.HasPrefix(name, segPrefix) && e.Type().IsRegular():
			m, err := parseSegName(name)
			if err != nil {
				bad = append(bad, name)
				continue
			}
			info, err := e.Info()
			if err != nil {
				return nil, err
			}
			m.size = info.Size()
			key := [2]uint64{m.seqFirst, m.seqLast}
			if old, ok := byRange[key]; ok {
				// A crash between writing a rolled copy and removing the
				// original: the coarser one wins.
				keep, drop := old, m
				if m.rollup > old.rollup {
					keep, drop = m, old
				}
				if err := b.removeFile(drop.name); err != nil {
					return nil, err
				}
				m = keep
			}
			byRange[key] = m
		}
	}
	for _, m := range byRange {
		b.segs = append(b.segs, m)
		b.used += m.size
	}
	b.sortSegs()
	return bad, nil
}

func (b *Buffer) sortSegs() {
	slices.SortFunc(b.segs, func(x, y *segMeta) int {
		switch {
		case x.seqFirst < y.seqFirst:
			return -1
		case x.seqFirst > y.seqFirst:
			return 1
		}
		return 0
	})
}

func (b *Buffer) faultAt(op, name string) error {
	if b.cfg.fault == nil {
		return nil
	}
	return b.cfg.fault(op, name)
}

func (b *Buffer) removeFile(name string) error {
	err := b.faultAt("remove", name)
	if err == nil {
		err = os.Remove(filepath.Join(b.cfg.Dir, name))
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("буфер: %w", err)
	}
	return nil
}

// Stream is the stream ULID of the buffer (contract R5).
func (b *Buffer) Stream() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stream
}

// NextSeq allocates the next seq of the stream; it strictly grows, also
// across restarts.
func (b *Buffer) NextSeq() (uint64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.allocSeq()
}

// Add puts a collector tick into the RAM ring and flushes the ring to disk
// once per FlushEvery. NaN, Inf and negative counters are dropped (the
// portal rejects them).
func (b *Buffer) Add(r Record) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return ErrClosed
	}
	samples := make([]Sample, 0, len(r.Samples))
	for _, s := range r.Samples {
		if math.IsNaN(s.Value) || math.IsInf(s.Value, 0) || (s.Kind == Counter && s.Value < 0) {
			continue
		}
		samples = append(samples, s)
	}
	r.Samples = samples
	seq, err := b.allocSeq()
	if err != nil {
		return err
	}
	mono := b.cfg.MonoMS()
	if len(b.pending) == 0 && b.elapsed(mono) >= b.cfg.FlushEvery {
		// After an idle gap the flush timer starts with this record.
		b.emptySince = mono
	}
	b.pending = append(b.pending, pendingRec{seq: seq, rec: r})
	if over := len(b.pending) - b.cfg.RingRecords; over > 0 {
		b.pending = slices.Delete(b.pending, 0, over)
		b.dropped += over
	}
	b.lastBoot, b.synced = r.BootID, r.ClockSynced
	// The timer runs from the moment the ring was last empty: online every
	// tick is acked at once and nothing reaches the disk (eMMC wear).
	if b.elapsed(mono) >= b.cfg.FlushEvery {
		return b.flush()
	}
	return nil
}

func (b *Buffer) elapsed(mono int64) time.Duration {
	return time.Duration(mono-b.emptySince) * time.Millisecond
}

// Flush writes the RAM ring to disk now and applies the age limits and the
// quota.
func (b *Buffer) Flush() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.flush()
}

// Close flushes the RAM ring; the buffer is unusable afterwards.
func (b *Buffer) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	err := b.flush()
	if serr := b.saveStream(); err == nil {
		err = serr
	}
	b.closed = true
	return err
}

func (b *Buffer) flush() error {
	if c := b.inflight; c != nil && c.ram {
		// The batch in flight keeps its identity: its records go to their own
		// segment that carries it.
		if err := b.pinRAM(c); err != nil {
			return err
		}
	}
	for len(b.pending) > 0 {
		n := 1
		for n < len(b.pending) && sameClock(b.pending[n], b.pending[0]) {
			n++
		}
		recs := b.pending[:n]
		m, err := b.writeSeg(buildSegment(b.stream, recs, b.uptimeAt(recs[n-1].rec.MonoMS)))
		if err != nil {
			return err
		}
		b.segs = append(b.segs, m)
		b.used += m.size
		b.pending = b.pending[n:]
	}
	b.pending = nil
	b.sortSegs()
	b.emptySince = b.cfg.MonoMS()
	return b.enforce(b.cfg.Now())
}

func sameClock(a, b pendingRec) bool {
	return a.rec.BootID == b.rec.BootID && a.rec.ClockSynced == b.rec.ClockSynced
}

// Next is the chunk to send now, nil when the buffer is drained: fresh
// points → events → backlog from the oldest to the newest. Until Ack it
// returns the same chunk.
func (b *Buffer) Next() (*Chunk, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.cfg.Now()
	if err := b.expire(now); err != nil {
		return nil, err
	}
	if err := b.reapTombs(); err != nil {
		return nil, err
	}
	for {
		if err := b.popQueued(); err != nil {
			return nil, err
		}
		c := b.inflight
		if c == nil {
			return b.next(now)
		}
		if c.seg == nil {
			return c, nil
		}
		// Points that aged out while the batch waited are not sent.
		if seg := trimBefore(c.Segment, b.localCut(c.seg, now)); seg != nil {
			c.Segment = seg
			return c, nil
		}
		if err := b.dropSeg(c.seg); err != nil {
			return nil, err
		}
	}
}

// next hands out a new chunk: a disk segment that already carries a batch
// identity keeps it, anything else gets the next seq and a new batch id. The
// identity of a disk batch is stored before the first hand-out.
func (b *Buffer) next(now time.Time) (*Chunk, error) {
	c, err := b.pick(now)
	if c == nil || err != nil {
		return nil, err
	}
	if c.seg != nil && c.Segment.BatchSeq != 0 {
		c.Stream, c.Seq, c.BatchID = c.Segment.Stream, c.Segment.BatchSeq, c.Segment.BatchID
	} else {
		if c.Seq, err = b.allocSeq(); err != nil {
			return nil, err
		}
		if c.BatchID, err = newULID(now, b.cfg.Rand); err != nil {
			return nil, err
		}
		c.Stream = b.stream
	}
	b.inflight = c
	if c.seg != nil {
		if err := b.savePinned(); err != nil {
			b.inflight = nil
			return nil, err
		}
	}
	return c, nil
}

func (b *Buffer) pick(now time.Time) (*Chunk, error) {
	if c := b.freshChunk(); c != nil {
		return c, nil
	}
	for {
		if len(b.events) > 0 {
			n := min(len(b.events), MaxChunkEvents)
			c := &Chunk{Kind: ChunkEvents, Events: slices.Clone(b.events[:n]), eventsIDs: map[string]bool{}}
			for _, e := range c.Events {
				c.eventsIDs[e.ID] = true
			}
			return c, nil
		}
		if len(b.segs) == 0 {
			return b.ramBacklog(), nil
		}
		m := b.segs[0]
		seg, err := readSegment(filepath.Join(b.cfg.Dir, m.name))
		switch {
		case errors.Is(err, errCorrupt):
			if err := b.quarantine(m); err != nil {
				return nil, err
			}
			continue
		case err != nil:
			return nil, err
		}
		if seg = trimBefore(seg, b.localCut(m, now)); seg == nil {
			if err := b.dropSeg(m); err != nil {
				return nil, err
			}
			continue
		}
		return &Chunk{Kind: ChunkBacklog, Backfill: true, Segment: seg, seg: m}, nil
	}
}

// freshChunk is the newest window of records not yet sent as fresh: up to
// ChunkRecords of one boot and clock state. Older records become backlog.
func (b *Buffer) freshChunk() *Chunk {
	n := len(b.pending)
	if n == 0 || b.pending[n-1].seq <= b.freshMark {
		return nil
	}
	last := b.pending[n-1]
	i := n - 1
	for i > 0 && n-i < b.cfg.ChunkRecords && b.pending[i-1].seq > b.freshMark && sameClock(b.pending[i-1], last) {
		i--
	}
	b.freshMark = last.seq
	return b.ramChunk(ChunkFresh, b.pending[i:])
}

// ramBacklog is the oldest run of records already passed over as fresh.
func (b *Buffer) ramBacklog() *Chunk {
	n := 0
	for n < len(b.pending) && n < b.cfg.ChunkRecords && b.pending[n].seq <= b.freshMark &&
		sameClock(b.pending[n], b.pending[0]) {
		n++
	}
	if n == 0 {
		return nil
	}
	return b.ramChunk(ChunkBacklog, b.pending[:n])
}

func (b *Buffer) ramChunk(kind ChunkKind, recs []pendingRec) *Chunk {
	up := b.uptimeAt(recs[len(recs)-1].rec.MonoMS)
	return &Chunk{
		Kind: kind, Backfill: kind == ChunkBacklog, Segment: buildSegment(b.stream, recs, up),
		ram: true, ramFirst: recs[0].seq, ramLast: recs[len(recs)-1].seq,
	}
}

// Ack confirms that the portal accepted c (status accepted or duplicate): its
// data leaves the buffer. A chunk no longer in flight (dropped by age or
// quota, or replaced by Split) is ignored.
func (b *Buffer) Ack(c *Chunk) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if c == nil || c != b.inflight {
		return nil
	}
	b.inflight = nil
	switch {
	case c.ram:
		b.deletePending(c.ramFirst, c.ramLast)
	case c.Kind == ChunkEvents:
		b.events = slices.DeleteFunc(b.events, func(e Event) bool { return c.eventsIDs[e.ID] })
		return b.saveEvents()
	case c.seg != nil:
		// The segment goes durably before its identity: a crash in between
		// leaves an identity without data, never data without identity. A
		// failed removal keeps the batch in flight, so a retry keeps its
		// identity and a repeated Ack finishes the job.
		if slices.Contains(b.segs, c.seg) {
			if err := b.removeFile(c.seg.name); err != nil {
				b.inflight = c
				return err
			}
			if err := b.syncDir(); err != nil {
				b.inflight = c
				return err
			}
			b.forgetSeg(c.seg)
		}
		return b.savePinned()
	}
	return nil
}

// removeSeg deletes a segment file and forgets it.
func (b *Buffer) removeSeg(m *segMeta) error {
	if err := b.removeFile(m.name); err != nil {
		return err
	}
	return b.forget(m)
}

// dropSeg deletes a segment the buffer gave up on (age or quota).
func (b *Buffer) dropSeg(m *segMeta) error {
	b.dropped++
	return b.removeSeg(m)
}

// forget drops a segment from the index and from the batches waiting to go
// (losing data is acceptable, sending it twice is not).
func (b *Buffer) forget(m *segMeta) error {
	b.forgetSeg(m)
	pinned := false
	if b.inflight != nil && b.inflight.seg == m {
		b.inflight, pinned = nil, true
	}
	n := len(b.queued)
	b.queued = slices.DeleteFunc(b.queued, func(c *Chunk) bool { return c.seg == m })
	if pinned || len(b.queued) != n {
		return b.savePinned()
	}
	return nil
}

func (b *Buffer) forgetSeg(m *segMeta) {
	if i := slices.Index(b.segs, m); i >= 0 {
		b.segs = slices.Delete(b.segs, i, i+1)
		b.used -= m.size
	}
}

// quarantine moves a damaged segment aside and records an event.
func (b *Buffer) quarantine(m *segMeta) error {
	if err := b.forget(m); err != nil {
		return err
	}
	return b.quarantineFile(m.name, "corrupted")
}

func (b *Buffer) quarantineFile(name, reason string) error {
	qdir := filepath.Join(b.cfg.Dir, quarantineDir)
	if err := os.MkdirAll(qdir, 0o700); err != nil {
		return fmt.Errorf("карантин: %w", err)
	}
	if err := os.Rename(filepath.Join(b.cfg.Dir, name), filepath.Join(qdir, name)); err != nil {
		return fmt.Errorf("карантин: %w", err)
	}
	if err := b.pruneQuarantine(); err != nil {
		return err
	}
	return b.addEvent(Event{
		Kind: "buffer.quarantine", Severity: "warning", At: b.cfg.Now(),
		Params: map[string]any{"segment": name, "reason": reason},
	})
}
