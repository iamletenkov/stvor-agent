package initsys_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/iamletenkov/stvor-agent/internal/initsys"
	"github.com/iamletenkov/stvor-agent/internal/platform"
)

// procdScripts are the init scripts the fake answers for: the test binary
// reached through a symlink /etc/init.d/<name> under a temporary Dir.
var procdScripts = map[string]bool{"stvor-agent": true, "stvor-agent-telemetry": true}

// procdFake puts both fake init scripts into one directory with a scenario and
// returns the adapter and the directory. Every call appends to calls.jsonl.
func procdFake(t *testing.T, sc scenario) (initsys.Procd, string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for name := range procdScripts {
		if err := os.Symlink(self, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := json.Marshal(sc)
	if err := os.WriteFile(filepath.Join(dir, "scenario.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return initsys.Procd{Dir: dir}, dir
}

// TestProcdCommands: /etc/init.d/<service> <verb>, the unit suffix .service
// dropped; disable --now is stop, then disable (rc.common has no --now). The
// environment is exactly PATH and LANG, as for systemctl.
func TestProcdCommands(t *testing.T) {
	t.Setenv("STVOR_TEST_SECRET", "must-not-leak")
	tests := []struct {
		name   string
		call   func(context.Context, initsys.Procd) error
		script string
		want   [][]string
	}{
		{"enable", func(ctx context.Context, p initsys.Procd) error { return p.Enable(ctx, "stvor-agent.service") },
			"stvor-agent", [][]string{{"enable"}}},
		{"restart", func(ctx context.Context, p initsys.Procd) error { return p.Restart(ctx, "stvor-agent.service") },
			"stvor-agent", [][]string{{"restart"}}},
		{"disable --now", func(ctx context.Context, p initsys.Procd) error { return p.DisableNow(ctx, "stvor-agent.service") },
			"stvor-agent", [][]string{{"stop"}, {"disable"}}},
		{"start telemetry", func(ctx context.Context, p initsys.Procd) error {
			return p.Start(ctx, "stvor-agent-telemetry.service")
		}, "stvor-agent-telemetry", [][]string{{"start"}}},
		{"stop telemetry", func(ctx context.Context, p initsys.Procd) error {
			return p.Stop(ctx, "stvor-agent-telemetry.service")
		}, "stvor-agent-telemetry", [][]string{{"stop"}}},
		{"name without suffix", func(ctx context.Context, p initsys.Procd) error { return p.Enable(ctx, "stvor-agent") },
			"stvor-agent", [][]string{{"enable"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, dir := procdFake(t, scenario{})
			if err := tc.call(t.Context(), p); err != nil {
				t.Fatalf("err = %v", err)
			}
			calls := procdCalls(t, dir)
			if len(calls) != len(tc.want) {
				t.Fatalf("calls = %+v, want %q", calls, tc.want)
			}
			for i, c := range calls {
				if c.Script != tc.script || !slices.Equal(c.Args, tc.want[i]) {
					t.Errorf("call %d = %s %q, want %s %q", i, c.Script, c.Args, tc.script, tc.want[i])
				}
				wantEnv := []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C.UTF-8"}
				if !slices.Equal(c.Env, wantEnv) {
					t.Errorf("env = %q, want %q", c.Env, wantEnv)
				}
			}
		})
	}
}

// TestProcdFailure: a non-zero exit and a missing script are errors; disable
// --now still runs disable after a failed stop and reports the failure.
func TestProcdFailure(t *testing.T) {
	t.Parallel()
	p, dir := procdFake(t, scenario{Exit: 1})
	if err := p.Enable(t.Context(), "stvor-agent.service"); err == nil {
		t.Error("exit 1: err = nil, want an error")
	}
	if err := p.DisableNow(t.Context(), "stvor-agent.service"); err == nil {
		t.Error("disable --now, exit 1: err = nil, want an error")
	}
	calls := procdCalls(t, dir)
	if n := len(calls); n != 3 || !slices.Equal(calls[2].Args, []string{"disable"}) {
		t.Errorf("calls = %+v, want enable, stop, disable", calls)
	}
	missing := initsys.Procd{Dir: t.TempDir()}
	if err := missing.Restart(t.Context(), "stvor-agent.service"); err == nil {
		t.Error("missing script: err = nil, want an error")
	}
}

// TestProcdUnitName: a unit name that is not a plain service name never
// becomes a path outside Dir.
func TestProcdUnitName(t *testing.T) {
	t.Parallel()
	p, dir := procdFake(t, scenario{})
	for _, unit := range []string{"", ".service", "../stvor-agent.service", "a/b.service", "-x.service"} {
		if err := p.Enable(t.Context(), unit); err == nil {
			t.Errorf("Enable(%q): err = nil, want an error", unit)
		}
		if got := p.IsActive(t.Context(), unit); got != "unknown" {
			t.Errorf("IsActive(%q) = %q, want unknown", unit, got)
		}
	}
	if calls := procdCalls(t, dir); len(calls) != 0 {
		t.Errorf("calls = %+v, want none", calls)
	}
}

// TestProcdIsActive: `running` exits 0 for a running service and non-zero
// otherwise (rc.common service_running); no script — unknown.
func TestProcdIsActive(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		sc   scenario
		want string
	}{
		{"running", scenario{}, "active"},
		{"not running", scenario{Exit: 1}, "inactive"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, dir := procdFake(t, tc.sc)
			if got := p.IsActive(t.Context(), "stvor-agent-telemetry.service"); got != tc.want {
				t.Errorf("IsActive = %q, want %q", got, tc.want)
			}
			calls := procdCalls(t, dir)
			if len(calls) != 1 || calls[0].Script != "stvor-agent-telemetry" || !slices.Equal(calls[0].Args, []string{"running"}) {
				t.Errorf("calls = %+v", calls)
			}
		})
	}
	missing := initsys.Procd{Dir: t.TempDir()}
	if got := missing.IsActive(t.Context(), "stvor-agent.service"); got != "unknown" {
		t.Errorf("missing script: IsActive = %q, want unknown", got)
	}
}

// TestFor: the family picks the init system.
func TestFor(t *testing.T) {
	t.Parallel()
	if _, ok := initsys.For(platform.Debian, io.Discard).(initsys.Systemd); !ok {
		t.Error("Debian: want Systemd")
	}
	p, ok := initsys.For(platform.OpenWrt, io.Discard).(initsys.Procd)
	if !ok {
		t.Fatal("OpenWrt: want Procd")
	}
	if p.Dir != "" {
		t.Errorf("Procd.Dir = %q, want the default", p.Dir)
	}
	if initsys.DefaultInitDir != "/etc/init.d" {
		t.Errorf("DefaultInitDir = %q", initsys.DefaultInitDir)
	}
}

type procdCall struct {
	Script string
	Args   []string
	Env    []string
}

// procdCalls reads the calls the fake scripts appended, in order.
func procdCalls(t *testing.T, dir string) []procdCall {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "calls.jsonl"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var calls []procdCall
	dec := json.NewDecoder(bytes.NewReader(data))
	for dec.More() {
		var c procdCall
		if err := dec.Decode(&c); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, c)
	}
	return calls
}
