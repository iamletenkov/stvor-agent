// Package buffer is the telemetry buffer of the agent (Stvor spec
// 2026-10-21-robot-telemetry §6, Т8): collector ticks stay in a RAM ring and
// go to the portal straight from it while the portal answers; offline they
// reach the disk as gzip segments once per FlushEvery (tmp → fsync → rename).
// The disk usage is held under a quota: raw data older than a day is rolled
// up to 1 min, then 5 min (levels → min/avg/max, cumulative counters → the
// last value), then the oldest data is deleted; the last 24 h stay raw and
// agent events are never rolled up and go last. Points older than 30 days
// are never sent.
//
// The drain order follows the contract (retry.order): fresh points → events →
// backlog from the oldest to the newest. A batch keeps its identity (stream,
// seq, batch_id) until Ack — see inflight.go. The age of data without a synced
// clock is the buffer uptime since its write, not its wall clock.
//
// Known limits: the RAM ring is bounded by records, not samples; the flush
// timer runs only while the ring is not empty (online every tick is acked and
// nothing reaches the disk), there is no separate «N failed uploads» trigger.
package buffer

import (
	"bytes"
	"cmp"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/iamletenkov/stvor-agent/internal/atomicfile"
)

// Kind tells how a series is rolled up.
type Kind string

// Series kinds: a gauge is a level (rolled to min/avg/max), a counter is a raw
// cumulative value (rolled to the last value of the window).
const (
	Gauge   Kind = "gauge"
	Counter Kind = "counter"
)

// Label is one label of a series.
type Label struct {
	K string `json:"k"`
	V string `json:"v"`
}

// Sample is one value of one series at a tick.
type Sample struct {
	Name   string
	Kind   Kind
	Labels []Label
	Value  float64
}

// Record is one collector tick.
type Record struct {
	At          time.Time // device wall clock
	MonoMS      int64     // CLOCK_BOOTTIME, ms
	BootID      string
	ClockSynced bool
	Samples     []Sample
}

// Agg is a rolled-up gauge window.
type Agg struct {
	Min, Avg, Max float64
	N             int // raw points in the window
}

// Point is a value at Off ms from the segment start (monotonic clock). A
// rolled-up gauge carries Agg and V = Agg.Avg.
type Point struct {
	Off int64
	V   float64
	Agg *Agg
}

// MarshalJSON writes a raw point as [off, v] and a rolled gauge as
// [off, min, avg, max, n].
func (p Point) MarshalJSON() ([]byte, error) {
	if p.Agg == nil {
		return json.Marshal([2]float64{float64(p.Off), p.V})
	}
	return json.Marshal([5]float64{float64(p.Off), p.Agg.Min, p.Agg.Avg, p.Agg.Max, float64(p.Agg.N)})
}

// UnmarshalJSON reads what MarshalJSON writes.
func (p *Point) UnmarshalJSON(data []byte) error {
	var a []float64
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	switch len(a) {
	case 2:
		*p = Point{Off: int64(a[0]), V: a[1]}
	case 5:
		agg := &Agg{Min: a[1], Avg: a[2], Max: a[3], N: int(a[4])}
		*p = Point{Off: int64(a[0]), V: agg.Avg, Agg: agg}
	default:
		return fmt.Errorf("точка из %d чисел", len(a))
	}
	return nil
}

// Series is the points of one metric with one label set.
type Series struct {
	Name   string  `json:"name"`
	Kind   Kind    `json:"kind"`
	Labels []Label `json:"labels,omitempty"`
	Points []Point `json:"points"`
}

// Segment is a run of records of one boot and one clock state: the unit of
// the disk and of a backlog chunk.
type Segment struct {
	Version     int       `json:"version"`
	Stream      string    `json:"stream"`
	BootID      string    `json:"boot_id"`
	ClockSynced bool      `json:"clock_synced"`
	SeqFirst    uint64    `json:"seq_first"`
	SeqLast     uint64    `json:"seq_last"`
	StartAt     time.Time `json:"start_at"`
	EndAt       time.Time `json:"end_at"`
	StartMonoMS int64     `json:"start_mono_ms"`
	RollupS     int       `json:"rollup_s,omitempty"`
	// UptimeMS is the buffer uptime at the segment end: the age basis of a
	// segment without a synced clock, whose wall clock may be 1970 or 2018.
	UptimeMS int64 `json:"uptime_ms"`
	// BatchSeq and BatchID are the batch identity once the segment was handed
	// out: a retry, also after a restart, goes under the same identity.
	BatchSeq uint64   `json:"batch_seq,omitempty"`
	BatchID  string   `json:"batch_id,omitempty"`
	Series   []Series `json:"series"`
}

const (
	segmentVersion  = 1
	segPrefix       = "seg-"
	segSuffix       = ".json.gz"
	maxSegmentBytes = 64 << 20 // on disk
	maxInflated     = 64 << 20
)

// segMeta is what the buffer keeps about a segment file without reading it.
type segMeta struct {
	name              string
	seqFirst, seqLast uint64
	start, end        time.Time
	rollup            int
	size              int64
	up                int64 // buffer uptime at the end, ms
	synced            bool
}

func segName(s *Segment) string {
	synced := 0
	if s.ClockSynced {
		synced = 1
	}
	return fmt.Sprintf("%s%020d-%020d-%d-%d-u%d-r%d-c%d%s", segPrefix, s.SeqFirst, s.SeqLast,
		max(s.StartAt.UnixMilli(), 0), max(s.EndAt.UnixMilli(), 0), max(s.UptimeMS, 0), s.RollupS, synced, segSuffix)
}

// parseSegName reads the metadata a segment file name carries.
func parseSegName(name string) (*segMeta, error) {
	body, ok := strings.CutPrefix(name, segPrefix)
	if ok {
		body, ok = strings.CutSuffix(body, segSuffix)
	}
	parts := strings.Split(body, "-")
	if !ok || len(parts) != 7 {
		return nil, fmt.Errorf("имя сегмента %q", name)
	}
	var n [7]int64
	for i, p := range parts {
		if prefix := [7]string{4: "u", 5: "r", 6: "c"}[i]; prefix != "" {
			if p, ok = strings.CutPrefix(p, prefix); !ok {
				return nil, fmt.Errorf("имя сегмента %q", name)
			}
		}
		v, err := strconv.ParseInt(p, 10, 64)
		if err != nil || v < 0 {
			return nil, fmt.Errorf("имя сегмента %q", name)
		}
		n[i] = v
	}
	if n[0] == 0 || n[1] < n[0] || !validRollup(int(n[5])) || n[6] > 1 {
		return nil, fmt.Errorf("имя сегмента %q", name)
	}
	return &segMeta{
		name: name, seqFirst: uint64(n[0]), seqLast: uint64(n[1]),
		start: time.UnixMilli(n[2]).UTC(), end: time.UnixMilli(n[3]).UTC(),
		up: n[4], rollup: int(n[5]), synced: n[6] == 1,
	}, nil
}

func validRollup(s int) bool { return s == 0 || s == 60 || s == 300 }

func encodeSegment(s *Segment) ([]byte, error) {
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// writeSegment stores s in dir atomically (temp file → fsync → rename).
func writeSegment(dir string, s *Segment) (*segMeta, error) {
	data, err := encodeSegment(s)
	if err != nil {
		return nil, fmt.Errorf("сегмент: %w", err)
	}
	name := segName(s)
	if err := (atomicfile.OS{}).WriteAtomic(filepath.Join(dir, name), data, 0o600); err != nil {
		return nil, err
	}
	return &segMeta{
		name: name, seqFirst: s.SeqFirst, seqLast: s.SeqLast, start: s.StartAt, end: s.EndAt,
		rollup: s.RollupS, size: int64(len(data)), up: s.UptimeMS, synced: s.ClockSynced,
	}, nil
}

func (b *Buffer) writeSeg(s *Segment) (*segMeta, error) {
	if err := b.faultAt("write", segName(s)); err != nil {
		return nil, fmt.Errorf("сегмент: %w", err)
	}
	return writeSegment(b.cfg.Dir, s)
}

var errCorrupt = errors.New("сегмент повреждён")

// readSegment reads and checks a segment file; any damage is errCorrupt.
func readSegment(path string) (*Segment, error) {
	data, _, err := (atomicfile.OS{}).ReadNoFollow(path, maxSegmentBytes)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %w", errCorrupt, err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errCorrupt, err)
	}
	raw, err := io.ReadAll(io.LimitReader(zr, maxInflated+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errCorrupt, err)
	}
	if len(raw) > maxInflated {
		return nil, fmt.Errorf("%w: больше %d байт", errCorrupt, maxInflated)
	}
	var s Segment
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("%w: %w", errCorrupt, err)
	}
	if err := s.validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", errCorrupt, err)
	}
	return &s, nil
}

func (s *Segment) validate() error {
	switch {
	case s.Version != segmentVersion:
		return fmt.Errorf("версия %d", s.Version)
	case s.SeqFirst == 0 || s.SeqLast < s.SeqFirst || s.UptimeMS < 0:
		return fmt.Errorf("seq %d..%d", s.SeqFirst, s.SeqLast)
	case !validRollup(s.RollupS):
		return fmt.Errorf("rollup_s %d", s.RollupS)
	}
	for _, ser := range s.Series {
		if ser.Name == "" || (ser.Kind != Gauge && ser.Kind != Counter) {
			return fmt.Errorf("ряд %q вида %q", ser.Name, ser.Kind)
		}
		var prev int64
		for _, p := range ser.Points {
			if p.Off < prev || math.IsNaN(p.V) {
				return fmt.Errorf("ряд %q: точка %d", ser.Name, p.Off)
			}
			prev = p.Off
		}
	}
	return nil
}

// buildSegment turns records of one boot and clock state into a segment; up
// is the buffer uptime at the last record.
func buildSegment(stream string, recs []pendingRec, up int64) *Segment {
	first, last := recs[0], recs[len(recs)-1]
	s := &Segment{
		Version: segmentVersion, Stream: stream,
		BootID: first.rec.BootID, ClockSynced: first.rec.ClockSynced,
		SeqFirst: first.seq, SeqLast: last.seq,
		StartAt: first.rec.At, EndAt: last.rec.At, StartMonoMS: first.rec.MonoMS, UptimeMS: up,
	}
	index := map[string]int{}
	for _, r := range recs {
		off := max(r.rec.MonoMS-first.rec.MonoMS, 0)
		for _, smp := range r.rec.Samples {
			key := seriesKey(smp.Name, smp.Labels)
			i, ok := index[key]
			if !ok {
				i = len(s.Series)
				index[key] = i
				s.Series = append(s.Series, Series{Name: smp.Name, Kind: smp.Kind, Labels: smp.Labels})
			}
			s.Series[i].Points = append(s.Series[i].Points, Point{Off: off, V: smp.Value})
		}
	}
	return s
}

func seriesKey(name string, labels []Label) string {
	var b strings.Builder
	b.WriteString(name)
	for _, l := range labels {
		b.WriteByte(0)
		b.WriteString(l.K)
		b.WriteByte('=')
		b.WriteString(l.V)
	}
	return b.String()
}

// trimBefore drops the points older than cut; nil when nothing is left.
func trimBefore(s *Segment, cut time.Time) *Segment {
	if !s.StartAt.Before(cut) {
		return s
	}
	cutOff := cut.UnixMilli() - s.StartAt.UnixMilli()
	out := *s
	out.Series = nil
	for _, ser := range s.Series {
		var pts []Point
		for _, p := range ser.Points {
			if p.Off >= cutOff {
				pts = append(pts, p)
			}
		}
		if len(pts) > 0 {
			ser.Points = pts
			out.Series = append(out.Series, ser)
		}
	}
	if len(out.Series) == 0 {
		return nil
	}
	return &out
}

// splitSegment cuts s in two halves by time: the first half of the distinct
// point offsets and the rest. The seq ranges of the halves are disjoint
// sub-ranges of s. ErrUnsplittable when s holds a single record or instant.
func splitSegment(s *Segment) (*Segment, *Segment, error) {
	seen := map[int64]bool{}
	var offs []int64
	for _, ser := range s.Series {
		for _, p := range ser.Points {
			if !seen[p.Off] {
				seen[p.Off] = true
				offs = append(offs, p.Off)
			}
		}
	}
	d := len(offs)
	if d < 2 || s.SeqLast == s.SeqFirst {
		return nil, nil, ErrUnsplittable
	}
	slices.Sort(offs)
	k := d / 2
	mid := offs[k]
	a, b := *s, *s
	a.Series, b.Series = nil, nil
	for _, ser := range s.Series {
		i, _ := slices.BinarySearchFunc(ser.Points, mid, func(p Point, t int64) int { return cmp.Compare(p.Off, t) })
		if i > 0 {
			sa := ser
			sa.Points = slices.Clone(ser.Points[:i])
			a.Series = append(a.Series, sa)
		}
		if i < len(ser.Points) {
			sb := ser
			sb.Points = make([]Point, 0, len(ser.Points)-i)
			for _, p := range ser.Points[i:] {
				p.Off -= mid
				sb.Points = append(sb.Points, p)
			}
			b.Series = append(b.Series, sb)
		}
	}
	a.SeqLast = s.SeqFirst + (s.SeqLast-s.SeqFirst)*uint64(k)/uint64(d) //nolint:gosec // k, d > 0
	b.SeqFirst = a.SeqLast + 1
	a.EndAt = s.StartAt.Add(time.Duration(offs[k-1]) * time.Millisecond)
	b.StartAt = s.StartAt.Add(time.Duration(mid) * time.Millisecond)
	if b.StartAt.After(s.EndAt) {
		b.StartAt = s.EndAt
	}
	b.StartMonoMS = s.StartMonoMS + mid
	a.UptimeMS = max(s.UptimeMS-(offs[d-1]-offs[k-1]), 0)
	return &a, &b, nil
}
