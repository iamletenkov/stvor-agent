package collect

import (
	"bytes"
	"errors"
	"fmt"
)

// Memory collects /proc/meminfo and PSI memory pressure (the "fast" group).
func (c Collector) Memory(s *Snapshot) error {
	return errors.Join(c.meminfo(s), c.psiMemory(s))
}

// meminfo fields the collector uses, by index.
const (
	miTotal = iota
	miFree
	miAvailable
	miBuffers
	miCached
	miSwapTotal
	miSwapFree
	miCount
)

func meminfoIndex(key []byte) int {
	switch string(key) {
	case "MemTotal":
		return miTotal
	case "MemFree":
		return miFree
	case "MemAvailable":
		return miAvailable
	case "Buffers":
		return miBuffers
	case "Cached":
		return miCached
	case "SwapTotal":
		return miSwapTotal
	case "SwapFree":
		return miSwapFree
	}
	return -1
}

// meminfo parses "Key:   value kB" lines. MemTotal is required; every other
// value is reported only when its line is present (MemAvailable appeared in
// 3.14). used = total − free − buffers − cached, as `free` computes it.
func (c Collector) meminfo(s *Snapshot) error {
	data, absent, err := c.readFile("proc/meminfo")
	if absent || err != nil {
		return err
	}
	var (
		v    [miCount]uint64
		have [miCount]bool
	)
	for line := range bytes.Lines(data) {
		colon := bytes.IndexByte(line, ':')
		if colon <= 0 {
			continue
		}
		idx := meminfoIndex(line[:colon])
		if idx < 0 || have[idx] {
			continue
		}
		num, rest := nextField(line[colon+1:])
		unit, _ := nextField(rest)
		x, ok := parseUint(num)
		if !ok || (len(unit) != 0 && string(unit) != "kB") {
			continue
		}
		if len(unit) != 0 {
			if x > (1<<64-1)/1024 {
				continue
			}
			x *= 1024
		}
		v[idx], have[idx] = x, true
	}
	if !have[miTotal] {
		return fmt.Errorf("/proc/meminfo: нет MemTotal: %w", errMalformed)
	}
	s.add(MetricMemoryLimit, Gauge, float64(v[miTotal]))
	if have[miAvailable] {
		s.add(MetricMemoryAvailable, Gauge, float64(v[miAvailable]))
	}
	if have[miFree] && have[miBuffers] && have[miCached] {
		used := float64(v[miTotal]) - float64(v[miFree]) - float64(v[miBuffers]) - float64(v[miCached])
		s.add(MetricMemoryUsage, Gauge, max(used, 0), Attr{AttrState, "used"})
	}
	if have[miFree] {
		s.add(MetricMemoryUsage, Gauge, float64(v[miFree]), Attr{AttrState, "free"})
	}
	if have[miBuffers] {
		s.add(MetricMemoryUsage, Gauge, float64(v[miBuffers]), Attr{AttrState, "buffers"})
	}
	if have[miCached] {
		s.add(MetricMemoryUsage, Gauge, float64(v[miCached]), Attr{AttrState, "cached"})
	}
	if have[miSwapTotal] && have[miSwapFree] {
		used := float64(v[miSwapTotal]) - float64(v[miSwapFree])
		s.add(MetricPagingUsage, Gauge, max(used, 0), Attr{AttrState, "used"})
		s.add(MetricPagingUsage, Gauge, float64(v[miSwapFree]), Attr{AttrState, "free"})
	}
	return nil
}

// psiMemory parses /proc/pressure/memory (kernel ≥ 4.20 with PSI enabled):
// "some|full avg10=… avg60=… avg300=… total=<µs>". The totals are raw
// counters of stalled time, reported in seconds.
func (c Collector) psiMemory(s *Snapshot) error {
	data, absent, err := c.readFile("proc/pressure/memory")
	if absent || err != nil {
		return err
	}
	start := len(s.Points)
	for line := range bytes.Lines(data) {
		kind, rest := nextField(line)
		if string(kind) != "some" && string(kind) != "full" {
			continue
		}
		for {
			var f []byte
			f, rest = nextField(rest)
			if len(f) == 0 {
				break
			}
			if !bytes.HasPrefix(f, []byte("total=")) {
				continue
			}
			us, ok := parseUint(f[len("total="):])
			if !ok {
				break
			}
			typ := "some"
			if string(kind) == "full" {
				typ = "full"
			}
			s.add(MetricMemoryPressure, Counter, float64(us)/1e6, Attr{AttrType, typ})
			break
		}
	}
	if len(s.Points) == start {
		return fmt.Errorf("/proc/pressure/memory: %w", errMalformed)
	}
	// A partially broken file is not trusted: both lines or what parsed
	// cleanly, but never a "full" without its "some".
	if !hasPressure(s.Points[start:], "some") {
		s.Points = s.Points[:start]
		return fmt.Errorf("/proc/pressure/memory: нет some: %w", errMalformed)
	}
	s.addCap(CapPSI)
	return nil
}

func hasPressure(ps []Point, typ string) bool {
	for _, p := range ps {
		if p.Attrs[0].Value == typ {
			return true
		}
	}
	return false
}
