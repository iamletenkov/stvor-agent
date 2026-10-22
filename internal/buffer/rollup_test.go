package buffer

import (
	"testing"
	"time"
)

func TestRollup(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	gauge := []float64{5, 1, 9, 3, 2, 4, 10, 20, 30, 40, 50, 60}
	counter := []float64{100, 110, 120, 130, 140, 150, 160, 170, 180, 190, 200, 210}
	seg := &Segment{Version: 1, StartAt: start, StartMonoMS: 1000}
	g := Series{Name: "system.memory.usage", Kind: Gauge}
	c := Series{Name: "system.cpu.time", Kind: Counter}
	for i := range gauge {
		off := int64(i) * 10_000
		g.Points = append(g.Points, Point{Off: off, V: gauge[i]})
		c.Points = append(c.Points, Point{Off: off, V: counter[i]})
	}
	seg.Series = []Series{g, c}

	r := Rollup(seg, 60)
	if r.RollupS != 60 || seg.RollupS != 0 {
		t.Fatalf("rollup_s %d, original %d", r.RollupS, seg.RollupS)
	}
	tests := []struct {
		name string
		got  Point
		want Point
	}{
		{"gauge 1st min", r.Series[0].Points[0], Point{Off: 0, V: 4, Agg: &Agg{Min: 1, Avg: 4, Max: 9, N: 6}}},
		{"gauge 2nd min", r.Series[0].Points[1], Point{Off: 60_000, V: 35, Agg: &Agg{Min: 10, Avg: 35, Max: 60, N: 6}}},
		{"counter 1st min — last", r.Series[1].Points[0], Point{Off: 0, V: 150}},
		{"counter 2nd min — last", r.Series[1].Points[1], Point{Off: 60_000, V: 210}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got.Off != tc.want.Off || tc.got.V != tc.want.V {
				t.Fatalf("got %+v, want %+v", tc.got, tc.want)
			}
			if (tc.got.Agg == nil) != (tc.want.Agg == nil) || (tc.got.Agg != nil && *tc.got.Agg != *tc.want.Agg) {
				t.Fatalf("agg %+v, want %+v", tc.got.Agg, tc.want.Agg)
			}
		})
	}

	// 1 min → 5 min: weighted average, min of mins, max of maxes.
	r5 := Rollup(r, 300)
	p := r5.Series[0].Points
	if len(p) != 1 || p[0].Agg == nil || *p[0].Agg != (Agg{Min: 1, Avg: 19.5, Max: 60, N: 12}) {
		t.Fatalf("5-min gauge = %+v", p)
	}
	if cp := r5.Series[1].Points; len(cp) != 1 || cp[0].V != 210 || cp[0].Agg != nil {
		t.Fatalf("5-min counter = %+v", cp)
	}
	if again := Rollup(r5, 300); len(again.Series[0].Points) != 1 || *again.Series[0].Points[0].Agg != *p[0].Agg {
		t.Fatal("rolling a rolled segment again is not stable")
	}
}

// Windows follow the wall clock, not the segment start, so neighbour
// segments never produce two points for one window... except at the seam.
func TestRollupWindowsAlignToWallClock(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 10, 1, 0, 0, 50, 0, time.UTC)
	seg := &Segment{Version: 1, StartAt: start, Series: []Series{{Name: "g", Kind: Gauge, Points: []Point{
		{Off: 0, V: 1}, {Off: 10_000, V: 2}, {Off: 20_000, V: 3},
	}}}}
	r := Rollup(seg, 60)
	pts := r.Series[0].Points
	if len(pts) != 2 || pts[0].Off != 0 || pts[1].Off != 10_000 || pts[0].Agg.N != 1 || pts[1].Agg.N != 2 {
		t.Fatalf("points %+v", pts)
	}
	for _, p := range pts {
		if p.Agg.Min > p.Agg.Avg || p.Agg.Avg > p.Agg.Max {
			t.Fatalf("min ≤ avg ≤ max broken: %+v", p.Agg)
		}
	}
}

// A counter reset (restart of a service, a wrapped interface counter) inside a
// window splits it: the value before the reset is kept, not lost.
func TestRollupSplitsWindowAtCounterReset(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	seg := &Segment{Version: 1, StartAt: start, Series: []Series{{Name: "c", Kind: Counter, Points: []Point{
		{Off: 0, V: 100}, {Off: 10_000, V: 110}, {Off: 20_000, V: 5}, {Off: 30_000, V: 15}, {Off: 60_000, V: 30},
	}}}}
	pts := Rollup(seg, 60).Series[0].Points
	want := []Point{{Off: 0, V: 110}, {Off: 20_000, V: 15}, {Off: 60_000, V: 30}}
	if len(pts) != len(want) {
		t.Fatalf("points %+v, want %+v", pts, want)
	}
	for i := range want {
		if pts[i].Off != want[i].Off || pts[i].V != want[i].V {
			t.Fatalf("point %d = %+v, want %+v", i, pts[i], want[i])
		}
	}
}
