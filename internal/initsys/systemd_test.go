package initsys_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/iamletenkov/stvor-agent/internal/initsys"
)

// The fake systemctl is this test binary reached through a symlink named
// fakeName; TestMain recognises it by argv[0] (the environment of the child is
// exactly two variables, so a marker variable would not get through).
const fakeName = "systemctl"

type scenario struct {
	Exit   int    `json:"exit"`
	Stdout string `json:"stdout"`
	// ExitBy overrides Exit for one verb (the first argument): the procd
	// scripts answer `running` apart from `restart`.
	ExitBy map[string]int `json:"exit_by,omitempty"`
}

type record struct {
	Args []string `json:"args"`
	Env  []string `json:"env"`
}

func TestMain(m *testing.M) {
	if b := filepath.Base(os.Args[0]); b == fakeName || procdScripts[b] {
		os.Exit(fakeSystemctl())
	}
	os.Exit(m.Run())
}

func fakeSystemctl() int {
	dir := filepath.Dir(os.Args[0])
	var sc scenario
	data, err := os.ReadFile(filepath.Join(dir, "scenario.json"))
	if err != nil || json.Unmarshal(data, &sc) != nil {
		return 120
	}
	out, _ := json.Marshal(record{Args: os.Args[1:], Env: os.Environ()})
	if os.WriteFile(filepath.Join(dir, "record.json"), out, 0o600) != nil {
		return 121
	}
	// Every call in order, with the name it was started by (procd scripts).
	call, _ := json.Marshal(struct {
		Script string
		Args   []string
		Env    []string
	}{filepath.Base(os.Args[0]), os.Args[1:], os.Environ()})
	f, err := os.OpenFile(filepath.Join(dir, "calls.jsonl"), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return 122
	}
	_, err = f.Write(append(call, '\n'))
	if f.Close() != nil || err != nil {
		return 122
	}
	_, _ = os.Stdout.WriteString(sc.Stdout)
	if len(os.Args) > 1 {
		if code, ok := sc.ExitBy[os.Args[1]]; ok {
			return code
		}
	}
	return sc.Exit
}

func fake(t *testing.T, sc scenario) (initsys.Systemd, string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, fakeName)
	if err := os.Symlink(self, path); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(sc)
	if err := os.WriteFile(filepath.Join(dir, "scenario.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return initsys.Systemd{Path: path}, dir
}

func seen(t *testing.T, dir string) record {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "record.json"))
	if err != nil {
		t.Fatalf("fake systemctl did not run: %v", err)
	}
	var r record
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestExecCommands(t *testing.T) {
	// The parent environment carries a secret; the child must not see it.
	t.Setenv("STVOR_TEST_SECRET", "must-not-leak")
	const unit = "stvor-agent.service"
	tests := []struct {
		name string
		call func(context.Context, initsys.Systemd) error
		want []string
	}{
		{"enable", func(ctx context.Context, e initsys.Systemd) error { return e.Enable(ctx, unit) }, []string{"enable", unit}},
		{"restart", func(ctx context.Context, e initsys.Systemd) error { return e.Restart(ctx, unit) }, []string{"restart", unit}},
		{"disable --now", func(ctx context.Context, e initsys.Systemd) error { return e.DisableNow(ctx, unit) }, []string{"disable", "--now", unit}},
		{"start", func(ctx context.Context, e initsys.Systemd) error { return e.Start(ctx, unit) }, []string{"start", unit}},
		{"stop", func(ctx context.Context, e initsys.Systemd) error { return e.Stop(ctx, unit) }, []string{"stop", unit}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e, dir := fake(t, scenario{})
			if err := tc.call(t.Context(), e); err != nil {
				t.Fatalf("err = %v", err)
			}
			r := seen(t, dir)
			if !slices.Equal(r.Args, tc.want) {
				t.Errorf("argv = %q, want %q", r.Args, tc.want)
			}
			wantEnv := []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C.UTF-8"}
			if !slices.Equal(r.Env, wantEnv) {
				t.Errorf("env = %q, want %q", r.Env, wantEnv)
			}
		})
	}
}

func TestExecFailure(t *testing.T) {
	t.Parallel()
	e, _ := fake(t, scenario{Exit: 1})
	if err := e.Enable(t.Context(), "stvor-agent.service"); err == nil {
		t.Error("exit 1: err = nil, want an error")
	}
	missing := initsys.Systemd{Path: filepath.Join(t.TempDir(), "absent")}
	if err := missing.Restart(t.Context(), "stvor-agent.service"); err == nil {
		t.Error("missing binary: err = nil, want an error")
	}
}

func TestExecIsActive(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		sc   scenario
		want string
	}{
		{"active", scenario{Stdout: "active\n"}, "active"},
		{"inactive exits 3", scenario{Stdout: "inactive\n", Exit: 3}, "inactive"},
		{"failed", scenario{Stdout: "failed\n", Exit: 3}, "failed"},
		{"garbage", scenario{Stdout: "Active: <b>\n"}, "unknown"},
		{"empty", scenario{Exit: 4}, "unknown"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e, dir := fake(t, tc.sc)
			if got := e.IsActive(t.Context(), "stvor-agent.service"); got != tc.want {
				t.Errorf("IsActive = %q, want %q", got, tc.want)
			}
			if r := seen(t, dir); !slices.Equal(r.Args, []string{"is-active", "stvor-agent.service"}) {
				t.Errorf("argv = %q", r.Args)
			}
		})
	}
	missing := initsys.Systemd{Path: filepath.Join(t.TempDir(), "absent")}
	if got := missing.IsActive(t.Context(), "stvor-agent.service"); got != "unknown" {
		t.Errorf("missing binary: IsActive = %q, want unknown", got)
	}
}

func TestFake(t *testing.T) {
	t.Parallel()
	f := &initsys.Fake{Active: "active", RestartErr: os.ErrPermission}
	ctx := t.Context()
	_ = f.Enable(ctx, "u")
	if err := f.Restart(ctx, "u"); err == nil {
		t.Error("RestartErr not returned")
	}
	_ = f.DisableNow(ctx, "u")
	_ = f.Start(ctx, "t")
	_ = f.Stop(ctx, "t")
	if got := f.IsActive(ctx, "u"); got != "active" {
		t.Errorf("IsActive = %q", got)
	}
	want := []string{"enable u", "restart u", "disable --now u", "start t", "stop t", "is-active u"}
	if got := f.Calls(); !slices.Equal(got, want) {
		t.Errorf("Calls = %q, want %q", got, want)
	}
	if got := (&initsys.Fake{}).IsActive(ctx, "u"); got != "inactive" {
		t.Errorf("zero Fake IsActive = %q, want inactive", got)
	}
}
