package collect

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"strconv"
)

// userHZ is the tick of /proc/stat: USER_HZ is 100 in the Linux ABI of every
// architecture the agent ships for (amd64, arm64).
const userHZ = 100

// cpuModes are the /proc/stat columns after the "cpuN" label, in order.
// guest and guest_nice are already included in user and nice.
func cpuModes() [8]string {
	return [8]string{"user", "nice", "system", "idle", "iowait", "irq", "softirq", "steal"}
}

const sysCPU = "sys/devices/system/cpu"

// CPU collects CPU time by mode, load average, frequency and throttling
// counters (the "fast" group).
func (c Collector) CPU(s *Snapshot) error {
	return errors.Join(c.cpuStat(s), c.loadavg(s), c.cpuSysfs(s))
}

// cpuStat parses /proc/stat: the aggregate "cpu" line (or the "cpuN" lines
// with PerCore) and the number of online logical CPUs.
func (c Collector) cpuStat(s *Snapshot) error {
	data, absent, err := c.readFile("proc/stat")
	if absent || err != nil {
		return err
	}
	modes := cpuModes()
	var total [8]uint64
	totalN := -1 // fields parsed from the aggregate line; -1 — no line
	cores := 0
	start := len(s.Points)
	for line := range bytes.Lines(data) {
		label, rest := nextField(line)
		if len(label) < 3 || string(label[:3]) != "cpu" {
			continue
		}
		var v [8]uint64
		n, ok := parseTicks(rest, &v)
		if !ok {
			continue
		}
		if len(label) == 3 {
			if totalN < 0 {
				total, totalN = v, n
			}
			continue
		}
		id := label[3:]
		if _, isNum := parseUint(id); !isNum {
			continue
		}
		cores++
		if c.PerCore {
			cpu := string(id)
			for i := range n {
				s.add(MetricCPUTime, Counter, ticksToSeconds(v[i]), Attr{AttrCPU, cpu}, Attr{AttrMode, modes[i]})
			}
		}
	}
	if totalN < 0 {
		s.Points = s.Points[:start]
		return fmt.Errorf("/proc/stat: нет строки cpu: %w", errMalformed)
	}
	if !c.PerCore {
		for i := range totalN {
			s.add(MetricCPUTime, Counter, ticksToSeconds(total[i]), Attr{AttrMode, modes[i]})
		}
	}
	if cores > 0 {
		s.add(MetricCPULogicalCount, Gauge, float64(cores))
	}
	return nil
}

// parseTicks reads up to 8 tick columns; at least user, nice, system and
// idle (every kernel has them) are required.
func parseTicks(rest []byte, v *[8]uint64) (int, bool) {
	n := 0
	for n < len(v) {
		var f []byte
		f, rest = nextField(rest)
		if len(f) == 0 {
			break
		}
		x, ok := parseUint(f)
		if !ok {
			return 0, false
		}
		v[n] = x
		n++
	}
	return n, n >= 4
}

func ticksToSeconds(t uint64) float64 { return float64(t) / userHZ }

// loadavg parses /proc/loadavg: "1m 5m 15m running/total lastpid".
func (c Collector) loadavg(s *Snapshot) error {
	data, absent, err := c.readFile("proc/loadavg")
	if absent || err != nil {
		return err
	}
	var l [3]float64
	rest := data
	for i := range l {
		var f []byte
		f, rest = nextField(rest)
		x, ok := parseNonNegFloat(f)
		if !ok {
			return fmt.Errorf("/proc/loadavg: %w", errMalformed)
		}
		l[i] = x
	}
	s.add(MetricLoad1, Gauge, l[0])
	s.add(MetricLoad5, Gauge, l[1])
	s.add(MetricLoad15, Gauge, l[2])
	return nil
}

// cpuSysfs reads cpufreq (scaling_cur_freq, kHz) and the Intel
// thermal_throttle counters of every cpuN under /sys/devices/system/cpu. An
// offline CPU has neither and is skipped. Core throttle events are summed
// over CPUs; the package counter repeats on each CPU of a package, so its
// maximum is taken.
func (c Collector) cpuSysfs(s *Snapshot) error {
	entries, err := fs.ReadDir(c.Root, sysCPU)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("чтение /%s: %w", sysCPU, err)
	}
	var (
		freqSum           float64
		freqN             int
		coreThr, pkgThr   uint64
		haveCore, havePkg bool
		errs              []error
	)
	for _, e := range entries {
		name := e.Name()
		if len(name) < 4 || name[:3] != "cpu" {
			continue
		}
		if _, ok := parseUint([]byte(name[3:])); !ok {
			continue
		}
		dir := sysCPU + "/" + name
		if khz, ok, err := c.readUintFile(dir + "/cpufreq/scaling_cur_freq"); err != nil {
			errs = append(errs, err)
		} else if ok {
			hz := float64(khz) * 1000
			if c.PerCore {
				s.add(MetricCPUFrequency, Gauge, hz, Attr{AttrCPU, name[3:]})
			}
			freqSum += hz
			freqN++
		}
		if n, ok, err := c.readUintFile(dir + "/thermal_throttle/core_throttle_count"); err != nil {
			errs = append(errs, err)
		} else if ok {
			coreThr += n
			haveCore = true
		}
		if n, ok, err := c.readUintFile(dir + "/thermal_throttle/package_throttle_count"); err != nil {
			errs = append(errs, err)
		} else if ok {
			pkgThr = max(pkgThr, n)
			havePkg = true
		}
	}
	if freqN > 0 {
		s.addCap(CapCPUFreq)
		if !c.PerCore {
			s.add(MetricCPUFrequency, Gauge, freqSum/float64(freqN))
		}
	}
	if haveCore || havePkg {
		s.addCap(CapCPUThrottle)
	}
	if haveCore {
		s.add(MetricCPUThrottles, Counter, float64(coreThr), Attr{AttrType, "core"})
	}
	if havePkg {
		s.add(MetricCPUThrottles, Counter, float64(pkgThr), Attr{AttrType, "package"})
	}
	return errors.Join(errs...)
}

// readUintFile reads a sysfs file holding one decimal number; ok is false
// when the file is absent. A malformed value is an error and no value.
func (c Collector) readUintFile(name string) (v uint64, ok bool, err error) {
	data, absent, err := c.readFile(name)
	if absent || err != nil {
		return 0, false, err
	}
	f, _ := nextField(data)
	v, ok = parseUint(f)
	if !ok {
		return 0, false, fmt.Errorf("/%s: %w", name, errMalformed)
	}
	return v, true, nil
}

// nextField returns the next blank-separated field of b and the rest.
func nextField(b []byte) (field, rest []byte) {
	i := 0
	for i < len(b) && isBlank(b[i]) {
		i++
	}
	j := i
	for j < len(b) && !isBlank(b[j]) {
		j++
	}
	return b[i:j], b[j:]
}

func isBlank(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// parseUint parses a non-empty decimal number without allocating; overflow
// and any non-digit are a failure.
func parseUint(b []byte) (uint64, bool) {
	if len(b) == 0 {
		return 0, false
	}
	var n uint64
	for _, c := range b {
		if c < '0' || c > '9' {
			return 0, false
		}
		d := uint64(c - '0')
		if n > (math.MaxUint64-d)/10 {
			return 0, false
		}
		n = n*10 + d
	}
	return n, true
}

// parseNonNegFloat parses a finite, non-negative decimal like "629105.21".
func parseNonNegFloat(b []byte) (float64, bool) {
	if len(b) == 0 || len(b) > 32 || b[0] < '0' || b[0] > '9' {
		return 0, false
	}
	x, err := strconv.ParseFloat(string(b), 64)
	if err != nil || math.IsInf(x, 0) || math.IsNaN(x) {
		return 0, false
	}
	return x, true
}
