package collect

import (
	"testing"
	"testing/fstest"
)

func TestMemoryBrokenInput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		files   fstest.MapFS
		wantErr bool
		want    map[string]float64
		absent  []string
		psi     bool
	}{
		{
			name:    "meminfo without MemTotal is malformed",
			files:   fstest.MapFS{"proc/meminfo": {Data: []byte("MemFree: 10 kB\n")}},
			wantErr: true,
			absent:  []string{MetricMemoryLimit, "system.memory.usage{state=free}"},
		},
		{
			name: "kernel < 3.14: no MemAvailable, no swap lines",
			files: fstest.MapFS{"proc/meminfo": {Data: []byte(
				"MemTotal: 1000 kB\nMemFree: 100 kB\nBuffers: 10 kB\nCached: 200 kB\n")}},
			want: map[string]float64{
				MetricMemoryLimit:                 1000 * kib,
				"system.memory.usage{state=used}": 690 * kib,
			},
			absent: []string{MetricMemoryAvailable, "system.paging.usage{state=used}"},
		},
		{
			name: "garbage lines skipped, used never negative",
			files: fstest.MapFS{"proc/meminfo": {Data: []byte(
				"MemTotal: 100 kB\nMemFree: x kB\nMemFree 5 kB\n:\nMemFree: 90 kB\nBuffers: 20 kB\nCached: 30 kB\nHugePages_Total: 3\n")}},
			want: map[string]float64{
				"system.memory.usage{state=used}": 0,
				"system.memory.usage{state=free}": 90 * kib,
			},
		},
		{
			name: "swap disabled is a real zero",
			files: fstest.MapFS{"proc/meminfo": {Data: []byte(
				"MemTotal: 100 kB\nSwapTotal: 0 kB\nSwapFree: 0 kB\n")}},
			want: map[string]float64{"system.paging.usage{state=used}": 0, "system.paging.usage{state=free}": 0},
		},
		{
			name: "psi garbage is malformed and absent",
			files: fstest.MapFS{
				"proc/meminfo":         {Data: []byte("MemTotal: 100 kB\n")},
				"proc/pressure/memory": {Data: []byte("some avg10=0.00\nfull total=abc\n")},
			},
			wantErr: true,
			absent:  []string{"system.memory.linux.pressure{type=some}", "system.memory.linux.pressure{type=full}"},
		},
		{
			name: "psi some only (CPU-style file)",
			files: fstest.MapFS{
				"proc/meminfo":         {Data: []byte("MemTotal: 100 kB\n")},
				"proc/pressure/memory": {Data: []byte("some avg10=0.00 avg60=0.00 avg300=0.00 total=2500000\n")},
			},
			want:   map[string]float64{"system.memory.linux.pressure{type=some}": 2.5},
			absent: []string{"system.memory.linux.pressure{type=full}"},
			psi:    true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var s Snapshot
			err := Collector{Root: tc.files}.Memory(&s)
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
			if s.Has(CapPSI) != tc.psi {
				t.Errorf("psi capability = %v, want %v", s.Has(CapPSI), tc.psi)
			}
		})
	}
}

// FuzzMemory runs its seed corpus of broken lines in every `go test`.
func FuzzMemory(f *testing.F) {
	for _, seed := range []string{
		"", ":", "MemTotal:", "MemTotal: kB", "MemTotal: 18446744073709551615 kB", "MemTotal: 1 MB",
		"some", "some total=", "full avg10=x total=1", "some total=99999999999999999999", "\x00:\x00",
	} {
		f.Add(seed, seed)
	}
	f.Fuzz(func(_ *testing.T, meminfo, psi string) {
		files := fstest.MapFS{
			"proc/meminfo":         {Data: []byte(meminfo)},
			"proc/pressure/memory": {Data: []byte(psi)},
		}
		var s Snapshot
		_ = Collector{Root: files}.Memory(&s)
	})
}
