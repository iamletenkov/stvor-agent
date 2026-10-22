package collect

import (
	"context"
	"errors"
	"math"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// Fixture trees under testdata/ are excerpts of /proc and /sys:
//   - pve-vm — copied read-only from a Proxmox KVM guest (Debian 12, 12 vCPU,
//     steal time, PSI, no cpufreq and no thermal_throttle in the guest);
//   - debian12-amd64 — Intel bare metal, kernel 6.1: cpufreq and
//     thermal_throttle counters;
//   - ubuntu2404-amd64 — AMD bare metal, kernel 6.8: cpufreq, no
//     thermal_throttle (Intel-only sysfs), reboot-required present;
//   - jetson-l4t-r32 — Jetson Nano, L4T R32 (Ubuntu 18.04, kernel 4.9,
//     arm64): no /proc/pressure (PSI appeared in 4.20), cpu3 offline.
//
// Task 6 adds /proc/net/{dev,wireless}, /proc/diskstats,
// /proc/self/mountinfo, /sys/class/net, /sys/block, thermal zones and hwmon:
// pve-vm — copied read-only from the same KVM guest (Docker veth/bridges,
// overlay mounts, no thermal zones, virtio_net with speed -1); the others are
// written after the real layouts: debian12 — PEAK USB CAN, NVMe + LVM + XFS,
// coretemp/nvme hwmon; ubuntu2404 — Wi-Fi, WWAN, btrfs subvolumes, snaps,
// two NVMe hwmon; jetson — gs_usb CAN, eMMC with boot areas, L4T zones
// (CPU-therm, GPU-therm…), hwmon attributes under device/ as on 4.9.
//
// jetson-nano-r32 is a read-only capture of a real Jetson Nano and
// sandbox-strict the agent's mountinfo under its systemd sandbox; the
// provenance of every fixture is in testdata/README.md.
//
// Cross-check with prometheus/procfs (done by hand, no dependency): procfs
// Stat() divides /proc/stat ticks by userHZ=100 exactly as here
// (CPUStat.User = 107463705/100 for pve-vm), and Meminfo *Bytes fields are
// the kB values ×1024; PSI totals are microseconds in both.

func fixture(t *testing.T, name string) Collector {
	t.Helper()
	return Collector{Root: os.DirFS("testdata/" + name)}
}

// key renders a point as name{k=v,…} for map lookups in tests.
func key(p Point) string {
	if len(p.Attrs) == 0 {
		return p.Name
	}
	parts := make([]string, len(p.Attrs))
	for i, a := range p.Attrs {
		parts[i] = a.Key + "=" + a.Value
	}
	return p.Name + "{" + strings.Join(parts, ",") + "}"
}

func pointMap(t *testing.T, s *Snapshot) map[string]Point {
	t.Helper()
	m := make(map[string]Point, len(s.Points))
	for _, p := range s.Points {
		k := key(p)
		if _, dup := m[k]; dup {
			t.Errorf("duplicate point %s", k)
		}
		m[k] = p
	}
	return m
}

func near(a, b float64) bool {
	if a == b {
		return true
	}
	return math.Abs(a-b) <= 1e-9*math.Max(math.Abs(a), math.Abs(b))
}

func clockOK(off time.Duration) ClockFunc {
	return func() (ClockState, error) { return ClockState{Synced: true, Offset: off}, nil }
}

func clockAbsent() (ClockState, error) { return ClockState{}, ErrAbsent }

func units(names ...string) UnitsFunc {
	return func(context.Context) ([]string, error) { return names, nil }
}

func unitsAbsent(context.Context) ([]string, error) { return nil, ErrAbsent }

const kib = 1024

func TestFixtures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		clock   ClockFunc
		units   UnitsFunc
		want    map[string]float64
		kinds   map[string]Kind
		absent  []string
		caps    []string
		bootID  string
		failed  []string
		synced  bool
		clockOn bool
	}{
		{
			name:  "debian12-amd64",
			clock: clockOK(1500 * time.Microsecond),
			units: units(),
			want: map[string]float64{
				"system.cpu.time{mode=user}":              47056.38,
				"system.cpu.time{mode=nice}":              12.03,
				"system.cpu.time{mode=system}":            15204.17,
				"system.cpu.time{mode=idle}":              1823915.54,
				"system.cpu.time{mode=iowait}":            842.11,
				"system.cpu.time{mode=irq}":               0,
				"system.cpu.time{mode=softirq}":           301.22,
				"system.cpu.time{mode=steal}":             0,
				"system.cpu.logical.count":                4,
				"system.cpu.load_average.1m":              0.42,
				"system.cpu.load_average.5m":              0.37,
				"system.cpu.load_average.15m":             0.31,
				"system.cpu.frequency":                    1797750e3,
				"system.cpu.throttles{type=core}":         22,
				"system.cpu.throttles{type=package}":      19,
				"system.memory.limit":                     7999056 * kib,
				"system.memory.linux.available":           6421180 * kib,
				"system.memory.usage{state=used}":         1637984 * kib,
				"system.memory.usage{state=free}":         3104512 * kib,
				"system.memory.usage{state=buffers}":      212344 * kib,
				"system.memory.usage{state=cached}":       3044216 * kib,
				"system.paging.usage{state=used}":         0,
				"system.paging.usage{state=free}":         999420 * kib,
				"system.memory.linux.pressure{type=some}": 0.081347,
				"system.memory.linux.pressure{type=full}": 0.060211,
				"system.uptime":                           1880443.21,
				"system.clock.synced":                     1,
				"system.clock.offset":                     0.0015,
				"system.systemd.units.failed":             0,
				"system.reboot_required":                  0,
			},
			kinds: map[string]Kind{
				"system.cpu.time{mode=user}":              Counter,
				"system.cpu.throttles{type=core}":         Counter,
				"system.memory.linux.pressure{type=some}": Counter,
				"system.cpu.load_average.1m":              Gauge,
				"system.memory.usage{state=used}":         Gauge,
				"system.uptime":                           Gauge,
			},
			caps:    []string{CapCAN, CapClock, CapCPUThrottle, CapCPUFreq, CapPSI, CapSystemd, CapThermal},
			bootID:  "5b0f7f1e-3c8a-4f0b-9d3e-1a2b3c4d5e6f",
			failed:  []string{},
			synced:  true,
			clockOn: true,
		},
		{
			name: "ubuntu2404-amd64",
			clock: func() (ClockState, error) {
				return ClockState{Synced: false, Offset: 3 * time.Second}, nil
			},
			units: units("nvidia-persistenced.service"),
			want: map[string]float64{
				"system.cpu.time{mode=user}":              22104.35,
				"system.cpu.time{mode=steal}":             0,
				"system.cpu.logical.count":                8,
				"system.cpu.load_average.15m":             0.71,
				"system.cpu.frequency":                    2134375e3,
				"system.memory.limit":                     15729520 * kib,
				"system.memory.linux.available":           9876332 * kib,
				"system.memory.usage{state=used}":         5511788 * kib,
				"system.paging.usage{state=used}":         182012 * kib,
				"system.paging.usage{state=free}":         4012288 * kib,
				"system.memory.linux.pressure{type=some}": 9.120332,
				"system.memory.linux.pressure{type=full}": 4.410921,
				"system.uptime":                           1016433.90,
				"system.clock.synced":                     0,
				"system.systemd.units.failed":             1,
				"system.reboot_required":                  1,
			},
			// No thermal_throttle on AMD; an unsynced clock has no offset.
			absent:  []string{"system.cpu.throttles", "system.clock.offset"},
			caps:    []string{CapClock, CapCPUFreq, CapPSI, CapSystemd, CapThermal, CapWiFi},
			bootID:  "c2a17a2e-7f4d-4b52-8a9c-0e5d6f7a8b9c",
			failed:  []string{"nvidia-persistenced.service"},
			synced:  false,
			clockOn: true,
		},
		{
			name:  "jetson-l4t-r32",
			clock: clockAbsent,
			units: units("nvgetty.service", "nvzramconfig.service"),
			want: map[string]float64{
				"system.cpu.time{mode=user}":      12013.30,
				"system.cpu.time{mode=irq}":       310.02,
				"system.cpu.logical.count":        3,
				"system.cpu.load_average.1m":      1.53,
				"system.cpu.frequency":            1479000e3,
				"system.memory.limit":             4059272 * kib,
				"system.memory.linux.available":   2410112 * kib,
				"system.memory.usage{state=used}": 1581384 * kib,
				"system.paging.usage{state=used}": 49508 * kib,
				"system.uptime":                   315500.12,
				"system.systemd.units.failed":     2,
				"system.reboot_required":          0,
			},
			// Kernel 4.9: no PSI; Jetson: no thermal_throttle; clock source absent.
			absent: []string{
				"system.memory.linux.pressure", "system.cpu.throttles",
				"system.clock.synced", "system.clock.offset",
			},
			caps:   []string{CapCAN, CapCPUFreq, CapSystemd, CapThermal},
			bootID: "9e1d2c3b-4a5f-4e6d-8c7b-6a5f4e3d2c1b",
			failed: []string{"nvgetty.service", "nvzramconfig.service"},
		},
		{
			name:  "pve-vm",
			clock: clockOK(-200 * time.Microsecond),
			units: unitsAbsent,
			want: map[string]float64{
				"system.cpu.time{mode=user}":              1074637.05,
				"system.cpu.time{mode=nice}":              9.17,
				"system.cpu.time{mode=system}":            348423.96,
				"system.cpu.time{mode=idle}":              6064491.96,
				"system.cpu.time{mode=iowait}":            12041.71,
				"system.cpu.time{mode=irq}":               0,
				"system.cpu.time{mode=softirq}":           38420.30,
				"system.cpu.time{mode=steal}":             1024.91,
				"system.cpu.logical.count":                12,
				"system.cpu.load_average.1m":              3.34,
				"system.cpu.load_average.5m":              10.71,
				"system.cpu.load_average.15m":             14.31,
				"system.memory.limit":                     24607436 * kib,
				"system.memory.linux.available":           10223964 * kib,
				"system.memory.usage{state=used}":         12580832 * kib,
				"system.paging.usage{state=used}":         7955280 * kib,
				"system.memory.linux.pressure{type=some}": 233.132474,
				"system.memory.linux.pressure{type=full}": 167.268690,
				"system.uptime":                           629105.21,
				"system.clock.synced":                     1,
				"system.clock.offset":                     -0.0002,
				"system.reboot_required":                  1,
			},
			// A KVM guest has neither cpufreq nor thermal_throttle; systemctl absent.
			absent: []string{
				"system.cpu.frequency", "system.cpu.throttles", "system.systemd.units.failed",
			},
			caps:    []string{CapClock, CapPSI},
			bootID:  "26940de4-95eb-4eb8-bd95-82b941070d94",
			synced:  true,
			clockOn: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := fixture(t, tc.name)
			c.Clock, c.FailedUnits = tc.clock, tc.units
			var s Snapshot
			if err := c.Collect(context.Background(), &s); err != nil {
				t.Fatalf("Collect: %v", err)
			}
			got := pointMap(t, &s)
			for k, v := range tc.want {
				p, ok := got[k]
				if !ok {
					t.Errorf("no point %s", k)
					continue
				}
				if !near(p.Value, v) {
					t.Errorf("%s = %v, want %v", k, p.Value, v)
				}
			}
			for k, kind := range tc.kinds {
				if p, ok := got[k]; ok && p.Kind != kind {
					t.Errorf("%s kind = %v, want %v", k, p.Kind, kind)
				}
			}
			for _, name := range tc.absent {
				for k := range got {
					if k == name || strings.HasPrefix(k, name+"{") {
						t.Errorf("point %s must be absent (no sensor), got %v", k, got[k].Value)
					}
				}
			}
			caps := slices.Clone(s.Capabilities)
			sort.Strings(caps)
			if !slices.Equal(caps, tc.caps) {
				t.Errorf("capabilities = %v, want %v", caps, tc.caps)
			}
			if s.BootID != tc.bootID {
				t.Errorf("BootID = %q, want %q", s.BootID, tc.bootID)
			}
			if (tc.failed == nil) != (s.FailedUnits == nil) || !slices.Equal(s.FailedUnits, tc.failed) {
				t.Errorf("FailedUnits = %#v, want %#v", s.FailedUnits, tc.failed)
			}
			if s.ClockKnown != tc.clockOn || s.ClockSynced != tc.synced {
				t.Errorf("clock known/synced = %v/%v, want %v/%v", s.ClockKnown, s.ClockSynced, tc.clockOn, tc.synced)
			}
		})
	}
}

func TestCPUPerCore(t *testing.T) {
	t.Parallel()
	c := fixture(t, "debian12-amd64")
	c.PerCore = true
	var s Snapshot
	if err := c.CPU(&s); err != nil {
		t.Fatal(err)
	}
	got := pointMap(t, &s)
	want := map[string]float64{
		"system.cpu.time{cpu=0,mode=user}":    11903.10,
		"system.cpu.time{cpu=3,mode=softirq}": 56.19,
		"system.cpu.time{cpu=2,mode=idle}":    456021.20,
		"system.cpu.frequency{cpu=0}":         2300000e3,
		"system.cpu.frequency{cpu=3}":         800000e3,
		"system.cpu.logical.count":            4,
		"system.cpu.throttles{type=core}":     22,
		"system.cpu.throttles{type=package}":  19,
		"system.cpu.load_average.1m":          0.42,
	}
	for k, v := range want {
		if p, ok := got[k]; !ok || !near(p.Value, v) {
			t.Errorf("%s = %v (present %v), want %v", k, p.Value, ok, v)
		}
	}
	times := 0
	for k := range got {
		if strings.HasPrefix(k, "system.cpu.time{mode=") || k == "system.cpu.frequency" {
			t.Errorf("per-core mode must not emit the aggregate %s", k)
		}
		if strings.HasPrefix(k, "system.cpu.time{cpu=") {
			times++
		}
	}
	if times != 4*8 {
		t.Errorf("per-core time points = %d, want 32", times)
	}
}

// Absent sources yield no point and no capability — never a zero.
func TestAbsentSourcesAreNotZero(t *testing.T) {
	t.Parallel()
	c := Collector{Root: fstest.MapFS{}, Clock: clockAbsent, FailedUnits: unitsAbsent}
	var s Snapshot
	if err := c.Collect(context.Background(), &s); err != nil {
		t.Fatalf("absent sources are not an error: %v", err)
	}
	for _, p := range s.Points {
		// The reboot flag is a marker file: its absence means "not required".
		if p.Name != MetricRebootRequired {
			t.Errorf("unexpected point %s = %v", key(p), p.Value)
		}
	}
	if len(s.Capabilities) != 0 || s.BootID != "" || s.FailedUnits != nil || s.ClockKnown {
		t.Errorf("absent sources leaked into the snapshot: %+v", s)
	}
	// nil hooks behave as absent sources.
	var s2 Snapshot
	if err := (Collector{Root: fstest.MapFS{}}).System(context.Background(), &s2); err != nil {
		t.Fatal(err)
	}
	if s2.ClockKnown || s2.FailedUnits != nil {
		t.Errorf("nil hooks must be absent: %+v", s2)
	}
}

func TestSourceErrorsAreReported(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	c := Collector{
		Root:        fstest.MapFS{"proc/uptime": {Data: []byte("12.5 30.0\n")}},
		Clock:       func() (ClockState, error) { return ClockState{}, boom },
		FailedUnits: func(context.Context) ([]string, error) { return nil, boom },
	}
	var s Snapshot
	err := c.System(context.Background(), &s)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	got := pointMap(t, &s)
	if p, ok := got[MetricUptime]; !ok || p.Value != 12.5 {
		t.Errorf("a failing source must not hide the others: uptime %v %v", p.Value, ok)
	}
	if _, ok := got[MetricUnitsFailed]; ok {
		t.Error("failed systemctl must not report 0 failed units")
	}
}

func TestSnapshotReset(t *testing.T) {
	t.Parallel()
	c := fixture(t, "pve-vm")
	var s Snapshot
	if err := c.Memory(&s); err != nil {
		t.Fatal(err)
	}
	n := len(s.Points)
	s.Reset()
	if len(s.Points) != 0 || len(s.Capabilities) != 0 || s.BootID != "" || s.ClockKnown || s.FailedUnits != nil {
		t.Fatalf("Reset left state: %+v", s)
	}
	if err := c.Memory(&s); err != nil {
		t.Fatal(err)
	}
	if len(s.Points) != n {
		t.Errorf("points after reset = %d, want %d", len(s.Points), n)
	}
}

// slowMetrics are the metrics of the contract's "slow" group (60 s); every
// other metric the collector emits is "fast" (10 s). The list mirrors
// groups in contract v1 of the portal (robot_telemetry/contract/v1.json).
var slowMetrics = []string{
	MetricNetSpeed, MetricNetDuplex, MetricNetMTU,
	MetricFSUsage, MetricFSLimit, MetricFSInodesUsage, MetricFSInodesLimit, MetricFSReadonly,
	MetricTemperature,
	MetricUptime, MetricClockSynced, MetricClockOffset, MetricUnitsFailed, MetricRebootRequired,
}

// TestCollectGroups: CollectFast emits only fast metrics, CollectSlow only
// slow ones, and together they are Collect.
func TestCollectGroups(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"debian12-amd64", "ubuntu2404-amd64", "jetson-l4t-r32", "jetson-nano-r32", "pve-vm", "pve-robot-debian13"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := fixture(t, name)
			c.Clock, c.FailedUnits = clockOK(time.Millisecond), units("a.service")
			c.Statfs = func(string) (FSStat, error) { return FSStat{Size: 10, Free: 5, Avail: 4, Files: 3, FilesFree: 1}, nil }
			var fast, slow, all Snapshot
			if err := c.CollectFast(context.Background(), &fast); err != nil {
				t.Fatalf("CollectFast: %v", err)
			}
			if err := c.CollectSlow(context.Background(), &slow); err != nil {
				t.Fatalf("CollectSlow: %v", err)
			}
			if err := c.Collect(context.Background(), &all); err != nil {
				t.Fatalf("Collect: %v", err)
			}
			for _, p := range fast.Points {
				if slices.Contains(slowMetrics, p.Name) {
					t.Errorf("fast group emitted slow %s", key(p))
				}
			}
			for _, p := range slow.Points {
				if !slices.Contains(slowMetrics, p.Name) {
					t.Errorf("slow group emitted fast %s", key(p))
				}
			}
			if len(fast.Points) == 0 || len(slow.Points) == 0 {
				t.Errorf("points: fast %d, slow %d", len(fast.Points), len(slow.Points))
			}
			if len(all.Points) != len(fast.Points)+len(slow.Points) {
				t.Errorf("Collect %d points, fast+slow %d", len(all.Points), len(fast.Points)+len(slow.Points))
			}
		})
	}
}

// TestJetsonNanoCapture runs the whole collector on jetson-nano-r32, a
// read-only capture of a real Jetson Nano (L4T R32.6.1, kernel 4.9, root on
// a USB SSD, Intel Wi-Fi, no CAN): see testdata/README.md.
func TestJetsonNanoCapture(t *testing.T) {
	t.Parallel()
	c := fixture(t, "jetson-nano-r32")
	c.Statfs = func(string) (FSStat, error) { return FSStat{Size: 100, Free: 50, Avail: 40}, nil }
	var s Snapshot
	if err := c.Collect(context.Background(), &s); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	checkPoints(t, &s, map[string]float64{
		"system.cpu.logical.count":                                                   4,
		"system.network.link.up{iface=eth0}":                                         1,
		"system.network.speed{iface=eth0}":                                           1e9,
		"system.network.duplex{iface=eth0}":                                          1,
		"system.network.wifi.signal{iface=wlan0}":                                    -41,
		"system.network.io{iface=eth0,direction=receive}":                            12525937,
		"system.thermal.temperature{zone=CPU-therm}":                                 29.5,
		"system.thermal.temperature{zone=GPU-therm}":                                 28.5,
		"system.thermal.temperature{zone=PMIC-Die}":                                  50,
		"system.filesystem.readonly{mountpoint=/,device=sda1,type=ext4}":             0,
		"system.filesystem.limit{mountpoint=/media/*/cidata,device=sda15,type=vfat}": 100,
	}, nil, []string{
		// No PSI before 4.20, no thermal_throttle on ARM, the bridge, the
		// dummy and Docker interfaces are not physical; the user's name in
		// /media/<user>/… never leaves the machine.
		"system.memory.linux.pressure", "system.cpu.throttles",
		"system.network.io{iface=l4tbr0", "system.network.io{iface=dummy0", "system.network.io{iface=docker0",
		"system.filesystem.limit{mountpoint=/media/nano",
	})
	if got := labelValues(&s, AttrIface); !slices.Equal(got, []string{"eth0", "rndis0", "usb0", "wlan0"}) {
		t.Errorf("interfaces = %v", got)
	}
	if s.Has(CapCPUThrottle) || s.Has(CapPSI) || s.Has(CapCAN) || !s.Has(CapWiFi) || !s.Has(CapThermal) {
		t.Errorf("capabilities = %v", s.Capabilities)
	}
}

// TestPveRobotCapture runs the whole collector on pve-robot-debian13, a capture
// of the test robot pve-robot-1 by tools/collect-fixture.sh (Proxmox guest,
// Debian 13, kernel 6.12, 2 vCPU, cgroup v2, Docker): see testdata/README.md.
func TestPveRobotCapture(t *testing.T) {
	t.Parallel()
	c := fixture(t, "pve-robot-debian13")
	c.Clock, c.FailedUnits = clockOK(time.Millisecond), units()
	c.Statfs = func(string) (FSStat, error) { return FSStat{Size: 100, Free: 50, Avail: 40}, nil }
	var s Snapshot
	if err := c.Collect(context.Background(), &s); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	checkPoints(t, &s, map[string]float64{
		"system.cpu.logical.count":                                       2,
		"system.memory.limit":                                            2069405696,
		"system.network.link.up{iface=eth0}":                             1,
		"system.network.mtu{iface=eth0}":                                 1500,
		"system.network.io{iface=eth0,direction=receive}":                633223132,
		"system.disk.operations{device=sda,direction=write}":             190975,
		"system.filesystem.readonly{mountpoint=/,device=sda1,type=ext4}": 0,
		"system.reboot_required":                                         1,
	}, nil, []string{
		// A VM: no thermal zones, no cpufreq, no throttle counters; Docker
		// interfaces and the CD-ROM are not reported.
		"system.thermal.temperature", "system.cpu.frequency", "system.cpu.throttles",
		"system.network.io{iface=docker0", "system.network.io{iface=veth", "system.disk.io{device=sr0",
	})
	if !s.Has(CapPSI) || s.Has(CapThermal) || s.Has(CapCPUThrottle) || s.Has(CapCAN) || s.Has(CapWiFi) {
		t.Errorf("capabilities = %v", s.Capabilities)
	}
}
