package collect

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func TestSystemBrokenInput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		files   fstest.MapFS
		wantErr bool
		boot    string
		uptime  bool
	}{
		{"uptime garbage", fstest.MapFS{"proc/uptime": {Data: []byte("x y\n")}}, true, "", false},
		{"uptime negative", fstest.MapFS{"proc/uptime": {Data: []byte("-5 1\n")}}, true, "", false},
		{"boot_id not a uuid", fstest.MapFS{"proc/sys/kernel/random/boot_id": {Data: []byte("../../etc\n")}}, true, "", false},
		{"boot_id upper case normalised", fstest.MapFS{
			"proc/sys/kernel/random/boot_id": {Data: []byte("26940DE4-95EB-4EB8-BD95-82B941070D94\n")},
			"proc/uptime":                    {Data: []byte("1.5\n")},
		}, false, "26940de4-95eb-4eb8-bd95-82b941070d94", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var s Snapshot
			err := Collector{Root: tc.files}.System(context.Background(), &s)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if s.BootID != tc.boot {
				t.Errorf("BootID = %q, want %q", s.BootID, tc.boot)
			}
			if _, ok := pointMap(t, &s)[MetricUptime]; ok != tc.uptime {
				t.Errorf("uptime present = %v, want %v", ok, tc.uptime)
			}
		})
	}
}

func TestClockFromTimex(t *testing.T) {
	t.Parallel()
	const (
		timeOK    = 0
		timeError = 5
		staUnsync = 0x0040
		staNano   = 0x2000
		staPLL    = 0x0001
	)
	tests := []struct {
		name   string
		state  int
		status int32
		offset int64
		want   ClockState
	}{
		{"synced, microseconds", timeOK, staPLL, -250, ClockState{Synced: true, Offset: -250 * time.Microsecond}},
		{"synced, nanoseconds", timeOK, staPLL | staNano, 1500, ClockState{Synced: true, Offset: 1500 * time.Nanosecond}},
		{"STA_UNSYNC", timeOK, staUnsync, 0, ClockState{Synced: false}},
		{"TIME_ERROR", timeError, staPLL, 10, ClockState{Synced: false, Offset: 10 * time.Microsecond}},
	}
	for _, tc := range tests {
		if got := clockFromTimex(tc.state, tc.status, tc.offset); got != tc.want {
			t.Errorf("%s: got %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

// Adjtimex with modes 0 only reads and needs no privileges (CI container).
func TestAdjtimexReads(t *testing.T) {
	t.Parallel()
	if _, err := Adjtimex(); err != nil {
		t.Fatalf("Adjtimex: %v", err)
	}
}

func TestParseFailedUnits(t *testing.T) {
	t.Parallel()
	out := strings.Join([]string{
		"nvgetty.service      loaded failed failed NVIDIA getty",
		"● bad\\x2dname.service loaded failed failed Escaped",
		"",
		"   ",
		"rm -rf /; loaded failed failed injection",
		"session-3.scope loaded failed failed Session 3",
		strings.Repeat("a", 300) + ".service loaded failed failed Too long",
		strings.Repeat("b", 121) + ".service loaded failed failed Over the contract label limit",
		strings.Repeat("c", 120) + ".service loaded failed failed At the limit",
	}, "\n")
	names := parseFailedUnits([]byte(out))
	want := []string{"nvgetty.service", "bad\\x2dname.service", "session-3.scope", strings.Repeat("c", 120) + ".service"}
	if !slices.Equal(names, want) {
		t.Errorf("parseFailedUnits = %q, want %q", names, want)
	}
	if names := parseFailedUnits(nil); names == nil || len(names) != 0 {
		t.Errorf("empty output must be an empty, non-nil list: %#v", names)
	}
}

// The snapshot keeps at most maxFailedUnits names (failed_units_max of the
// portal contract v1); the metric counts all.
func TestFailedUnitsCapped(t *testing.T) {
	t.Parallel()
	if maxFailedUnits != 32 || maxUnitName != 128 {
		t.Fatalf("limits %d/%d differ from contract v1 (failed_units_max 32, names ≤ 128)", maxFailedUnits, maxUnitName)
	}
	all := make([]string, maxFailedUnits+10)
	for i := range all {
		all[i] = "u" + strconv.Itoa(i) + ".service"
	}
	var s Snapshot
	c := Collector{Root: fstest.MapFS{}, FailedUnits: units(all...)}
	if err := c.System(context.Background(), &s); err != nil {
		t.Fatal(err)
	}
	if len(s.FailedUnits) != maxFailedUnits || s.FailedUnits[0] != "u0.service" {
		t.Errorf("snapshot list len = %d", len(s.FailedUnits))
	}
	if p := pointMap(t, &s)[MetricUnitsFailed]; p.Value != maxFailedUnits+10 {
		t.Errorf("%s = %v, want %d", MetricUnitsFailed, p.Value, maxFailedUnits+10)
	}
}

// fakeSystemctl writes a script that records its argv and prints out.
func fakeSystemctl(t *testing.T, body string) (Systemctl, string) {
	t.Helper()
	dir := t.TempDir()
	args := filepath.Join(dir, "args")
	path := filepath.Join(dir, "systemctl")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + args + "\n" + body + "\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return Systemctl{Path: path}, args
}

// Subtests run one by one: writing a script while a parallel test forks
// gives ETXTBSY on exec.
func TestSystemctlFailedUnits(t *testing.T) {
	t.Parallel()
	t.Run("lists failed units", func(t *testing.T) {
		sc, argsFile := fakeSystemctl(t, `printf 'a.service loaded failed failed A\nb.mount loaded failed failed B\n'`)
		got, err := sc.FailedUnits(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, []string{"a.service", "b.mount"}) {
			t.Errorf("got %q", got)
		}
		args, _ := os.ReadFile(argsFile)
		want := "list-units\n--state=failed\n--plain\n--no-legend\n--no-pager\n"
		if string(args) != want {
			t.Errorf("argv = %q, want %q", args, want)
		}
	})
	t.Run("none failed", func(t *testing.T) {
		sc, _ := fakeSystemctl(t, "exit 0")
		got, err := sc.FailedUnits(context.Background())
		if err != nil || got == nil || len(got) != 0 {
			t.Errorf("got %#v, %v; want empty list", got, err)
		}
	})
	t.Run("non-zero exit is an error", func(t *testing.T) {
		sc, _ := fakeSystemctl(t, "echo 'System has not been booted with systemd' >&2; exit 1")
		if _, err := sc.FailedUnits(context.Background()); err == nil || errors.Is(err, ErrAbsent) {
			t.Errorf("err = %v, want a non-absent error", err)
		}
	})
	t.Run("missing binary is absent", func(t *testing.T) {
		sc := Systemctl{Path: filepath.Join(t.TempDir(), "nope")}
		if _, err := sc.FailedUnits(context.Background()); !errors.Is(err, ErrAbsent) {
			t.Errorf("err = %v, want ErrAbsent", err)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		sc, _ := fakeSystemctl(t, "exec sleep 10")
		sc.Timeout = 100 * time.Millisecond
		start := time.Now()
		_, err := sc.FailedUnits(context.Background())
		if err == nil || errors.Is(err, ErrAbsent) {
			t.Errorf("err = %v, want timeout error", err)
		}
		if d := time.Since(start); d > 5*time.Second {
			t.Errorf("timeout not enforced: %v", d)
		}
	})
}

func TestNewDefaults(t *testing.T) {
	t.Parallel()
	c := New(true)
	if c.Root == nil || c.Clock == nil || c.FailedUnits == nil || !c.PerCore {
		t.Errorf("New must wire every source: %+v", c)
	}
}

// FuzzSystem runs its seed corpus of broken lines in every `go test`.
func FuzzSystem(f *testing.F) {
	for _, seed := range []string{
		"", " ", "1e400 2", "NaN", "-0", "x", "26940de4-95eb-4eb8-bd95-82b941070d9", "\x00",
		"● ", "●", "a.service", "\xff\xfe loaded failed",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(_ *testing.T, in string) {
		files := fstest.MapFS{
			"proc/uptime":                    {Data: []byte(in)},
			"proc/sys/kernel/random/boot_id": {Data: []byte(in)},
		}
		var s Snapshot
		_ = Collector{Root: files}.System(context.Background(), &s)
		_ = parseFailedUnits([]byte(in))
	})
}
