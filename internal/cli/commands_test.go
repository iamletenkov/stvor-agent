package cli

import (
	"os"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/iamletenkov/stvor-agent/internal/portal"
	"github.com/iamletenkov/stvor-agent/internal/state"
	"github.com/iamletenkov/stvor-agent/internal/teleport"
	"github.com/iamletenkov/stvor-agent/internal/teleport/teleportfake"
	"github.com/iamletenkov/stvor-agent/internal/token"
)

var wantEnableCalls = []string{"enable stvor-agent.service", "restart stvor-agent.service"}

func TestEnableStdin(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		stdin    string
		wantCode int
		wantErr  string
	}{
		{"one line", testToken + "\n", 0, ""},
		{"no newline", testToken, 0, ""},
		{"spaces around", "  \t" + testToken + " \n", 0, ""},
		{"crlf", testToken + "\r\n", 0, ""},
		{"empty", "", 1, "пуст"},
		{"only newline", "\n", 1, "пуст"},
		{"two lines", testToken + "\n" + testToken + "\n", 1, "одна строка"},
		{"blank second line", testToken + "\n\n", 1, "одна строка"},
		{"42 chars", testToken[:42] + "\n", 1, "43"},
		{"44 chars", testToken + "A\n", 1, "43"},
		{"inner space", testToken[:20] + " " + testToken[21:] + "\n", 1, "43"},
		{"huge", strings.Repeat("A", 1<<20), 1, "43"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			code, stdout, stderr := e.run(tc.stdin, "enable", "URL="+testURL)
			if code != tc.wantCode {
				t.Fatalf("exit code = %d, want %d (stderr %q)", code, tc.wantCode, stderr)
			}
			e.assertNoToken(stdout, stderr)
			if tc.wantCode != 0 {
				if !strings.Contains(stderr, tc.wantErr) {
					t.Errorf("stderr = %q, want it to contain %q", stderr, tc.wantErr)
				}
				if exists(e.app.TokenPath) {
					t.Error("refused stdin still wrote the token file")
				}
				if len(e.sys.Calls()) != 0 {
					t.Errorf("refused stdin still called systemctl: %q", e.sys.Calls())
				}
				return
			}
			tok, err := token.Read(e.fs, e.app.TokenPath)
			if err != nil {
				t.Fatalf("token file: %v", err)
			}
			if tok.URL != testURL || tok.Token != testToken {
				t.Errorf("token file = %v, want url %s and the trimmed token", tok, testURL)
			}
			fi, err := os.Stat(e.app.TokenPath)
			if err != nil {
				t.Fatal(err)
			}
			if fi.Mode().Perm() != 0o600 {
				t.Errorf("token file mode = %04o, want 0600", fi.Mode().Perm())
			}
		})
	}
}

func TestEnableBadURL(t *testing.T) {
	t.Parallel()
	for _, u := range []string{"http://stvor.example/robot-api/v1/labels", "https://stvor.example/other", "https://STVOR.example/robot-api/v1/labels"} {
		t.Run(u, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			code, stdout, stderr := e.run(testToken+"\n", "enable", "URL="+u)
			if code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			if !strings.Contains(stderr, "URL") {
				t.Errorf("stderr = %q, want the reason", stderr)
			}
			if exists(e.app.TokenPath) || len(e.sys.Calls()) != 0 {
				t.Error("a bad URL still wrote the token or called systemctl")
			}
			e.assertNoToken(stdout, stderr)
		})
	}
}

// enable writes the token, enables and restarts the service, then syncs at
// once; the token goes only into the Authorization of the poll.
func TestEnableFlow(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.tp.Current.Managed = teleport.Labels{Env: "dev", Space: "sp-0"}
	code, stdout, stderr := e.run(testToken+"\n", "enable", "URL="+testURL)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	if got := e.sys.Calls(); !slices.Equal(got, wantEnableCalls) {
		t.Errorf("systemctl = %q, want %q", got, wantEnableCalls)
	}
	if len(e.portal.reqs) != 1 || e.portal.reqs[0].URL != testURL || e.portal.reqs[0].Token != testToken {
		t.Errorf("portal requests = %d, want one to %s with the token", len(e.portal.reqs), testURL)
	}
	if got := e.tp.SetCalls(); !slices.Equal(got, []teleport.Labels{labelsProd}) {
		t.Errorf("set-managed = %v, want %v", got, labelsProd)
	}
	st, err := e.loadState()
	if err != nil || st.AppliedRevision != testRev {
		t.Errorf("state after enable = %+v, %v; want applied %s", st, err, testRev)
	}
	e.assertNoToken(stdout, stderr)
}

// A network failure of the first sync is 0 with a line: the service catches up.
func TestEnableNetworkFailureIsZero(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.portal.resp = portal.Response{Outcome: portal.OutcomeNetwork}
	code, stdout, stderr := e.run(testToken+"\n", "enable", "URL="+testURL)
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout+stderr, "служба") {
		t.Errorf("output %q %q, want a line that the service will retry", stdout, stderr)
	}
	if !exists(e.app.TokenPath) {
		t.Error("token file missing")
	}
}

func TestEnableSystemctlFailure(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.sys.RestartErr = os.ErrPermission
	code, stdout, stderr := e.run(testToken+"\n", "enable", "URL="+testURL)
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if e.portal.calls() != 0 {
		t.Error("sync ran although the service did not start")
	}
	e.assertNoToken(stdout, stderr)
}

// Another portal address drops state.json; the same address keeps it. The
// restart fails here so that no sync rewrites the state afterwards.
func TestEnableResetsStateForAnotherURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		url       string
		wantState bool
	}{
		{"same url keeps state", testURL, true},
		{"other url drops state", otherURL, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			e.writeToken()
			e.saveState(stateInEffect())
			e.sys.RestartErr = os.ErrPermission
			e.run(testToken+"\n", "enable", "URL="+tc.url)
			if got := exists(e.app.StatePath); got != tc.wantState {
				t.Errorf("state.json exists = %v, want %v", got, tc.wantState)
			}
			tok, err := token.Read(e.fs, e.app.TokenPath)
			if err != nil || tok.URL != tc.url {
				t.Errorf("token url = %q, %v; want %q", tok.URL, err, tc.url)
			}
		})
	}
}

func TestDisable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		setup    func(e *env)
		wantCode int
	}{
		{"enabled agent", func(e *env) { e.writeToken(); e.saveState(stateInEffect()) }, 0},
		{"already disabled", func(*env) {}, 0},
		{"systemctl fails, files still go", func(e *env) {
			e.writeToken()
			e.saveState(stateInEffect())
			e.sys.DisableErr = os.ErrPermission
		}, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			tc.setup(e)
			code, stdout, stderr := e.run("", "disable")
			if code != tc.wantCode {
				t.Errorf("exit code = %d, want %d (stderr %q)", code, tc.wantCode, stderr)
			}
			if got := e.sys.Calls(); !slices.Equal(got, []string{"disable --now stvor-agent.service"}) {
				t.Errorf("systemctl = %q", got)
			}
			if exists(e.app.TokenPath) || exists(e.app.StatePath) {
				t.Error("token or state.json left behind")
			}
			// teleport is not called: managed env/space stay.
			if e.tp.StatusCalls() != 0 || len(e.tp.SetCalls()) != 0 || e.portal.calls() != 0 {
				t.Error("disable called teleport or the portal")
			}
			e.assertNoToken(stdout, stderr)
		})
	}
}

func TestSyncCodes(t *testing.T) {
	t.Parallel()
	recent := state.Attempts{Count: 3, Last: t0.Add(-time.Minute)}
	tests := []struct {
		name     string
		setup    func(e *env)
		wantCode int
		wantSet  int
		wantOut  string // substring of stdout+stderr
	}{
		{"in effect", func(*env) {}, 0, 0, "действуют"},
		{"applied", func(e *env) { e.tp.Current.Managed = teleport.Labels{Env: "dev", Space: "sp-0"} }, 0, 1, "действуют"},
		{"304 in effect", func(e *env) {
			e.saveState(stateInEffect())
			e.portal.resp = portal.Response{Outcome: portal.OutcomeNotModified, HTTPStatus: 304}
		}, 0, 0, "действуют"},
		{"network", func(e *env) { e.portal.resp = portal.Response{Outcome: portal.OutcomeNetwork} }, 0, 0, "служба"},
		{"401", func(e *env) {
			e.portal.resp = portal.Response{Outcome: portal.OutcomeUnauthorized, HTTPStatus: 401}
		}, 0, 0, "401"},
		{"429", func(e *env) {
			e.portal.resp = portal.Response{Outcome: portal.OutcomeRateLimited, HTTPStatus: 429}
		}, 0, 0, "429"},
		{"500", func(e *env) { e.portal.resp = portal.Response{Outcome: portal.OutcomeHTTPError, HTTPStatus: 500} }, 0, 0, "500"},
		{"redirect", func(e *env) { e.portal.resp = portal.Response{Outcome: portal.OutcomeRedirect, HTTPStatus: 302} }, 0, 0, "302"},
		{"bad body", func(e *env) { e.portal.resp = portal.Response{Outcome: portal.OutcomeBadBody, HTTPStatus: 200} }, 1, 0, "контракт"},
		{"304 without remembered labels", func(e *env) {
			e.portal.resp = portal.Response{Outcome: portal.OutcomeNotModified, HTTPStatus: 304}
		}, 1, 0, "304"},
		{"unconfirmed", func(e *env) {
			e.tp.Current.Managed = teleport.Labels{Env: "dev", Space: "sp-0"}
			e.tp.SetFunc = func(f *teleportfake.Fake, l teleport.Labels) (teleport.SetResult, error) {
				f.Current.Managed, f.Current.Pending = l, true
				return teleport.SetResult{Status: f.Current, Result: teleport.ResultUnconfirmed}, nil
			}
		}, 1, 1, "не подтвердил"},
		{"deferred", func(e *env) {
			e.tp.Current = teleport.Status{Managed: teleport.Labels{Env: "dev", Space: "sp-0"}}
		}, 0, 1, "join"},
		{"set-managed fails", func(e *env) {
			e.tp.Current.Managed = teleport.Labels{Env: "dev", Space: "sp-0"}
			e.tp.SetErr = teleport.ErrLockBusy
		}, 1, 1, "не действуют"},
		{"managed-status fails", func(e *env) { e.tp.StatusErr = teleport.ErrCream }, 1, 0, "teleport"},
		{"no pause for a person", func(e *env) {
			st := stateInEffect()
			st.RestartAttempts = recent
			e.saveState(st)
			e.tp.Current.Managed = teleport.Labels{Env: "dev", Space: "sp-0"}
		}, 0, 1, "действуют"},
		{"not enabled", func(e *env) { _ = os.Remove(e.app.TokenPath) }, 1, 0, "enable"},
		{"token bad mode", func(e *env) { _ = os.Chmod(e.app.TokenPath, 0o644) }, 1, 0, "bad_mode"},
		{"token corrupt", func(e *env) {
			_ = os.WriteFile(e.app.TokenPath, []byte("url=x\n"), 0o600)
		}, 1, 0, "corrupt"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			e.writeToken()
			tc.setup(e)
			code, stdout, stderr := e.run("", "sync")
			if code != tc.wantCode {
				t.Errorf("exit code = %d, want %d (stdout %q, stderr %q)", code, tc.wantCode, stdout, stderr)
			}
			if got := len(e.tp.SetCalls()); got != tc.wantSet {
				t.Errorf("set-managed calls = %d, want %d", got, tc.wantSet)
			}
			if !strings.Contains(stdout+stderr, tc.wantOut) {
				t.Errorf("output = %q %q, want it to contain %q", stdout, stderr, tc.wantOut)
			}
			e.assertNoToken(stdout, stderr)
		})
	}
}

// sync reports the tick's end state: no managed-status of its own outside the
// agent lock.
func TestSyncNoSecondStatus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		setup  func(e *env)
		status int // managed-status calls: the tick's only
	}{
		{"in effect", func(*env) {}, 1},
		{"applied", func(e *env) { e.tp.Current.Managed = teleport.Labels{Env: "dev", Space: "sp-0"} }, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			e.writeToken()
			tc.setup(e)
			if code, stdout, stderr := e.run("", "sync"); code != 0 {
				t.Fatalf("exit code = %d (stdout %q, stderr %q)", code, stdout, stderr)
			}
			if got := e.tp.StatusCalls(); got != tc.status {
				t.Fatalf("managed-status calls = %d, want %d", got, tc.status)
			}
		})
	}
}

func TestSyncLockTimeout(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.writeToken()
	e.holdLock()
	e.fastLockClock()
	code, _, stderr := e.run("", "sync")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr, "блокировка") {
		t.Errorf("stderr = %q, want the lock reason", stderr)
	}
	if e.portal.calls() != 0 {
		t.Error("sync polled the portal without the lock")
	}
}

func TestApply(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		setup    func(e *env)
		wantCode int
		wantOut  string
	}{
		{"applied", func(e *env) { e.tp.Current.Managed = teleport.Labels{Env: "dev", Space: "sp-0"} }, 0, "applied"},
		{"unchanged", func(*env) {}, 0, "unchanged"},
		{"deferred", func(e *env) { e.tp.Current = teleport.Status{} }, 0, "join"},
		{"unconfirmed", func(e *env) {
			e.tp.SetFunc = func(f *teleportfake.Fake, l teleport.Labels) (teleport.SetResult, error) {
				f.Current.Managed, f.Current.Pending = l, true
				return teleport.SetResult{Status: f.Current, Result: teleport.ResultUnconfirmed}, nil
			}
		}, 1, "не подтвердил"},
		{"cream lock busy", func(e *env) { e.tp.SetErr = teleport.ErrLockBusy }, 1, "set-managed"},
		{"paused attempts do not stop a person", func(e *env) {
			st := stateInEffect()
			st.RestartAttempts = state.Attempts{Count: 5, Last: t0}
			e.saveState(st)
		}, 0, "unchanged"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			e.writeToken()
			tc.setup(e)
			before, _ := os.ReadFile(e.app.StatePath)
			code, stdout, stderr := e.run("", "apply", "env=prod", "space=sp-1")
			if code != tc.wantCode {
				t.Errorf("exit code = %d, want %d (stderr %q)", code, tc.wantCode, stderr)
			}
			if got := e.tp.SetCalls(); !slices.Equal(got, []teleport.Labels{labelsProd}) {
				t.Errorf("set-managed = %v, want exactly %v", got, labelsProd)
			}
			if !strings.Contains(stdout+stderr, tc.wantOut) {
				t.Errorf("output = %q %q, want it to contain %q", stdout, stderr, tc.wantOut)
			}
			after, _ := os.ReadFile(e.app.StatePath)
			if string(before) != string(after) {
				t.Error("apply changed state.json")
			}
			if e.portal.calls() != 0 {
				t.Error("apply polled the portal")
			}
			e.assertNoToken(stdout, stderr)
		})
	}
}

func TestApplyLockTimeout(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.holdLock()
	e.fastLockClock()
	if code, _, _ := e.run("", "apply", "env=prod", "space=sp-1"); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if len(e.tp.SetCalls()) != 0 {
		t.Error("apply ran set-managed without the lock")
	}
}

// run checks the teleport actions once, ticks until SIGTERM and exits 0.
func TestRunService(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.writeToken()
	e.envs["STVOR_AGENT_INTERVAL"] = "5"
	e.sendOnNotify(syscall.SIGTERM)
	done := make(chan int, 1)
	var stderr string
	go func() {
		code, _, errOut := e.run("", "run")
		stderr = errOut
		done <- code
	}()
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("exit code = %d, want 0", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not stop on SIGTERM")
	}
	if e.tp.ActionsCalls() != 1 {
		t.Errorf("CheckActions calls = %d, want 1", e.tp.ActionsCalls())
	}
	if !strings.Contains(stderr, "STVOR_AGENT_INTERVAL") {
		t.Errorf("stderr = %q, want the config problem line", stderr)
	}
	e.assertNoToken(stderr)
}
