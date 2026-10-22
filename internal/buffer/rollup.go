package buffer

// Rollup returns a copy of s rolled up to windows of sec seconds aligned to
// the wall clock: a gauge window becomes {min, avg, max} (rolled windows
// combine with a weighted average), a counter window keeps its last value.
// The point of a window sits at the window start, but not before the segment
// start; a counter reset inside a window starts a new point at the reset. Rolling to a level not above the current one keeps the level and is
// stable.
func Rollup(s *Segment, sec int) *Segment {
	out := *s
	out.RollupS = max(sec, s.RollupS)
	out.Series = make([]Series, 0, len(s.Series))
	startMs := s.StartAt.UnixMilli()
	size := int64(out.RollupS) * 1000
	for _, ser := range s.Series {
		ns := Series{Name: ser.Name, Kind: ser.Kind, Labels: ser.Labels}
		var (
			open     bool
			win, off int64
			last     float64
			agg      Agg
			sum      float64
		)
		emit := func() {
			if !open {
				return
			}
			if ser.Kind == Counter {
				ns.Points = append(ns.Points, Point{Off: off, V: last})
				return
			}
			a := agg
			a.Avg = min(max(sum/float64(a.N), a.Min), a.Max)
			ns.Points = append(ns.Points, Point{Off: off, V: a.Avg, Agg: &a})
		}
		for _, p := range ser.Points {
			w := floorDiv(startMs+p.Off, size) * size
			reset := open && ser.Kind == Counter && p.V < last
			if !open || w != win || reset {
				emit()
				open, off = true, max(w-startMs, 0)
				if reset && w == win {
					// A counter reset splits the window: the last value
					// before it is kept.
					off = p.Off
				}
				win, agg, sum = w, Agg{}, 0
			}
			last = p.V
			pMin, pAvg, pMax, n := p.V, p.V, p.V, 1
			if p.Agg != nil {
				pMin, pAvg, pMax, n = p.Agg.Min, p.Agg.Avg, p.Agg.Max, max(p.Agg.N, 1)
			}
			if agg.N == 0 || pMin < agg.Min {
				agg.Min = pMin
			}
			if agg.N == 0 || pMax > agg.Max {
				agg.Max = pMax
			}
			agg.N += n
			sum += pAvg * float64(n)
		}
		emit()
		out.Series = append(out.Series, ns)
	}
	return &out
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}
