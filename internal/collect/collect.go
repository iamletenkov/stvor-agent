// Package collect reads the machine's telemetry from /proc and /sys (spec
// "robot telemetry" §4, Stvor plan Tasks 3 and 6): CPU, memory with PSI,
// system state, network (with CAN and Wi-Fi), disks, filesystems and
// temperatures. Every source is read relative to Collector.Root, so tests run on
// fixture trees; the clock and systemctl are hooks for the same reason.
//
// A source that does not exist on the machine (no PSI on kernel 4.9, no
// cpufreq in a VM, no systemctl) yields no point and no capability — never a
// zero. Counters are raw cumulative values; the portal computes rates.
// Metric names follow the OpenTelemetry semconv vocabulary (system.*).
//
// Groups (contract v1): CollectFast runs the 10 s "fast" group, CollectSlow
// the 60 s "slow" one (system state, link properties, filesystems,
// temperatures).
//
// Not collected in this version: CPU throttling on Jetson. The
// thermal_throttle counters are Intel-only sysfs; on L4T the throttle state
// lives in debugfs (/sys/kernel/debug/…, root only) and in tegrastats, which
// needs root as well, so a Jetson has no non-root source and reports no
// cpu_throttle capability rather than a zero.
package collect

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"time"
)

// Metric names (OpenTelemetry semconv system.*; the portal stores them with
// "." replaced by "_"). Units are the base units of semconv: seconds, bytes,
// hertz.
const (
	MetricCPUTime         = "system.cpu.time"             // counter, s; attrs mode [, cpu]
	MetricCPULogicalCount = "system.cpu.logical.count"    // gauge; online logical CPUs
	MetricLoad1           = "system.cpu.load_average.1m"  // gauge
	MetricLoad5           = "system.cpu.load_average.5m"  // gauge
	MetricLoad15          = "system.cpu.load_average.15m" // gauge
	MetricCPUFrequency    = "system.cpu.frequency"        // gauge, Hz; mean, or per cpu
	MetricCPUThrottles    = "system.cpu.throttles"        // counter; attr type=core|package

	MetricMemoryLimit     = "system.memory.limit"           // gauge, bytes (MemTotal)
	MetricMemoryAvailable = "system.memory.linux.available" // gauge, bytes (MemAvailable)
	MetricMemoryUsage     = "system.memory.usage"           // gauge, bytes; attr state
	MetricPagingUsage     = "system.paging.usage"           // gauge, bytes; attr state=used|free
	MetricMemoryPressure  = "system.memory.linux.pressure"  // counter, s stalled; attr type=some|full

	MetricUptime         = "system.uptime"               // gauge, s
	MetricClockSynced    = "system.clock.synced"         // gauge 0/1
	MetricClockOffset    = "system.clock.offset"         // gauge, s; only when synced
	MetricUnitsFailed    = "system.systemd.units.failed" // gauge; list in Snapshot.FailedUnits
	MetricRebootRequired = "system.reboot_required"      // gauge 0/1
)

// Attribute keys (the label dictionary of the telemetry contract).
const (
	AttrCPU        = "cpu"
	AttrMode       = "mode"
	AttrState      = "state"
	AttrType       = "type"
	AttrIface      = "iface"
	AttrDirection  = "direction"
	AttrDevice     = "device"
	AttrMountpoint = "mountpoint"
	AttrZone       = "zone"
)

// Capabilities a snapshot can report: the optional sources found on the
// machine.
const (
	CapPSI         = "psi"
	CapCPUFreq     = "cpufreq"
	CapCPUThrottle = "cpu_throttle"
	CapClock       = "clock"
	CapSystemd     = "systemd"
	CapCAN         = "can"
	CapWiFi        = "wifi"
	CapThermal     = "thermal"
)

// ErrAbsent is returned by a ClockFunc or UnitsFunc when the source does not
// exist on the machine; the collector then reports nothing for it.
var ErrAbsent = errors.New("источник отсутствует")

// errMalformed marks a source that exists but yielded nothing parseable.
var errMalformed = errors.New("не разобран")

// Kind is how the portal treats a point.
type Kind uint8

const (
	// Gauge is a level: the value as sampled.
	Gauge Kind = iota
	// Counter is a raw cumulative counter; the portal computes rates.
	Counter
)

func (k Kind) String() string {
	if k == Counter {
		return "counter"
	}
	return "gauge"
}

// Attr is one attribute (label) of a point.
type Attr struct {
	Key, Value string
}

// Point is one sampled value.
type Point struct {
	Name  string
	Kind  Kind
	Value float64
	Attrs []Attr
}

// Snapshot accumulates what the collectors found. Reuse it with Reset to keep
// the Points capacity between ticks.
type Snapshot struct {
	Points       []Point
	Capabilities []string
	// BootID is /proc/sys/kernel/random/boot_id, "" when absent.
	BootID string
	// ClockKnown tells whether the clock state could be read at all.
	ClockKnown  bool
	ClockSynced bool
	// FailedUnits lists failed systemd units (at most maxFailedUnits);
	// nil when systemctl is absent, empty when none failed.
	FailedUnits []string
}

// Reset empties the snapshot, keeping the allocated capacity.
func (s *Snapshot) Reset() {
	s.Points = s.Points[:0]
	s.Capabilities = s.Capabilities[:0]
	s.BootID = ""
	s.ClockKnown, s.ClockSynced = false, false
	s.FailedUnits = nil
}

// Has reports whether the capability was found.
func (s *Snapshot) Has(capability string) bool {
	return slices.Contains(s.Capabilities, capability)
}

func (s *Snapshot) addCap(capability string) {
	if !s.Has(capability) {
		s.Capabilities = append(s.Capabilities, capability)
	}
}

func (s *Snapshot) add(name string, kind Kind, v float64, attrs ...Attr) {
	s.Points = append(s.Points, Point{Name: name, Kind: kind, Value: v, Attrs: attrs})
}

// ClockState is the kernel's view of time synchronisation.
type ClockState struct {
	Synced bool
	// Offset is the estimated offset from the reference time.
	Offset time.Duration
}

// ClockFunc reads the clock state; ErrAbsent — no source.
type ClockFunc func() (ClockState, error)

// UnitsFunc lists failed systemd units; ErrAbsent — no systemctl.
type UnitsFunc func(ctx context.Context) ([]string, error)

// Collector reads the sources. The zero value of a hook means the source is
// absent.
type Collector struct {
	// Root is the filesystem root: paths are "proc/stat", "sys/…".
	Root fs.FS
	// PerCore emits CPU time and frequency per logical CPU (off by
	// default: the aggregate only).
	PerCore     bool
	Clock       ClockFunc
	FailedUnits UnitsFunc
	// LinkDump returns an rtnetlink RTM_GETLINK dump (CAN bus state).
	LinkDump func() ([]byte, error)
	// WiFiStations returns an nl80211 station dump of the interface
	// (Wi-Fi bitrate); ErrAbsent — no nl80211 or not a wireless interface.
	WiFiStations func(ifindex int) ([]byte, error)
	// Statfs reads filesystem usage at a mount point.
	Statfs StatfsFunc
}

// New wires the real machine: the root filesystem, adjtimex(2), systemctl
// with its default path and timeout, rtnetlink, nl80211 and statfs(2).
func New(perCore bool) Collector {
	return Collector{
		Root:         os.DirFS("/"),
		PerCore:      perCore,
		Clock:        Adjtimex,
		FailedUnits:  Systemctl{}.FailedUnits,
		LinkDump:     RouteLinkDump,
		WiFiStations: NL80211Stations,
		Statfs:       Statfs,
	}
}

// CollectFast runs the collectors of the contract's "fast" group (10 s):
// CPU, memory, network traffic and link state, disk I/O. A failing source
// does not hide the others, their errors are joined.
func (c Collector) CollectFast(_ context.Context, s *Snapshot) error {
	return errors.Join(c.CPU(s), c.Memory(s), c.Network(s), c.Disks(s))
}

// CollectSlow runs the collectors of the "slow" group (60 s): system state,
// link properties (speed, duplex, MTU), filesystems and temperatures.
func (c Collector) CollectSlow(ctx context.Context, s *Snapshot) error {
	return errors.Join(c.System(ctx, s), c.NetworkLinks(s), c.Filesystems(s), c.Thermal(s))
}

// Collect runs both groups.
func (c Collector) Collect(ctx context.Context, s *Snapshot) error {
	return errors.Join(c.CollectFast(ctx, s), c.CollectSlow(ctx, s))
}

// readFile reads name under Root; absent reports a missing file.
func (c Collector) readFile(name string) (data []byte, absent bool, err error) {
	data, err = fs.ReadFile(c.Root, name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("чтение /%s: %w", name, err)
	}
	return data, false, nil
}
