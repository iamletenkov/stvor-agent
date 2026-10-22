package collect

import (
	"testing"
	"testing/fstest"
)

func TestCPUBrokenInput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		files   fstest.MapFS
		wantErr bool
		want    map[string]float64
		absent  []string
	}{
		{
			name:    "empty stat is malformed",
			files:   fstest.MapFS{"proc/stat": {Data: nil}},
			wantErr: true,
			absent:  []string{"system.cpu.time{mode=user}", MetricCPULogicalCount},
		},
		{
			name: "old kernel without steal: present modes only",
			files: fstest.MapFS{"proc/stat": {Data: []byte(
				"cpu  100 0 50 1000 7\ncpu0 100 0 50 1000 7\n")}},
			want:   map[string]float64{"system.cpu.time{mode=user}": 1, "system.cpu.time{mode=iowait}": 0.07, MetricCPULogicalCount: 1},
			absent: []string{"system.cpu.time{mode=steal}", "system.cpu.time{mode=irq}"},
		},
		{
			name: "garbage per-core lines are skipped",
			files: fstest.MapFS{"proc/stat": {Data: []byte(
				"cpu  100 0 50 1000 0 0 0 0 0 0\ncpuX 1 2 3\ncpu0 1 -2 x\ncpu1\ncpu0 100 0 50 1000 0 0 0 0 0 0\nintr 1 2\n")}},
			want: map[string]float64{"system.cpu.time{mode=user}": 1, MetricCPULogicalCount: 1},
		},
		{
			name: "overflowing number is malformed, not wrapped",
			files: fstest.MapFS{"proc/stat": {Data: []byte(
				"cpu  99999999999999999999999 0 0 0\n")}},
			wantErr: true,
			absent:  []string{"system.cpu.time{mode=user}"},
		},
		{
			name: "loadavg garbage",
			files: fstest.MapFS{
				"proc/stat":    {Data: []byte("cpu  1 2 3 4\n")},
				"proc/loadavg": {Data: []byte("abc\n")},
			},
			wantErr: true,
			absent:  []string{MetricLoad1, MetricLoad5, MetricLoad15},
		},
		{
			name: "cpufreq garbage and missing throttle files",
			files: fstest.MapFS{
				"proc/stat": {Data: []byte("cpu  1 2 3 4\ncpu0 1 2 3 4\ncpu1 1 2 3 4\n")},
				"sys/devices/system/cpu/cpu0/cpufreq/scaling_cur_freq":             {Data: []byte("oops\n")},
				"sys/devices/system/cpu/cpu1/cpufreq/scaling_cur_freq":             {Data: []byte("1000000\n")},
				"sys/devices/system/cpu/cpu1/thermal_throttle/core_throttle_count": {Data: []byte("5\n")},
				"sys/devices/system/cpu/cpufreq/policy0/scaling_cur_freq":          {Data: []byte("7\n")},
			},
			wantErr: true, // cpu0's garbage is reported, cpu1 still counts
			want: map[string]float64{
				MetricCPUFrequency:                1e9,
				"system.cpu.throttles{type=core}": 5,
			},
			absent: []string{"system.cpu.throttles{type=package}"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var s Snapshot
			err := Collector{Root: tc.files}.CPU(&s)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			got := pointMap(t, &s)
			for k, v := range tc.want {
				if p, ok := got[k]; !ok || !near(p.Value, v) {
					t.Errorf("%s = %v (present %v), want %v", k, p.Value, ok, v)
				}
			}
			for _, k := range tc.absent {
				if p, ok := got[k]; ok {
					t.Errorf("%s must be absent, got %v", k, p.Value)
				}
			}
		})
	}
}

func TestParseUint(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want uint64
		ok   bool
	}{
		{"0", 0, true},
		{"18446744073709551615", 18446744073709551615, true},
		{"18446744073709551616", 0, false},
		{"", 0, false},
		{"-1", 0, false},
		{"12a", 0, false},
	}
	for _, tc := range tests {
		got, ok := parseUint([]byte(tc.in))
		if got != tc.want || ok != tc.ok {
			t.Errorf("parseUint(%q) = %d, %v; want %d, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

// FuzzCPU runs its seed corpus of broken lines in every `go test`: the
// parsers must not panic on anything /proc could hand them.
func FuzzCPU(f *testing.F) {
	for _, seed := range []string{
		"", "cpu", "cpu ", "cpu  1", "cpu0", "cpu0 x", "cpu 1 2 3 4 5 6 7 8 9 10 11 12",
		"cpu4294967296 1 2 3 4", "\x00\xff", "cpu  1 2 3 4\ncpu0 1 2", "1.0 2.0", "1.0 2.0 3.0 1/2 3",
		"NaN NaN Inf", "-1 -2 -3",
	} {
		f.Add(seed, seed)
	}
	f.Fuzz(func(_ *testing.T, stat, other string) {
		files := fstest.MapFS{
			"proc/stat":    {Data: []byte(stat)},
			"proc/loadavg": {Data: []byte(other)},
			"sys/devices/system/cpu/cpu0/cpufreq/scaling_cur_freq":                {Data: []byte(other)},
			"sys/devices/system/cpu/cpu0/thermal_throttle/core_throttle_count":    {Data: []byte(other)},
			"sys/devices/system/cpu/cpu0/thermal_throttle/package_throttle_count": {Data: []byte(other)},
		}
		var s Snapshot
		_ = Collector{Root: files, PerCore: len(stat)%2 == 0}.CPU(&s)
	})
}
