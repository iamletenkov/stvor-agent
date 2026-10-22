package buffer

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/iamletenkov/stvor-agent/internal/atomicfile"
)

// The portal drops a batch with seq ≤ last_seq of its stream as a duplicate,
// and VictoriaMetrics does not deduplicate: data sent once more under a new
// seq is stored twice. So a batch identity (stream, seq, batch_id) stays with
// its data until Ack — through a flush, a roll-up, a Split and a restart — and
// a batch with a fixed identity goes before anything new: a fresh batch with
// a higher seq would move the portal cursor past it.
//
// inflightFile lists the disk batches with a fixed identity in send order. It
// is written before the data it names (a crash leaves an identity without
// data, never data without identity) and rewritten after the data of an acked
// batch is durably gone.
const (
	inflightFile     = "inflight.json"
	maxInflightBytes = 1 << 20
)

// ErrUnsplittable is returned by Split for a chunk of one record or event.
var ErrUnsplittable = errors.New("пачку не разделить: одна запись")

type pinnedEntry struct {
	Segment  string    `json:"segment"`
	Stream   string    `json:"stream"`
	Seq      uint64    `json:"seq"`
	BatchID  string    `json:"batch_id"`
	Kind     ChunkKind `json:"kind"`
	Backfill bool      `json:"backfill"`
	// Replaces is the segment a Split cut into halves: a crash before its
	// removal must not leave the same points under two identities.
	Replaces string `json:"replaces,omitempty"`
}

type pinnedState struct {
	Batches []pinnedEntry `json:"batches"`
	// Tombstones are originals of a Split (or halves of an interrupted one)
	// whose removal failed: they stay on disk until a removal succeeds and
	// are never handed out again.
	Tombstones []string `json:"tombstones,omitempty"`
}

// savePinned writes the identities of the disk batches in flight and queued;
// with none the file goes.
func (b *Buffer) savePinned() error {
	var st pinnedState
	for _, c := range append([]*Chunk{b.inflight}, b.queued...) {
		if c == nil || c.seg == nil {
			continue
		}
		st.Batches = append(st.Batches, pinnedEntry{
			Segment: c.seg.name, Stream: c.Stream, Seq: c.Seq, BatchID: c.BatchID,
			Kind: c.Kind, Backfill: c.Backfill, Replaces: c.replaces,
		})
	}
	st.Tombstones = b.tombs
	if len(st.Batches) == 0 && len(st.Tombstones) == 0 {
		return b.removeFile(inflightFile)
	}
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if err := b.faultAt("write", inflightFile); err != nil {
		return fmt.Errorf("пачки в полёте: %w", err)
	}
	if err := (atomicfile.OS{}).WriteAtomic(filepath.Join(b.cfg.Dir, inflightFile), data, 0o600); err != nil {
		return fmt.Errorf("пачки в полёте: %w", err)
	}
	return nil
}

// loadPinned restores the batches with a fixed identity; they go out first.
// An entry without its segment is dropped. An interrupted Split is settled:
// with all halves on disk the original goes, with some missing the halves
// go and the original stays (it is sent again under a new identity — the
// portal rejected it with 413, so nothing of it is stored). Tombstoned
// segments are taken out of the index and their removal is retried.
func (b *Buffer) loadPinned() error {
	data, _, err := (atomicfile.OS{}).ReadNoFollow(filepath.Join(b.cfg.Dir, inflightFile), maxInflightBytes)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	var st pinnedState
	if err != nil || json.Unmarshal(data, &st) != nil {
		return b.removeFile(inflightFile)
	}
	byName := map[string]*segMeta{}
	for _, m := range b.segs {
		byName[m.name] = m
	}
	// Only names of indexed segments are acted on: the file never makes the
	// buffer touch anything else.
	for _, name := range st.Tombstones {
		if m := byName[name]; m != nil && !slices.Contains(b.tombs, name) {
			b.forgetSeg(m)
			delete(byName, name)
			b.tombs = append(b.tombs, name)
		}
	}
	halves := map[string][2]int{} // original → halves named, halves on disk
	for _, e := range st.Batches {
		if e.Replaces != "" {
			h := halves[e.Replaces]
			h[0]++
			if byName[e.Segment] != nil {
				h[1]++
			}
			halves[e.Replaces] = h
		}
	}
	incomplete := func(e pinnedEntry) bool {
		h := halves[e.Replaces]
		return e.Replaces != "" && byName[e.Replaces] != nil && h[1] < h[0]
	}
	pinned := map[*segMeta]bool{}
	for _, e := range st.Batches {
		m := byName[e.Segment]
		if m == nil || pinned[m] || e.Seq == 0 || len(e.BatchID) != ulidLen || len(e.Stream) != ulidLen {
			continue
		}
		if incomplete(e) {
			b.dropFile(m)
			continue
		}
		if e.Kind != ChunkFresh {
			e.Kind = ChunkBacklog
		}
		pinned[m] = true
		b.queued = append(b.queued, &Chunk{
			Kind: e.Kind, Backfill: e.Backfill, Stream: e.Stream, Seq: e.Seq, BatchID: e.BatchID,
			seg: m, replaces: e.Replaces,
		})
	}
	for _, c := range b.queued {
		if orig := byName[c.replaces]; orig != nil && !pinned[orig] {
			b.dropFile(orig)
			delete(byName, c.replaces)
		}
	}
	return b.savePinned()
}

// dropFile takes a segment out of the index and removes its file; a failed
// removal leaves a tombstone, so the segment is never handed out again.
func (b *Buffer) dropFile(m *segMeta) {
	b.forgetSeg(m)
	if err := b.removeFile(m.name); err != nil {
		b.tombs = append(b.tombs, m.name)
	}
}

// reapTombs retries the removal of tombstoned segments; the sidecar forgets
// them once they are durably gone.
func (b *Buffer) reapTombs() error {
	if len(b.tombs) == 0 {
		return nil
	}
	left := slices.DeleteFunc(slices.Clone(b.tombs), func(name string) bool { return b.removeFile(name) == nil })
	if len(left) == len(b.tombs) {
		return nil
	}
	if err := b.syncDir(); err != nil {
		return err
	}
	b.tombs = left
	return b.savePinned()
}

// popQueued makes the first queued batch the one in flight, reading its
// segment.
func (b *Buffer) popQueued() error {
	for b.inflight == nil && len(b.queued) > 0 {
		c := b.queued[0]
		if c.Segment == nil && c.seg != nil {
			seg, err := readSegment(filepath.Join(b.cfg.Dir, c.seg.name))
			switch {
			case errors.Is(err, errCorrupt):
				if err := b.quarantine(c.seg); err != nil {
					return err
				}
				continue
			case errors.Is(err, os.ErrNotExist):
				if err := b.forget(c.seg); err != nil {
					return err
				}
				continue
			case err != nil:
				return err
			}
			c.Segment = seg
		}
		b.queued = b.queued[1:]
		b.inflight = c
	}
	return nil
}

func (b *Buffer) isPinned(m *segMeta) bool {
	if b.inflight != nil && b.inflight.seg == m {
		return true
	}
	return slices.ContainsFunc(b.queued, func(c *Chunk) bool { return c.seg == m })
}

// pinRAM moves the RAM batch in flight to its own segment that carries its
// identity; the chunk stays the same, so a late Ack is honoured.
func (b *Buffer) pinRAM(c *Chunk) error {
	seg := *c.Segment
	seg.Stream, seg.BatchSeq, seg.BatchID = c.Stream, c.Seq, c.BatchID
	p := &segMeta{name: segName(&seg)}
	c.seg, c.ram = p, false
	if err := b.writePinned([]*Segment{&seg}, []*segMeta{p}); err != nil {
		c.seg, c.ram = nil, true
		_ = b.savePinned()
		return err
	}
	b.deletePending(c.ramFirst, c.ramLast)
	return nil
}

func (b *Buffer) deletePending(first, last uint64) {
	b.pending = slices.DeleteFunc(b.pending, func(p pendingRec) bool { return p.seq >= first && p.seq <= last })
	if len(b.pending) == 0 {
		b.emptySince = b.cfg.MonoMS()
	}
}

// writePinned stores the identities (already bound to the chunks) and then
// the segments; metas are the placeholders the chunks point to.
func (b *Buffer) writePinned(segs []*Segment, metas []*segMeta) error {
	if err := b.savePinned(); err != nil {
		return err
	}
	for i, s := range segs {
		m, err := b.writeSeg(s)
		if err != nil {
			for _, p := range metas[:i] {
				b.forgetSeg(p)
				_ = b.removeFile(p.name)
			}
			return err
		}
		*metas[i] = *m
		b.segs = append(b.segs, metas[i])
		b.used += m.size
	}
	b.sortSegs()
	return nil
}

// Split answers a 413 for the chunk in flight: it is replaced by two halves,
// each with its own new seq and batch id. The halves go after the batches
// already queued (those have lower seqs and must reach the portal first). Halves of points are stored on disk with their
// identities, so a flush or a restart does not change them; halves of events
// stay in memory (events are idempotent by event_id). A chunk of one record
// or one event gives ErrUnsplittable.
func (b *Buffer) Split(c *Chunk) (*Chunk, *Chunk, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if c == nil || c != b.inflight {
		return nil, nil, errors.New("буфер: делить можно только пачку в полёте")
	}
	now := b.cfg.Now()
	if c.Kind == ChunkEvents {
		if len(c.Events) < 2 {
			return nil, nil, ErrUnsplittable
		}
		k := len(c.Events) / 2
		ha, hb := eventsChunk(c.Events[:k]), eventsChunk(c.Events[k:])
		if err := b.identify(now, ha, hb); err != nil {
			return nil, nil, err
		}
		b.inflight, b.queued = nil, append(slices.Clone(b.queued), ha, hb)
		return ha, hb, b.popQueued()
	}
	sa, sb, err := splitSegment(c.Segment)
	if err != nil {
		return nil, nil, err
	}
	ha := &Chunk{Kind: c.Kind, Backfill: c.Backfill, Segment: sa, seg: &segMeta{}}
	hb := &Chunk{Kind: c.Kind, Backfill: c.Backfill, Segment: sb, seg: &segMeta{}}
	if err := b.identify(now, ha, hb); err != nil {
		return nil, nil, err
	}
	for _, h := range []*Chunk{ha, hb} {
		if c.seg != nil {
			h.replaces = c.seg.name
		}
		h.Segment.Stream, h.Segment.BatchSeq, h.Segment.BatchID = h.Stream, h.Seq, h.BatchID
		h.seg.name = segName(h.Segment)
	}
	oldQueued := b.queued
	b.inflight, b.queued = nil, append(slices.Clone(b.queued), ha, hb)
	if err := b.writePinned([]*Segment{sa, sb}, []*segMeta{ha.seg, hb.seg}); err != nil {
		b.inflight, b.queued = c, oldQueued
		_ = b.savePinned()
		return nil, nil, err
	}
	// The original goes once the halves are durable. It leaves the index
	// first: if its removal fails, a tombstone keeps it from ever being
	// handed out again under a new identity while the halves carry its data.
	switch {
	case c.ram:
		b.deletePending(c.ramFirst, c.ramLast)
	case c.seg != nil && slices.Contains(b.segs, c.seg):
		b.forgetSeg(c.seg)
		if err := b.removeFile(c.seg.name); err != nil {
			b.tombs = append(b.tombs, c.seg.name)
			// The halves still name the original as replaced: a crash
			// before this write settles it at Open all the same.
			_ = b.savePinned()
			break
		}
		if err := b.syncDir(); err != nil {
			return nil, nil, err
		}
	}
	return ha, hb, b.popQueued()
}

func eventsChunk(events []Event) *Chunk {
	c := &Chunk{Kind: ChunkEvents, Events: slices.Clone(events), eventsIDs: map[string]bool{}}
	for _, e := range c.Events {
		c.eventsIDs[e.ID] = true
	}
	return c
}

// identify gives each chunk the next seq of the stream and a new batch id.
func (b *Buffer) identify(now time.Time, cs ...*Chunk) error {
	for _, c := range cs {
		seq, err := b.allocSeq()
		if err != nil {
			return err
		}
		id, err := newULID(now, b.cfg.Rand)
		if err != nil {
			return err
		}
		c.Stream, c.Seq, c.BatchID = b.stream, seq, id
	}
	return nil
}

// uptimeAt is the buffer uptime at the CLOCK_BOOTTIME mono: it grows across
// restarts and reboots and does not jump with the wall clock.
func (b *Buffer) uptimeAt(mono int64) int64 { return b.upBase + max(mono-b.monoOpen, 0) }

func (b *Buffer) uptime() int64 { return b.uptimeAt(b.cfg.MonoMS()) }

// ageEnd is the end of m on the current wall clock: its own end when the
// clock was synced, else now minus the uptime since it was written (a device
// without an RTC battery writes 1970 or 2018 and then jumps on NTP sync).
func (b *Buffer) ageEnd(m *segMeta, now time.Time) time.Time {
	if m.synced {
		return m.end
	}
	return now.Add(-time.Duration(max(b.uptime()-m.up, 0)) * time.Millisecond)
}

func (b *Buffer) ageStart(m *segMeta, now time.Time) time.Time {
	return b.ageEnd(m, now).Add(-m.end.Sub(m.start))
}

// localCut is the autonomy cutoff on the point clock of m.
func (b *Buffer) localCut(m *segMeta, now time.Time) time.Time {
	return now.Add(-b.cfg.Autonomy).Add(m.end.Sub(b.ageEnd(m, now)))
}

const (
	quarantineDir      = "quarantine"
	maxQuarantineFiles = 16
)

type quarantined struct {
	name string
	mt   time.Time
	size int64
}

// listQuarantine is the quarantine, the newest first.
func (b *Buffer) listQuarantine() ([]quarantined, error) {
	ents, err := os.ReadDir(filepath.Join(b.cfg.Dir, quarantineDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("карантин: %w", err)
	}
	var out []quarantined
	for _, e := range ents {
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, quarantined{name: e.Name(), mt: info.ModTime(), size: info.Size()})
	}
	slices.SortFunc(out, func(x, y quarantined) int { return y.mt.Compare(x.mt) })
	return out, nil
}

func (b *Buffer) removeQuarantined(name string) error {
	err := os.RemoveAll(filepath.Join(b.cfg.Dir, quarantineDir, name))
	if err != nil {
		return fmt.Errorf("карантин: %w", err)
	}
	return nil
}

// pruneQuarantine keeps the last maxQuarantineFiles files not older than
// MaxAge and counts them toward the quota.
func (b *Buffer) pruneQuarantine() error {
	files, err := b.listQuarantine()
	if err != nil {
		return err
	}
	cut := b.cfg.Now().Add(-MaxAge)
	b.qUsed = 0
	for i, f := range files {
		if i >= maxQuarantineFiles || f.mt.Before(cut) {
			if err := b.removeQuarantined(f.name); err != nil {
				return err
			}
			continue
		}
		b.qUsed += f.size
	}
	return nil
}

// shedQuarantine deletes quarantine files, the oldest first, while over the
// quota: they go before any data.
func (b *Buffer) shedQuarantine() error {
	if !b.over() || b.qUsed == 0 {
		return nil
	}
	files, err := b.listQuarantine()
	if err != nil {
		return err
	}
	for i := len(files) - 1; i >= 0 && b.over(); i-- {
		if err := b.removeQuarantined(files[i].name); err != nil {
			return err
		}
		b.qUsed -= files[i].size
	}
	return nil
}

func (b *Buffer) syncDir() error {
	if err := b.faultAt("sync", b.cfg.Dir); err != nil {
		return fmt.Errorf("fsync каталога буфера: %w", err)
	}
	return syncDir(b.cfg.Dir)
}

func syncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // the buffer directory from the config
	if err != nil {
		return fmt.Errorf("fsync каталога буфера: %w", err)
	}
	if err := d.Sync(); err != nil {
		_ = d.Close()
		return fmt.Errorf("fsync каталога буфера: %w", err)
	}
	return d.Close()
}
