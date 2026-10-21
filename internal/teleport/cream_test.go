package teleport_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/iamletenkov/stvor-agent/internal/teleport"
)

// The fake cream is this test binary itself, reached through a symlink named
// fakeName in a temporary directory. TestMain recognises it by the base name of
// argv[0] — not by an environment variable, because the adapter starts cream
// with an exact two-variable environment and the test checks exactly that. The
// scenario lies next to the symlink, and the fake writes what it saw there.
const fakeName = "cream"

// scenario is what the fake cream does.
type scenario struct {
	Exit       int    `json:"exit"`
	Stdout     string `json:"stdout"`
	StdoutFile string `json:"stdout_file"` // relative to testdata
	StdoutSize int    `json:"stdout_size"` // print this many bytes of 'x'
	Stderr     string `json:"stderr"`
	Hang       bool   `json:"hang"` // start a grandchild in the group and sleep
	// OwnGroup (with Hang) behaves like the real cream: the grandchild (the
	// action) runs in a process group of its own, and SIGTERM makes the fake
	// kill that group and exit. SIGKILL of the fake would orphan it.
	OwnGroup bool `json:"own_group"`
	// IgnoreTerm (with Hang) ignores SIGTERM: only SIGKILL stops the fake.
	IgnoreTerm bool `json:"ignore_term"`
}

// record is what the fake cream saw.
type record struct {
	Args          []string `json:"args"`
	Env           []string `json:"env"`
	Stdin         string   `json:"stdin"`
	StdinCharDev  bool     `json:"stdin_char_dev"`
	Dir           string   `json:"dir"`
	GrandchildPID int      `json:"grandchild_pid"`
}

func TestMain(m *testing.M) {
	if filepath.Base(os.Args[0]) == fakeName {
		os.Exit(fakeCream())
	}
	os.Exit(m.Run())
}

func fakeCream() int {
	if len(os.Args) > 1 && os.Args[1] == "--fake-sleeper" {
		time.Sleep(time.Hour)
		return 0
	}
	dir := filepath.Dir(os.Args[0])
	var sc scenario
	data, err := os.ReadFile(filepath.Join(dir, "scenario.json"))
	if err != nil {
		return 120
	}
	if err := json.Unmarshal(data, &sc); err != nil {
		return 121
	}
	rec := record{Args: os.Args[1:], Env: os.Environ()}
	if fi, err := os.Stdin.Stat(); err == nil {
		rec.StdinCharDev = fi.Mode()&os.ModeCharDevice != 0
	}
	in, _ := io.ReadAll(os.Stdin)
	rec.Stdin = string(in)
	rec.Dir, _ = os.Getwd()
	if sc.IgnoreTerm {
		signal.Ignore(syscall.SIGTERM)
	}
	if sc.Hang {
		child := exec.Command(os.Args[0], "--fake-sleeper")
		if sc.OwnGroup {
			child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		}
		if err := child.Start(); err != nil {
			return 122
		}
		rec.GrandchildPID = child.Process.Pid
		if sc.OwnGroup {
			term := make(chan os.Signal, 1)
			signal.Notify(term, syscall.SIGTERM)
			go func() {
				<-term
				_ = syscall.Kill(-child.Process.Pid, syscall.SIGKILL)
				os.Exit(143)
			}()
		}
	}
	out, _ := json.Marshal(rec)
	if err := os.WriteFile(filepath.Join(dir, "record.json"), out, 0o600); err != nil {
		return 123
	}
	if sc.Hang {
		time.Sleep(time.Hour)
	}
	switch {
	case sc.StdoutFile != "":
		b, err := os.ReadFile(sc.StdoutFile)
		if err != nil {
			return 124
		}
		_, _ = os.Stdout.Write(b)
	case sc.StdoutSize > 0:
		_, _ = os.Stdout.Write(bytes.Repeat([]byte("x"), sc.StdoutSize))
	default:
		_, _ = os.Stdout.WriteString(sc.Stdout)
	}
	_, _ = os.Stderr.WriteString(sc.Stderr)
	return sc.Exit
}

// fake sets up the fake cream with a scenario and returns the adapter and the
// directory where record.json appears.
func fake(t *testing.T, sc scenario) (*teleport.CreamTeleport, string) {
	t.Helper()
	if sc.StdoutFile != "" {
		abs, err := filepath.Abs(filepath.Join("testdata", sc.StdoutFile))
		if err != nil {
			t.Fatal(err)
		}
		sc.StdoutFile = abs
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Symlink(self, filepath.Join(dir, fakeName)); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(sc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scenario.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return teleport.NewCream(filepath.Join(dir, fakeName)), dir
}

func readRecord(t *testing.T, dir string) record {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "record.json"))
	if err != nil {
		t.Fatalf("fake cream не записал record.json: %v", err)
	}
	var rec record
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

const secret = "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789_-abcdZ"

// TestCleanCall: argv exactly as in spec §5.7, cream flags before the
// extension name, the environment exactly PATH and LANG, stdin /dev/null, and
// the token of the parent nowhere in argv, environment or stdin.
func TestCleanCall(t *testing.T) {
	t.Setenv("STVOR_AGENT_TOKEN", secret)
	t.Setenv("CREAM_ROOT", "/tmp/should-not-leak")
	wantEnv := []string{"LANG=C.UTF-8", "PATH=/usr/sbin:/usr/bin:/sbin:/bin"}
	tests := []struct {
		name     string
		sc       scenario
		call     func(*teleport.CreamTeleport) error
		wantArgs []string
	}{
		{
			name: "managed-status",
			sc:   scenario{StdoutFile: "managed-status-joined.json"},
			call: func(c *teleport.CreamTeleport) error {
				_, err := c.Status(context.Background())
				return err
			},
			wantArgs: []string{"run", "teleport", "managed-status"},
		},
		{
			name: "set-managed",
			sc:   scenario{StdoutFile: "set-managed-applied.json"},
			call: func(c *teleport.CreamTeleport) error {
				_, err := c.SetManaged(context.Background(), teleport.Labels{Env: "prod", Space: "sp-1"})
				return err
			},
			wantArgs: []string{"run", "--lock-timeout", "20s", "teleport", "set-managed", "env=prod", "space=sp-1"},
		},
		{
			name: "actions",
			sc:   scenario{StdoutFile: "cream/actions-teleport.json"},
			call: func(c *teleport.CreamTeleport) error {
				return c.CheckActions(context.Background())
			},
			wantArgs: []string{"actions", "teleport", "--json"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, dir := fake(t, tc.sc)
			if err := tc.call(c); err != nil {
				t.Fatalf("вызов: %v", err)
			}
			rec := readRecord(t, dir)
			if !slices.Equal(rec.Args, tc.wantArgs) {
				t.Errorf("argv = %q, want %q", rec.Args, tc.wantArgs)
			}
			env := slices.Clone(rec.Env)
			slices.Sort(env)
			if !slices.Equal(env, wantEnv) {
				t.Errorf("env = %q, want %q", env, wantEnv)
			}
			if !rec.StdinCharDev || rec.Stdin != "" {
				t.Errorf("stdin не /dev/null: char=%v, %q", rec.StdinCharDev, rec.Stdin)
			}
			if rec.Dir != "/" {
				t.Errorf("cwd = %q, want /", rec.Dir)
			}
			all := strings.Join(rec.Args, " ") + strings.Join(rec.Env, " ") + rec.Stdin
			if strings.Contains(all, secret) || strings.Contains(all, "should-not-leak") {
				t.Error("окружение родителя протекло в cream")
			}
		})
	}
}

func TestStatusFixtures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		file string
		want teleport.Status
	}{
		{"managed-status-joined.json", teleport.Status{Managed: teleport.Labels{Env: "prod", Space: "sp-1"}, Joined: true, Active: true}},
		{"managed-status-pending.json", teleport.Status{Managed: teleport.Labels{Env: "prod", Space: "sp-1"}, Pending: true, Joined: true, Active: true}},
		{"managed-status-not-joined.json", teleport.Status{}},
		{"managed-status-inactive.json", teleport.Status{Managed: teleport.Labels{Env: "prod", Space: "sp-1"}, Joined: true}},
	}
	for _, tc := range tests {
		t.Run(tc.file, func(t *testing.T) {
			t.Parallel()
			c, _ := fake(t, scenario{StdoutFile: tc.file})
			got, err := c.Status(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestSetManagedResults(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		sc      scenario
		want    string
		wantErr error
	}{
		{"applied", scenario{StdoutFile: "set-managed-applied.json"}, teleport.ResultApplied, nil},
		{"unchanged", scenario{StdoutFile: "set-managed-unchanged.json"}, teleport.ResultUnchanged, nil},
		{"deferred", scenario{StdoutFile: "set-managed-deferred.json"}, teleport.ResultDeferred, nil},
		{"unconfirmed exit 1", scenario{Exit: 1, StdoutFile: "set-managed-unconfirmed.json"}, teleport.ResultUnconfirmed, nil},
		{"unconfirmed with exit 0", scenario{StdoutFile: "set-managed-unconfirmed.json"}, "", teleport.ErrBadOutput},
		{"applied with exit 1", scenario{Exit: 1, StdoutFile: "set-managed-applied.json"}, "", teleport.ErrBadOutput},
		{"exit 1 not json", scenario{Exit: 1, Stdout: "oops"}, "", teleport.ErrBadOutput},
		{"exit 2 with json", scenario{Exit: 2, Stdout: `{"schema":1,"error":"env: неверное значение"}`}, "", teleport.ErrInvalidInput},
		{"exit 3", scenario{Exit: 3, StdoutFile: "set-managed-applied.json"}, "", teleport.ErrActionFailed},
		{"exit 63", scenario{Exit: 63}, "", teleport.ErrActionFailed},
		{"exit 64", scenario{Exit: 64}, "", teleport.ErrCream},
		{"exit 70", scenario{Exit: 70}, "", teleport.ErrCream},
		{"exit 75", scenario{Exit: 75}, "", teleport.ErrLockBusy},
		{"exit 76", scenario{Exit: 76}, "", teleport.ErrProtocol},
		{"exit 79", scenario{Exit: 79}, "", teleport.ErrCream},
		{"exit 80", scenario{Exit: 80}, "", teleport.ErrCream},
		{"status object without result", scenario{StdoutFile: "managed-status-joined.json"}, "", teleport.ErrBadOutput},
		{"stdout over 64 KiB", scenario{StdoutSize: 64<<10 + 1}, "", teleport.ErrBadOutput},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, _ := fake(t, tc.sc)
			got, err := c.SetManaged(context.Background(), teleport.Labels{Env: "prod", Space: "sp-1"})
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Result != tc.want {
				t.Errorf("result = %q, want %q", got.Result, tc.want)
			}
		})
	}
}

func TestStatusErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		sc      scenario
		wantErr error
	}{
		{"exit 1", scenario{Exit: 1, StdoutFile: "managed-status-joined.json"}, teleport.ErrActionFailed},
		{"exit 2", scenario{Exit: 2, Stdout: `{}`}, teleport.ErrInvalidInput},
		{"exit 70", scenario{Exit: 70}, teleport.ErrCream},
		{"exit 75", scenario{Exit: 75}, teleport.ErrLockBusy},
		{"exit 76", scenario{Exit: 76}, teleport.ErrProtocol},
		{"not json", scenario{Stdout: "joined: yes\n"}, teleport.ErrBadOutput},
		{"foreign schema", scenario{Stdout: `{"schema":2,"managed":{},"pending":false,"joined":true,"active":true}`}, teleport.ErrBadOutput},
		{"extra key", scenario{Stdout: `{"schema":1,"managed":{},"pending":false,"joined":true,"active":true,"x":1}`}, teleport.ErrBadOutput},
		{"two objects", scenario{Stdout: `{"schema":1,"managed":{},"pending":false,"joined":true,"active":true}{}`}, teleport.ErrBadOutput},
		{"set-managed object", scenario{StdoutFile: "set-managed-applied.json"}, teleport.ErrBadOutput},
		{"stdout over 64 KiB", scenario{StdoutSize: 64<<10 + 1}, teleport.ErrBadOutput},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, _ := fake(t, tc.sc)
			_, err := c.Status(context.Background())
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestMissingCream(t *testing.T) {
	t.Parallel()
	c := teleport.NewCream(filepath.Join(t.TempDir(), "no-cream"))
	if _, err := c.Status(context.Background()); !errors.Is(err, teleport.ErrCream) {
		t.Errorf("Status: err = %v, want ErrCream", err)
	}
	if _, err := c.SetManaged(context.Background(), teleport.Labels{Env: "a", Space: "b"}); !errors.Is(err, teleport.ErrCream) {
		t.Errorf("SetManaged: err = %v, want ErrCream", err)
	}
	if err := c.CheckActions(context.Background()); err == nil || errors.Is(err, teleport.ErrActionsMissing) {
		t.Errorf("CheckActions: err = %v, want unknown", err)
	}
}

// TestSetManagedRejectsBadLabels: values that cannot be T labels never reach
// argv (nor an argument starting with "-").
func TestSetManagedRejectsBadLabels(t *testing.T) {
	t.Parallel()
	for _, l := range []teleport.Labels{
		{Env: "", Space: "sp-1"},
		{Env: "prod", Space: ""},
		{Env: "-x", Space: "sp-1"},
		{Env: "Prod", Space: "sp-1"},
		{Env: "prod", Space: "sp 1"},
		{Env: "prod", Space: "sp=1"},
		{Env: strings.Repeat("a", 64), Space: "sp-1"},
	} {
		c, dir := fake(t, scenario{StdoutFile: "set-managed-applied.json"})
		_, err := c.SetManaged(context.Background(), l)
		if !errors.Is(err, teleport.ErrInvalidInput) {
			t.Errorf("%+v: err = %v, want ErrInvalidInput", l, err)
		}
		if _, statErr := os.Stat(filepath.Join(dir, "record.json")); statErr == nil {
			t.Errorf("%+v: cream запущен", l)
		}
	}
}

// TestTimeoutKillsProcessGroup: on timeout the whole process group dies, the
// grandchild included, and the call returns ErrTimeout quickly.
func TestTimeoutKillsProcessGroup(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		call func(*teleport.CreamTeleport) error
	}{
		{"managed-status", func(c *teleport.CreamTeleport) error {
			c.StatusTimeout = 300 * time.Millisecond
			_, err := c.Status(context.Background())
			return err
		}},
		{"set-managed", func(c *teleport.CreamTeleport) error {
			c.SetTimeout = 300 * time.Millisecond
			_, err := c.SetManaged(context.Background(), teleport.Labels{Env: "prod", Space: "sp-1"})
			return err
		}},
		{"actions", func(c *teleport.CreamTeleport) error {
			c.ActionsTimeout = 300 * time.Millisecond
			return c.CheckActions(context.Background())
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, dir := fake(t, scenario{Hang: true})
			start := time.Now()
			err := tc.call(c)
			if !errors.Is(err, teleport.ErrTimeout) || !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("err = %v, want ErrTimeout", err)
			}
			if el := time.Since(start); el > 10*time.Second {
				t.Errorf("вызов длился %v", el)
			}
			rec := readRecord(t, dir)
			if rec.GrandchildPID == 0 {
				t.Fatal("внук не запущен")
			}
			waitDead(t, rec.GrandchildPID)
		})
	}
}

// TestTimeoutStopsCream: cream runs the action in a process group of its own
// and kills it on SIGTERM (cream docs/reference/cli.md, «Поведение процесса»), so on
// timeout the adapter sends SIGTERM to the group of cream and SIGKILL only
// after KillGrace — a SIGKILL first would orphan the action (found by the
// integration test against the real cream, plan Task 15).
func TestTimeoutStopsCream(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		sc   scenario
	}{
		{"cream убивает группу действия по SIGTERM", scenario{Hang: true, OwnGroup: true}},
		{"cream не реагирует на SIGTERM — SIGKILL после KillGrace", scenario{Hang: true, IgnoreTerm: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, dir := fake(t, tc.sc)
			c.StatusTimeout = 300 * time.Millisecond
			c.KillGrace = 500 * time.Millisecond
			start := time.Now()
			_, err := c.Status(context.Background())
			if !errors.Is(err, teleport.ErrTimeout) {
				t.Fatalf("err = %v, want ErrTimeout", err)
			}
			if el := time.Since(start); el > 5*time.Second {
				t.Errorf("вызов длился %v", el)
			}
			rec := readRecord(t, dir)
			if rec.GrandchildPID == 0 {
				t.Fatal("внук не запущен")
			}
			waitDead(t, rec.GrandchildPID)
		})
	}
}

// waitDead waits until pid is gone or a zombie (in a container without an init
// the orphan is never reaped, but a zombie is dead).
func waitDead(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if err != nil {
			return
		}
		if i := bytes.LastIndexByte(stat, ')'); i > 0 && i+2 < len(stat) && stat[i+2] == 'Z' {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Errorf("процесс %d жив после тайм-аута", pid)
}

// TestCancelledContext: managed-status follows the caller's context; a
// set-managed already asked for is not cut by it (spec §5.8 — the service
// waits for the child cream run), only by its own timeout.
func TestCancelledContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	c, _ := fake(t, scenario{StdoutFile: "managed-status-joined.json"})
	if _, err := c.Status(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Status: err = %v, want context.Canceled", err)
	}

	c, _ = fake(t, scenario{StdoutFile: "set-managed-applied.json"})
	got, err := c.SetManaged(ctx, teleport.Labels{Env: "prod", Space: "sp-1"})
	if err != nil || got.Result != teleport.ResultApplied {
		t.Errorf("SetManaged: %+v, %v; want applied", got, err)
	}
}

func TestCheckActions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		sc          scenario
		wantMissing bool
		wantOK      bool
	}{
		{name: "ok", sc: scenario{StdoutFile: "cream/actions-teleport.json"}, wantOK: true},
		{name: "missing (3.x)", sc: scenario{StdoutFile: "cream/actions-teleport-3x.json"}, wantMissing: true},
		{name: "missing set-managed", sc: scenario{Stdout: `{"ok":true,"result":{"actions":[{"name":"managed-status"}]}}`}, wantMissing: true},
		{name: "unknown: not json", sc: scenario{Stdout: "managed-status set-managed\n"}},
		{name: "unknown: exit 66", sc: scenario{Exit: 66, Stdout: `{"ok":false,"result":null,"error":{"code":"not_found.extension"}}`}},
		{name: "unknown: exit 64 with a full list", sc: scenario{Exit: 64, StdoutFile: "cream/actions-teleport.json"}},
		{name: "unknown: exit 1", sc: scenario{Exit: 1}},
		{name: "unknown: other form", sc: scenario{Stdout: `{"ok":true,"result":"managed-status,set-managed"}`}},
		{name: "unknown: over 64 KiB", sc: scenario{StdoutSize: 64<<10 + 1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, _ := fake(t, tc.sc)
			err := c.CheckActions(context.Background())
			switch {
			case tc.wantOK:
				if err != nil {
					t.Fatalf("err = %v, want nil", err)
				}
			case tc.wantMissing:
				if !errors.Is(err, teleport.ErrActionsMissing) {
					t.Fatalf("err = %v, want ErrActionsMissing", err)
				}
			default:
				if err == nil || errors.Is(err, teleport.ErrActionsMissing) {
					t.Fatalf("err = %v, want unknown (не nil и не ErrActionsMissing)", err)
				}
			}
		})
	}
}

// TestRunEnvelopeSample: the sample `cream run --json` envelope (the form of
// the cream golden run-*.json) carries the managed-status contract file in
// result.output — the same bytes after json.Compact (the envelope is
// indented, the contract file is one line). The agent itself calls run
// without --json; the sample is kept for the check against cream T15.
func TestRunEnvelopeSample(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join("testdata", "cream", "run-managed-status.json"))
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		Command string `json:"command"`
		OK      bool   `json:"ok"`
		Result  struct {
			Action   string          `json:"action"`
			ExitCode int             `json:"exit_code"`
			Output   json.RawMessage `json:"output"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatal(err)
	}
	if env.Command != "run" || !env.OK || env.Result.Action != "teleport/managed-status" || env.Result.ExitCode != 0 {
		t.Errorf("конверт: %+v", env)
	}
	contract, err := os.ReadFile(filepath.Join("testdata", "managed-status-joined.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got, want bytes.Buffer
	if err := json.Compact(&got, env.Result.Output); err != nil {
		t.Fatal(err)
	}
	if err := json.Compact(&want, contract); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes(), want.Bytes()) {
		t.Errorf("output конверта:\n%s\nфайл-образец:\n%s", got.Bytes(), want.Bytes())
	}
	c, _ := fake(t, scenario{Stdout: got.String() + "\n"})
	if _, err := c.Status(context.Background()); err != nil {
		t.Errorf("output конверта не принят строгим разбором: %v", err)
	}
}

func TestCreamImplementsTeleport(t *testing.T) {
	t.Parallel()
	var _ teleport.Teleport = teleport.NewCream("/usr/bin/cream")
	if got := teleport.NewCream("").Path; got != "/usr/bin/cream" {
		t.Errorf("путь по умолчанию = %q", got)
	}
}
