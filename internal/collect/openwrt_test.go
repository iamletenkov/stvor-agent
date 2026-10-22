package collect

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"
)

// openwrtProc is /proc of an OpenWrt 24.10 x86/64 VM (kernel 6.6, built
// without CONFIG_PSI): no /proc/pressure, no systemctl, no cpufreq.
func openwrtProc() fstest.MapFS {
	return fstest.MapFS{
		"proc/stat": {Data: []byte("cpu  4211 0 3175 912730 120 0 410 0 0 0\n" +
			"cpu0 4211 0 3175 912730 120 0 410 0 0 0\nbtime 1761100000\n")},
		"proc/meminfo": {Data: []byte("MemTotal:         997172 kB\nMemFree:          861004 kB\n" +
			"MemAvailable:     862612 kB\nBuffers:            3012 kB\nCached:            24676 kB\n" +
			"SwapTotal:             0 kB\nSwapFree:              0 kB\n")},
		"proc/loadavg":                   {Data: []byte("0.01 0.03 0.00 1/96 2817\n")},
		"proc/uptime":                    {Data: []byte("9210.44 9101.20\n")},
		"proc/sys/kernel/random/boot_id": {Data: []byte("5b0c1f3e-2a6d-4c8e-9f10-7a2b3c4d5e6f\n")},
	}
}

// TestOpenWrtWithoutPSI: no /proc/pressure is not an error on any tick — the
// pressure metric and the psi capability are simply absent (contract v1), the
// rest of memory is collected.
func TestOpenWrtWithoutPSI(t *testing.T) {
	t.Parallel()
	c := Collector{Root: openwrtProc(), Clock: clockAbsent, FailedUnits: unitsAbsent}
	for tick := range 3 {
		var s Snapshot
		if err := c.Collect(context.Background(), &s); err != nil {
			t.Fatalf("tick %d: Collect: %v", tick, err)
		}
		got := pointMap(t, &s)
		for k := range got {
			if strings.HasPrefix(k, MetricMemoryPressure) {
				t.Errorf("tick %d: point %s without PSI", tick, k)
			}
		}
		for _, cp := range s.Capabilities {
			if cp == CapPSI || cp == CapSystemd {
				t.Errorf("tick %d: capability %s without its source", tick, cp)
			}
		}
		if p, ok := got["system.memory.limit"]; !ok || !near(p.Value, 997172*kib) {
			t.Errorf("tick %d: system.memory.limit = %v, %t", tick, p.Value, ok)
		}
		if s.FailedUnits != nil {
			t.Errorf("tick %d: FailedUnits = %v without systemctl", tick, s.FailedUnits)
		}
	}
}
