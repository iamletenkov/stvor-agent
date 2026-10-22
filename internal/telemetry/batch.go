package telemetry

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/iamletenkov/stvor-agent/internal/buffer"
)

// wireBatch is the body of POST /metrics (contract batch.fields).
type wireBatch struct {
	Schema        int          `json:"schema"`
	AgentVersion  string       `json:"agent_version"`
	Capabilities  []string     `json:"capabilities"`
	BatchID       string       `json:"batch_id"`
	Stream        string       `json:"stream"`
	Seq           uint64       `json:"seq"`
	BootID        string       `json:"boot_id"`
	MachineIDHash string       `json:"machine_id_hash,omitempty"` // absent rather than invalid
	SentAt        string       `json:"sent_at"`
	SentMonoMS    int64        `json:"sent_mono_ms"`
	StartedAt     string       `json:"started_at"`
	StartMonoMS   int64        `json:"start_mono_ms"`
	ClockSynced   bool         `json:"clock_synced"`
	Backfill      bool         `json:"backfill,omitempty"`
	FailedUnits   []string     `json:"failed_units,omitempty"`
	Buffer        *wireBuffer  `json:"buffer,omitempty"`
	Series        []wireSeries `json:"series"`
}

type wireBuffer struct {
	UsedBytes  int64  `json:"used_bytes"`
	QuotaBytes int64  `json:"quota_bytes"`
	OldestAt   string `json:"oldest_at,omitempty"`
}

type wireSeries struct {
	Name    string            `json:"name"`
	Labels  map[string]string `json:"labels"`
	RollupS int               `json:"rollup_s,omitempty"`
	Points  []wirePoint       `json:"points"`
}

// wirePoint is [offset_ms, value] or, for a rolled-up gauge,
// [offset_ms, {"min", "avg", "max"}].
type wirePoint struct {
	off int64
	v   float64
	agg *buffer.Agg
}

func (p wirePoint) MarshalJSON() ([]byte, error) {
	if p.agg == nil {
		return json.Marshal([2]any{p.off, p.v})
	}
	return json.Marshal([2]any{p.off, map[string]float64{"min": p.agg.Min, "avg": p.agg.Avg, "max": p.agg.Max}})
}

const (
	maxAgentVersion = 64
	maxCapabilities = 32
	maxUnitName     = 128
)

// metricsBody builds the batch of a points chunk with sent_at of this very
// attempt. over is set when the batch breaks a count limit of the contract.
func (s *Sender) metricsBody(c *buffer.Chunk, now time.Time) (raw []byte, over string, err error) {
	seg := c.Segment
	if seg == nil {
		return nil, "", fmt.Errorf("пачка %d без точек", c.Seq)
	}
	meta := s.opt.Meta()
	b := wireBatch{
		Schema: s.k.Schema, AgentVersion: cut(meta.AgentVersion, maxAgentVersion),
		Capabilities: s.capabilities(meta.Capabilities),
		BatchID:      c.BatchID, Stream: c.Stream, Seq: c.Seq, BootID: seg.BootID,
		MachineIDHash: meta.MachineIDHash,
		SentAt:        rfc3339(now), SentMonoMS: s.opt.MonoMS(),
		StartedAt: rfc3339(seg.StartAt), StartMonoMS: seg.StartMonoMS, ClockSynced: seg.ClockSynced,
		Backfill: c.Backfill,
		Series:   []wireSeries{},
	}
	if !reSHA256.MatchString(b.MachineIDHash) {
		b.MachineIDHash = ""
	}
	if c.Kind == buffer.ChunkFresh {
		b.FailedUnits = s.failedUnits(meta.FailedUnits)
	}
	st := s.opt.Queue.Stats()
	b.Buffer = &wireBuffer{UsedBytes: st.UsedBytes, QuotaBytes: st.QuotaBytes}
	if !st.OldestAt.IsZero() {
		b.Buffer.OldestAt = rfc3339(st.OldestAt)
	}
	points := 0
	for _, ser := range seg.Series {
		if !s.cfg.Enabled(ser.Name) {
			continue
		}
		ws := wireSeries{Name: ser.Name, Labels: map[string]string{}, RollupS: seg.RollupS, Points: make([]wirePoint, 0, len(ser.Points))}
		for _, l := range ser.Labels {
			ws.Labels[l.K] = cut(l.V, s.k.Limits.LabelValueMax)
		}
		for _, p := range ser.Points {
			// A non-finite value would fail the JSON encoder on every retry.
			if !finite(p.V) || p.Agg != nil && (!finite(p.Agg.Min) || !finite(p.Agg.Avg) || !finite(p.Agg.Max)) {
				continue
			}
			ws.Points = append(ws.Points, wirePoint{off: p.Off, v: p.V, agg: p.Agg})
		}
		if len(ws.Points) == 0 {
			continue
		}
		points += len(ws.Points)
		b.Series = append(b.Series, ws)
	}
	switch l := s.k.Limits; {
	case len(b.Series) > l.SeriesPerBatch:
		return nil, fmt.Sprintf("%d рядов", len(b.Series)), nil
	case points > l.PointsPerBatch:
		return nil, fmt.Sprintf("%d точек", points), nil
	}
	if raw, err = json.Marshal(b); err != nil {
		return nil, "", fmt.Errorf("пачка %d: %w", c.Seq, err)
	}
	if len(raw) > s.k.Limits.BodyInflatedMax {
		return nil, fmt.Sprintf("%d байт без сжатия", len(raw)), nil
	}
	return raw, "", nil
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// capabilities keeps the known ones, sorted and unique.
func (s *Sender) capabilities(in []string) []string {
	out := []string{}
	for _, c := range in {
		if s.k.Capabilities[c] && !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	slices.Sort(out)
	if len(out) > maxCapabilities {
		out = out[:maxCapabilities]
	}
	return out
}

func (s *Sender) failedUnits(in []string) []string {
	var out []string
	for _, u := range in {
		if len(out) == s.k.Limits.FailedUnitsMax {
			break
		}
		if u = cut(u, maxUnitName); u != "" {
			out = append(out, u)
		}
	}
	return out
}

// cut drops control characters and cuts s to n runes.
func cut(s string, n int) string {
	var b strings.Builder
	k := 0
	for _, r := range s {
		if k == n {
			break
		}
		if r < 0x20 || r == 0x7f || r == utf8.RuneError {
			continue
		}
		b.WriteRune(r)
		k++
	}
	return b.String()
}
