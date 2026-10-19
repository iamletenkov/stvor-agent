//go:build integration

// The adapter against the real cream (plan Task 15): run by
// test/integration/run.sh inside a debian:12 container with the cream package
// and the fake extension test/integration/catalog/teleport-fake installed as
// "teleport". The fake actions answer from /var/lib/teleport-fake/<action>.*,
// which every test writes before the call. Outside that container the tests
// skip: SA_IT_CREAM names the cream binary to drive.
package teleport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

const fakeDir = "/var/lib/teleport-fake"

func itCream(t *testing.T) string {
	t.Helper()
	p := os.Getenv("SA_IT_CREAM")
	if p == "" {
		t.Skip("SA_IT_CREAM не задан: тест идёт только в контейнере make test-integration")
	}
	return p
}

// itAdapter is the adapter as the service builds it, with the stderr of cream
// in the test log.
func itAdapter(t *testing.T) *CreamTeleport {
	t.Helper()
	c := NewCream(itCream(t))
	c.Stderr = testLog{t}
	return c
}

type testLog struct{ t *testing.T }

func (w testLog) Write(p []byte) (int, error) {
	w.t.Logf("cream stderr: %s", bytes.TrimRight(p, "\n"))
	return len(p), nil
}

// scenario makes the fake action answer with out and code after sleeping
// sleep seconds (0 — no sleep); out nil — print nothing.
func scenario(t *testing.T, action string, out []byte, code, sleep int) {
	t.Helper()
	if err := os.MkdirAll(fakeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(fakeDir, action)
	for _, ext := range []string{".out", ".code", ".sleep", ".argv"} {
		_ = os.Remove(base + ext)
	}
	write := func(ext string, data []byte) {
		if err := os.WriteFile(base+ext, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if out != nil {
		write(".out", out)
	}
	write(".code", []byte(strconv.Itoa(code)))
	if sleep > 0 {
		write(".sleep", []byte(strconv.Itoa(sleep)))
	}
	t.Cleanup(func() {
		for _, ext := range []string{".out", ".code", ".sleep", ".argv"} {
			_ = os.Remove(base + ext)
		}
	})
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestIntegrationStatusFixtures(t *testing.T) {
	c := itAdapter(t)
	for _, name := range []string{"joined", "pending", "not-joined", "inactive"} {
		t.Run(name, func(t *testing.T) {
			data := fixture(t, "managed-status-"+name+".json")
			want, err := parseOutput(data, false)
			if err != nil {
				t.Fatalf("образец не разбирается: %v", err)
			}
			scenario(t, "managed-status", data, 0, 0)
			got, err := c.Status(context.Background())
			if err != nil {
				t.Fatalf("Status: %v", err)
			}
			if got != want.Status {
				t.Fatalf("Status = %+v, ожидалось %+v", got, want.Status)
			}
		})
	}
}

func TestIntegrationSetManagedFixtures(t *testing.T) {
	c := itAdapter(t)
	tests := []struct {
		name string
		code int
	}{
		{"applied", 0}, {"unchanged", 0}, {"deferred", 0}, {"unconfirmed", 1},
	}
	labels := Labels{Env: "prod", Space: "sp-1"}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data := fixture(t, "set-managed-"+tc.name+".json")
			want, err := parseOutput(data, true)
			if err != nil {
				t.Fatalf("образец не разбирается: %v", err)
			}
			scenario(t, "set-managed", data, tc.code, 0)
			got, err := c.SetManaged(context.Background(), labels)
			if err != nil {
				t.Fatalf("SetManaged: %v", err)
			}
			if got != want {
				t.Fatalf("SetManaged = %+v, ожидалось %+v", got, want)
			}
			argv, err := os.ReadFile(filepath.Join(fakeDir, "set-managed.argv"))
			if err != nil {
				t.Fatal(err)
			}
			if string(argv) != "env=prod\nspace=sp-1\n" {
				t.Fatalf("argv действия = %q, ожидалось env=prod space=sp-1", argv)
			}
		})
	}
}

func TestIntegrationExitCodes(t *testing.T) {
	c := itAdapter(t)
	tests := []struct {
		name   string
		action string
		out    []byte
		code   int
		want   error
	}{
		{"managed-status код 1", "managed-status", nil, 1, ErrActionFailed},
		{"managed-status код 2 с объектом", "managed-status", []byte(`{"error":"x"}`), 2, ErrInvalidInput},
		{"managed-status не JSON → 76", "managed-status", []byte("not json\n"), 0, ErrProtocol},
		{"managed-status пустой stdout → 76", "managed-status", nil, 0, ErrProtocol},
		{"managed-status два объекта → 76", "managed-status", []byte("{}\n{}\n"), 0, ErrProtocol},
		{"managed-status код 100 → 76", "managed-status", nil, 100, ErrProtocol},
		{"managed-status объект не схемы 1", "managed-status", []byte(`{"schema":2}`), 0, ErrBadOutput},
		{"set-managed код 3", "set-managed", nil, 3, ErrActionFailed},
		{"set-managed код 1 без unconfirmed", "set-managed",
			fixture(t, "set-managed-applied.json"), 1, ErrBadOutput},
		{"set-managed не JSON → 76", "set-managed", []byte("oops"), 0, ErrProtocol},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scenario(t, tc.action, tc.out, tc.code, 0)
			var err error
			if tc.action == "managed-status" {
				_, err = c.Status(context.Background())
			} else {
				_, err = c.SetManaged(context.Background(), Labels{Env: "prod", Space: "sp-1"})
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("ошибка = %v, ожидалось %v", err, tc.want)
			}
		})
	}
}

func TestIntegrationTimeout(t *testing.T) {
	t.Run("тайм-аут адаптера убивает группу", func(t *testing.T) {
		c := itAdapter(t)
		c.StatusTimeout = time.Second
		scenario(t, "managed-status", fixture(t, "managed-status-joined.json"), 0, 30)
		start := time.Now()
		_, err := c.Status(context.Background())
		if !errors.Is(err, ErrTimeout) {
			t.Fatalf("ошибка = %v, ожидалось ErrTimeout", err)
		}
		if d := time.Since(start); d > 8*time.Second {
			t.Fatalf("Status вернулся через %s", d)
		}
		waitNoProcess(t, "sleep", 3*time.Second)
	})
	t.Run("тайм-аут действия в cream → 79", func(t *testing.T) {
		c := itAdapter(t) // 15 s of the adapter > timeout: 5s of the manifest
		scenario(t, "managed-status", fixture(t, "managed-status-joined.json"), 0, 30)
		start := time.Now()
		_, err := c.Status(context.Background())
		if !errors.Is(err, ErrCream) || !strings.Contains(err.Error(), "код 79") {
			t.Fatalf("ошибка = %v, ожидалось ErrCream (код 79)", err)
		}
		if d := time.Since(start); d < 4*time.Second || d > 14*time.Second {
			t.Fatalf("Status вернулся через %s, ожидалось около 5 с", d)
		}
		waitNoProcess(t, "sleep", 3*time.Second)
	})
}

// waitNoProcess fails if a process named comm is still alive after d.
func waitNoProcess(t *testing.T, comm string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		pids := processes(comm)
		if len(pids) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("после тайм-аута живы процессы %s: %v", comm, pids)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func processes(comm string) []string {
	var pids []string
	dirs, _ := filepath.Glob("/proc/[0-9]*/comm")
	for _, f := range dirs {
		data, err := os.ReadFile(f)
		if err != nil || strings.TrimSpace(string(data)) != comm {
			continue
		}
		// A zombie is dead, it only waits to be reaped.
		stat, err := os.ReadFile(filepath.Join(filepath.Dir(f), "stat"))
		if i := bytes.LastIndexByte(stat, ')'); err == nil && i > 0 && i+2 < len(stat) && stat[i+2] == 'Z' {
			continue
		}
		pids = append(pids, filepath.Base(filepath.Dir(f)))
	}
	return pids
}

func TestIntegrationLockBusy(t *testing.T) {
	c := itAdapter(t)
	_ = os.MkdirAll(fakeDir, 0o755)
	started := filepath.Join(fakeDir, "hold.started")
	release := filepath.Join(fakeDir, "hold.release")
	_ = os.Remove(started)

	hold := exec.Command(c.Path, "run", "teleport", "hold")
	hold.Env = creamEnv()
	hold.Dir = "/"
	hold.Stderr = testLog{t}
	if err := hold.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- hold.Wait() }()
	t.Cleanup(func() {
		_ = os.WriteFile(release, nil, 0o644)
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = hold.Process.Kill()
		}
	})
	for i := 0; ; i++ {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if i > 100 {
			t.Fatal("hold не взял блокировку за 10 с")
		}
		time.Sleep(100 * time.Millisecond)
	}

	scenario(t, "set-managed", fixture(t, "set-managed-applied.json"), 0, 0)
	start := time.Now()
	_, err := c.SetManaged(context.Background(), Labels{Env: "prod", Space: "sp-1"})
	if !errors.Is(err, ErrLockBusy) {
		t.Fatalf("ошибка = %v, ожидалось ErrLockBusy", err)
	}
	if d := time.Since(start); d < 19*time.Second {
		t.Fatalf("75 через %s: --lock-timeout 20s не дошёл до cream", d)
	}
	if _, err := os.Stat(filepath.Join(fakeDir, "set-managed.argv")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("set-managed запустился при занятой блокировке: %v", err)
	}

	if err := os.WriteFile(release, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("hold: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("hold не отпустил блокировку")
	}
	got, err := c.SetManaged(context.Background(), Labels{Env: "prod", Space: "sp-1"})
	if err != nil || got.Result != ResultApplied {
		t.Fatalf("после освобождения SetManaged = %+v, %v", got, err)
	}
}

func TestIntegrationCheckActions(t *testing.T) {
	c := itAdapter(t)
	if err := c.CheckActions(context.Background()); err != nil {
		t.Fatalf("CheckActions: %v", err)
	}

	out := creamJSON(t, c.Path, "actions", "teleport", "--json")
	names, err := parseActions(out)
	if err != nil {
		t.Fatalf("parseActions на выводе настоящего cream: %v", err)
	}
	for _, n := range []string{"managed-status", "set-managed", "hold"} {
		if !names[n] {
			t.Fatalf("в cream actions нет %s: %v", n, names)
		}
	}
	// The sample of Task 4 has the same shape as the real output: the same
	// envelope keys, result keys and keys of every action.
	sameShape(t, "actions", out, fixture(t, "cream/actions-teleport.json"), "actions")
}

// TestIntegrationRunEnvelope checks the sample of `cream run --json`: the
// adapter does not use the envelope, but the sample must not drift.
func TestIntegrationRunEnvelope(t *testing.T) {
	c := itAdapter(t)
	scenario(t, "managed-status", fixture(t, "managed-status-joined.json"), 0, 0)
	out := creamJSON(t, c.Path, "run", "--json", "teleport", "managed-status")
	sameShape(t, "run", out, fixture(t, "cream/run-managed-status.json"), "")
}

func creamJSON(t *testing.T, path string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command(path, args...)
	cmd.Env = creamEnv()
	cmd.Dir = "/"
	cmd.Stderr = testLog{t}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("cream %v: %v", args, err)
	}
	return out
}

// sameShape compares key sets of the envelope, of result and (list != "") of
// every element of result[list] between the real output and the sample.
func sameShape(t *testing.T, what string, real, sample []byte, list string) {
	t.Helper()
	type env struct {
		top, result []string
		items       [][]string
	}
	shape := func(data []byte) env {
		var e map[string]json.RawMessage
		if err := json.Unmarshal(data, &e); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		var r map[string]json.RawMessage
		if err := json.Unmarshal(e["result"], &r); err != nil {
			t.Fatalf("%s result: %v", what, err)
		}
		s := env{top: keys(e), result: keys(r)}
		if list != "" {
			var items []map[string]json.RawMessage
			if err := json.Unmarshal(r[list], &items); err != nil {
				t.Fatalf("%s result.%s: %v", what, list, err)
			}
			for _, it := range items {
				s.items = append(s.items, keys(it))
			}
		}
		return s
	}
	got, want := shape(real), shape(sample)
	if !reflect.DeepEqual(got.top, want.top) || !reflect.DeepEqual(got.result, want.result) {
		t.Fatalf("%s: ключи настоящего cream %v / %v, образца %v / %v",
			what, got.top, got.result, want.top, want.result)
	}
	for _, g := range got.items {
		if !reflect.DeepEqual(g, want.items[0]) {
			t.Fatalf("%s: ключи действия настоящего cream %v, образца %v", what, g, want.items[0])
		}
	}
}

func keys(m map[string]json.RawMessage) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	slices.Sort(ks)
	return ks
}
