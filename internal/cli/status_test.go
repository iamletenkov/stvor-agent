package cli

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/iamletenkov/stvor-agent/internal/state"
	"github.com/iamletenkov/stvor-agent/internal/teleport"
)

// status --json of a healthy agent matches the golden file of schema 1.
func TestStatusJSONGolden(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.writeToken()
	e.saveState(stateInEffect())
	e.holdLock() // status takes no lock
	code, stdout, stderr := e.run("", "status", "--json")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	want, err := os.ReadFile("testdata/status.json")
	if err != nil {
		t.Fatal(err)
	}
	if stdout != string(want) {
		t.Errorf("status --json =\n%s\nwant\n%s", stdout, want)
	}
	if e.portal.calls() != 0 {
		t.Error("status went to the network")
	}
	e.assertNoToken(stdout, stderr)
}

func TestStatusToken(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		setup       func(e *env)
		wantToken   string
		wantEnabled bool
		wantCode    int
	}{
		{"ok", func(e *env) { e.writeToken() }, "ok", true, 0},
		{"absent", func(*env) {}, "absent", false, 1},
		{"bad_mode", func(e *env) { e.writeToken(); _ = os.Chmod(e.app.TokenPath, 0o640) }, "bad_mode", true, 0},
		{"bad_owner", func(e *env) { e.writeToken(); e.fs.fileUID = 1000 }, "bad_owner", true, 0},
		{"corrupt", func(e *env) {
			e.writeToken()
			_ = os.WriteFile(e.app.TokenPath, []byte("token="+testToken+"\n"), 0o600)
		}, "corrupt", true, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			tc.setup(e)
			code, stdout, stderr := e.run("", "status", "--json")
			if code != tc.wantCode {
				t.Errorf("exit code = %d, want %d", code, tc.wantCode)
			}
			var got struct {
				Schema  int     `json:"schema"`
				Token   string  `json:"token"`
				Enabled bool    `json:"enabled"`
				URL     *string `json:"url"`
			}
			if err := json.Unmarshal([]byte(stdout), &got); err != nil {
				t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
			}
			if got.Schema != 1 || got.Token != tc.wantToken || got.Enabled != tc.wantEnabled {
				t.Errorf("schema/token/enabled = %d/%q/%v, want 1/%q/%v",
					got.Schema, got.Token, got.Enabled, tc.wantToken, tc.wantEnabled)
			}
			if (got.URL != nil) != (tc.wantToken == "ok") {
				t.Errorf("url = %v, want it only for a readable token", got.URL)
			}
			e.assertNoToken(stdout, stderr)
		})
	}
}

// Not root: the token file is closed — status says root is needed, does not
// claim enabled (null) and is not 0.
func TestStatusNoAccess(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.writeToken()
	e.fs.denied = true
	code, stdout, stderr := e.run("", "status", "--json")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	var got struct {
		Token   string `json:"token"`
		Enabled *bool  `json:"enabled"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
	}
	if got.Token != "no_access" || got.Enabled != nil {
		t.Errorf("token/enabled = %q/%v, want no_access/null", got.Token, got.Enabled)
	}
	if !strings.Contains(stderr, "нужен root") {
		t.Errorf("stderr = %q, want the reason «нужен root»", stderr)
	}
	code, stdout, _ = e.run("", "status")
	if code != 1 || !strings.Contains(stdout, "неизвестно") || strings.Contains(stdout, "включён:  нет") {
		t.Errorf("human status (code %d):\n%s", code, stdout)
	}
	e.assertNoToken(stdout, stderr)
}

func TestStatusDetails(t *testing.T) {
	t.Parallel()
	type out struct {
		InEffect bool            `json:"in_effect"`
		Teleport json.RawMessage `json:"teleport"`
		Restart  struct {
			Attempts    int        `json:"attempts"`
			PausedUntil *time.Time `json:"paused_until"`
		} `json:"restart"`
		Expected json.RawMessage `json:"expected"`
	}
	tests := []struct {
		name       string
		setup      func(e *env)
		check      func(t *testing.T, o out)
		wantStderr string
	}{
		{"paused restarts", func(e *env) {
			st := stateInEffect()
			st.RestartAttempts = state.Attempts{Count: 3, Last: t0.Add(-time.Minute)}
			e.saveState(st)
			e.tp.Current.Managed = teleport.Labels{Env: "dev", Space: "sp-0"}
		}, func(t *testing.T, o out) {
			want := t0.Add(14 * time.Minute)
			if o.Restart.Attempts != 3 || o.Restart.PausedUntil == nil || !o.Restart.PausedUntil.Equal(want) {
				t.Errorf("restart = %+v, want 3 attempts paused until %v", o.Restart, want)
			}
			if o.InEffect {
				t.Error("in_effect = true for other labels")
			}
		}, ""},
		{"pause is over", func(e *env) {
			st := stateInEffect()
			st.RestartAttempts = state.Attempts{Count: 3, Last: t0.Add(-time.Hour)}
			e.saveState(st)
		}, func(t *testing.T, o out) {
			if o.Restart.PausedUntil != nil {
				t.Errorf("paused_until = %v, want null", o.Restart.PausedUntil)
			}
		}, ""},
		{"teleport unknown", func(e *env) {
			e.saveState(stateInEffect())
			e.tp.StatusErr = teleport.ErrCream
		}, func(t *testing.T, o out) {
			if string(o.Teleport) != "null" || o.InEffect {
				t.Errorf("teleport = %s, in_effect = %v; want null and false", o.Teleport, o.InEffect)
			}
		}, "managed-status"},
		{"no state", func(*env) {}, func(t *testing.T, o out) {
			if string(o.Expected) != "null" || o.InEffect {
				t.Errorf("expected = %s, in_effect = %v; want null and false", o.Expected, o.InEffect)
			}
		}, ""},
		{"state of another portal is not shown", func(e *env) {
			st := stateInEffect()
			st.URL = otherURL
			e.saveState(st)
		}, func(t *testing.T, o out) {
			if string(o.Expected) != "null" {
				t.Errorf("expected = %s, want null", o.Expected)
			}
		}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			e.writeToken()
			tc.setup(e)
			code, stdout, stderr := e.run("", "status", "--json")
			if code != 0 {
				t.Errorf("exit code = %d, want 0", code)
			}
			var o out
			if err := json.Unmarshal([]byte(stdout), &o); err != nil {
				t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
			}
			tc.check(t, o)
			if !strings.Contains(stderr, tc.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr, tc.wantStderr)
			}
		})
	}
}

func TestStatusHuman(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.writeToken()
	e.saveState(stateInEffect())
	code, stdout, stderr := e.run("", "status")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	for _, want := range []string{"служба", "active", testURL, "env=prod space=sp-1", testRev, "метки действуют", "да", "not_modified"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("status output lacks %q:\n%s", want, stdout)
		}
	}
	e.assertNoToken(stdout, stderr)
}

// version --label: the version when the token file exists, else an empty
// line; exit 0 always; the token file is never opened, no lock is taken.
func TestVersionLabel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		setup func(e *env)
		want  string
	}{
		{"enabled", func(e *env) { e.writeToken() }, "4.0.0\n"},
		{"enabled with a broken token file", func(e *env) {
			e.writeToken()
			_ = os.Chmod(e.app.TokenPath, 0o644)
		}, "4.0.0\n"},
		{"disabled", func(*env) {}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			tc.setup(e)
			e.holdLock()
			e.uid = 1000
			code, stdout, stderr := e.run("", "version", "--label")
			if code != 0 {
				t.Errorf("exit code = %d, want 0", code)
			}
			if stdout != tc.want || stderr != "" {
				t.Errorf("stdout, stderr = %q, %q; want %q, empty", stdout, stderr, tc.want)
			}
			if n := e.fs.readsOf(e.app.TokenPath); n != 0 {
				t.Errorf("token file opened %d times, want 0", n)
			}
			if len(e.sys.Calls()) != 0 || e.tp.StatusCalls() != 0 || e.portal.calls() != 0 {
				t.Error("version --label touched systemd, teleport or the portal")
			}
		})
	}
}

// Exit 0 even when stdout is closed: Teleport would put an error into the label.
func TestVersionLabelAlwaysZero(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.writeToken()
	var stderr strings.Builder
	if code := e.app.Run([]string{"version", "--label"}, strings.NewReader(""), failingWriter{}, &stderr); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
}
