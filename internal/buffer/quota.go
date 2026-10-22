package buffer

import (
	"errors"
	"path/filepath"
	"slices"
	"time"
)

// SetLimits applies the quota and the autonomy from the portal config (the
// caller checks the contract ranges); zero keeps the current value.
func (b *Buffer) SetLimits(quotaBytes int64, autonomy time.Duration) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if quotaBytes > 0 {
		b.cfg.QuotaBytes = quotaBytes
	}
	if autonomy > 0 {
		b.cfg.Autonomy = min(autonomy, MaxAge)
	}
	return b.enforce(b.cfg.Now())
}

// Stats is the current buffer state.
func (b *Buffer) Stats() Stats {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := Stats{
		UsedBytes: b.used + b.evSize + b.qUsed, QuotaBytes: b.cfg.QuotaBytes,
		Segments: len(b.segs), Events: len(b.events), Pending: len(b.pending),
		RolledUp: b.rolledUp, Dropped: b.dropped,
	}
	now := b.cfg.Now()
	for _, m := range b.segs {
		if start := b.ageStart(m, now); st.OldestAt.IsZero() || start.Before(st.OldestAt) {
			st.OldestAt = start
		}
	}
	if st.OldestAt.IsZero() && len(b.pending) > 0 {
		st.OldestAt = b.pending[0].rec.At
	}
	return st
}

// expire drops data beyond the autonomy and events older than MaxAge (an
// event without a synced clock has no trustworthy age and stays until the
// quota takes it).
func (b *Buffer) expire(now time.Time) error {
	cut := now.Add(-b.cfg.Autonomy)
	for _, m := range slices.Clone(b.segs) {
		if b.ageEnd(m, now).Before(cut) {
			if err := b.dropSeg(m); err != nil {
				return err
			}
		}
	}
	evCut := now.Add(-MaxAge)
	n := len(b.events)
	b.events = slices.DeleteFunc(b.events, func(e Event) bool { return e.ClockSynced && e.At.Before(evCut) })
	if len(b.events) != n {
		b.inflightEventsGone()
		return b.saveEvents()
	}
	return nil
}

func (b *Buffer) inflightEventsGone() {
	if b.inflight != nil && b.inflight.Kind == ChunkEvents {
		b.inflight = nil
	}
}

// enforce applies the age limits, rolls up raw data older than FullFor and
// holds the quota: quarantine goes first, then roll up to 1 min, then to
// 5 min (oldest first, the last FreshFull stays raw, the batch in flight is
// never rolled), then delete the oldest segments, the last day last, and only
// then events.
func (b *Buffer) enforce(now time.Time) error {
	if err := b.expire(now); err != nil {
		return err
	}
	if err := b.shedQuarantine(); err != nil {
		return err
	}
	fullCut, freshCut := now.Add(-b.cfg.FullFor), now.Add(-FreshFull)
	if err := b.rollAll(now, 0, 60, fullCut, false); err != nil {
		return err
	}
	for _, step := range [][2]int{{0, 60}, {60, 300}} {
		if err := b.rollAll(now, step[0], step[1], freshCut, true); err != nil {
			return err
		}
	}
	for _, fresh := range []bool{false, true} {
		for _, m := range slices.Clone(b.segs) {
			if !b.over() {
				return nil
			}
			if fresh || b.ageEnd(m, now).Before(freshCut) {
				if err := b.dropSeg(m); err != nil {
					return err
				}
			}
		}
	}
	if !b.over() || len(b.events) == 0 {
		return nil
	}
	b.inflightEventsGone()
	for len(b.events) > 0 && b.over() {
		b.events = b.events[1:]
		if err := b.saveEvents(); err != nil {
			return err
		}
	}
	return nil
}

func (b *Buffer) over() bool { return b.used+b.evSize+b.qUsed > b.cfg.QuotaBytes }

// rollAll rolls segments at level from up to level to that ended before cut,
// oldest first; onPressure stops as soon as the quota holds. A batch in
// flight or queued keeps its data as sent: rolling it would make a retry
// carry other points under the same identity.
func (b *Buffer) rollAll(now time.Time, from, to int, cut time.Time, onPressure bool) error {
	for _, m := range slices.Clone(b.segs) {
		if onPressure && !b.over() {
			return nil
		}
		if m.rollup != from || !b.ageEnd(m, now).Before(cut) || b.isPinned(m) {
			continue
		}
		if err := b.roll(m, to); err != nil {
			return err
		}
	}
	return nil
}

// roll replaces a segment with its roll-up: the rolled copy is written first,
// so a crash leaves both and Open keeps the rolled one.
func (b *Buffer) roll(m *segMeta, to int) error {
	seg, err := readSegment(filepath.Join(b.cfg.Dir, m.name))
	switch {
	case errors.Is(err, errCorrupt):
		return b.quarantine(m)
	case err != nil:
		return err
	}
	nm, err := b.writeSeg(Rollup(seg, to))
	if err != nil {
		return err
	}
	if nm.name != m.name {
		if err := b.removeFile(m.name); err != nil {
			return err
		}
	}
	i := slices.Index(b.segs, m)
	b.segs[i] = nm
	b.used += nm.size - m.size
	b.rolledUp++
	return nil
}
