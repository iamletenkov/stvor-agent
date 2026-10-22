package collect

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"strings"
	"unicode/utf8"
)

// MetricTemperature (portal contract v1): °C of every thermal zone and hwmon
// temperature input; thermal throttling is system.cpu.throttles.
const MetricTemperature = "system.thermal.temperature" // gauge, Cel; zone

// maxThermalZones is thermal_zones_per_machine_max of the contract.
const maxThermalZones = 64

// maxZoneLabel is the zone label limit of the contract.
const maxZoneLabel = 64

// Plausible sensor range in millidegrees: placeholders like −273150 or
// 255000 of an unconnected input are not temperatures.
const (
	minMilliC = -100_000
	maxMilliC = 250_000
)

const (
	sysThermal = "sys/class/thermal"
	sysHwmon   = "sys/class/hwmon"
)

// Thermal collects /sys/class/thermal/thermal_zone*/temp, labelled with
// the zone type ("CPU-therm"), and /sys/class/hwmon/hwmon*/temp*_input,
// labelled "hwmon:<name>/<label or tempN>"; a repeated label gets "#2",
// "#3"… in directory order. At most maxThermalZones. A sensor without a
// readable value is absent: sysfs answers EINVAL/ENODATA for sleeping
// sensors, so a failed read is not an error.
func (c Collector) Thermal(s *Snapshot) error {
	t := thermalScan{c: c, s: s, seen: map[string]int{}}
	return errors.Join(t.zones(), t.hwmon())
}

type thermalScan struct {
	c    Collector
	s    *Snapshot
	seen map[string]int
	n    int
}

func (t *thermalScan) emit(label string, milli int64) {
	if t.n == maxThermalZones || milli < minMilliC || milli > maxMilliC {
		return
	}
	label = sanitizeLabel(label, maxZoneLabel)
	if label == "" {
		return
	}
	t.seen[label]++
	if k := t.seen[label]; k > 1 {
		suffix := "#" + strconv.Itoa(k)
		label = sanitizeLabel(label, maxZoneLabel-len(suffix)) + suffix
	}
	t.n++
	t.s.addCap(CapThermal)
	t.s.add(MetricTemperature, Gauge, float64(milli)/1000, Attr{AttrZone, label})
}

func (t *thermalScan) readDir(dir string) ([]fs.DirEntry, error) {
	entries, err := fs.ReadDir(t.c.Root, dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("чтение /%s: %w", dir, err)
	}
	return entries, nil
}

func (t *thermalScan) zones() error {
	entries, err := t.readDir(sysThermal)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "thermal_zone") {
			continue
		}
		dir := sysThermal + "/" + e.Name() + "/"
		milli, ok := t.c.sysfsInt(dir + "temp")
		if !ok {
			continue
		}
		typ := t.c.sysfsLine(dir + "type")
		if typ == "" {
			typ = e.Name()
		}
		t.emit(typ, milli)
	}
	return nil
}

func (t *thermalScan) hwmon() error {
	entries, err := t.readDir(sysHwmon)
	if err != nil {
		return err
	}
	for _, e := range entries {
		dir := sysHwmon + "/" + e.Name()
		// Kernels before 5.x keep the attributes of some drivers under
		// hwmonN/device.
		if !t.c.exists(dir+"/name") && t.c.exists(dir+"/device/name") {
			dir += "/device"
		}
		name := t.c.sysfsLine(dir + "/name")
		if name == "" {
			name = e.Name()
		}
		inputs, err := t.readDir(dir)
		if err != nil {
			return err
		}
		for _, in := range inputs {
			f := in.Name()
			idx, ok := strings.CutSuffix(f, "_input")
			if !ok || !strings.HasPrefix(idx, "temp") {
				continue
			}
			if _, isNum := parseUint([]byte(idx[4:])); !isNum {
				continue
			}
			milli, ok := t.c.sysfsInt(dir + "/" + f)
			if !ok {
				continue
			}
			label := t.c.sysfsLine(dir + "/" + idx + "_label")
			if label == "" {
				label = idx
			}
			t.emit("hwmon:"+name+"/"+label, milli)
		}
	}
	return nil
}

// sysfsLine is the first line of a sysfs file, trimmed; "" when unreadable.
func (c Collector) sysfsLine(name string) string {
	data, err := fs.ReadFile(c.Root, name)
	if err != nil {
		return ""
	}
	line, _, _ := bytes.Cut(data, []byte("\n"))
	return string(bytes.TrimSpace(line))
}

// sysfsInt reads a signed decimal sysfs value.
func (c Collector) sysfsInt(name string) (int64, bool) {
	data, err := fs.ReadFile(c.Root, name)
	if err != nil {
		return 0, false
	}
	f, _ := nextField(data)
	v, err := strconv.ParseInt(string(f), 10, 64)
	return v, err == nil
}

// sanitizeLabel drops control characters and invalid UTF-8 and cuts the
// value to limit characters (the contract's label rules).
func sanitizeLabel(v string, limit int) string {
	var b strings.Builder
	n := 0
	for _, r := range v {
		if n == limit {
			break
		}
		if r < 0x20 || r == 0x7f || r == utf8.RuneError {
			continue
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}
