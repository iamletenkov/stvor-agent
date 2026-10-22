package cli

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/iamletenkov/stvor-agent/internal/atomicfile"
	"github.com/iamletenkov/stvor-agent/internal/daemon"
	"github.com/iamletenkov/stvor-agent/internal/initsys"
	"github.com/iamletenkov/stvor-agent/internal/portal"
	"github.com/iamletenkov/stvor-agent/internal/state"
	"github.com/iamletenkov/stvor-agent/internal/teleport"
	"github.com/iamletenkov/stvor-agent/internal/teleport/teleportfake"
	"github.com/iamletenkov/stvor-agent/internal/token"
)

const (
	testURL   = "https://stvor.example/robot-api/v1/labels"
	otherURL  = "https://other.example/robot-api/v1/labels"
	testToken = "Tok3n_-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789" // 43 chars
	testRev   = "0123456789abcdef"
)

var (
	labelsProd = teleport.Labels{Env: "prod", Space: "sp-1"}
	t0         = time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
)

// rootFS is the real file system that reports root as the owner of every
// file and directory (the tests do not run as root) and counts reads.
type rootFS struct {
	atomicfile.OS
	mu sync.Mutex
	// fileUID is reported for files read with ReadNoFollow.
	fileUID uint32
	reads   map[string]int
	// denied answers like to a user who is not root: EACCES.
	denied bool
	owners map[string][2]int
}

func (r *rootFS) ReadNoFollow(path string, limit int64) ([]byte, atomicfile.Info, error) {
	r.mu.Lock()
	r.reads[path]++
	uid, denied := r.fileUID, r.denied
	r.mu.Unlock()
	if denied {
		return nil, atomicfile.Info{}, &os.PathError{Op: "open", Path: path, Err: syscall.EACCES}
	}
	data, info, err := r.OS.ReadNoFollow(path, limit)
	info.UID = uid
	return data, info, err
}

func (r *rootFS) Lstat(path string) (atomicfile.Info, error) {
	info, err := r.OS.Lstat(path)
	info.UID = 0
	return info, err
}

func (r *rootFS) Remove(path string) error { return os.Remove(path) }

// WriteAtomicOwned and Chown record the owner instead of setting it (the
// tests do not run as root).
func (r *rootFS) WriteAtomicOwned(path string, data []byte, mode fs.FileMode, uid, gid int) error {
	if err := r.WriteAtomic(path, data, mode); err != nil {
		return err
	}
	return r.Chown(path, uid, gid)
}

func (r *rootFS) Chown(path string, uid, gid int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.owners == nil {
		r.owners = map[string][2]int{}
	}
	r.owners[path] = [2]int{uid, gid}
	return nil
}

func (r *rootFS) owner(path string) [2]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.owners[path]
}

func (r *rootFS) readsOf(path string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reads[path]
}

// fakePortal answers every poll with resp and records the requests.
type fakePortal struct {
	mu   sync.Mutex
	resp portal.Response
	reqs []portal.Request
}

func (p *fakePortal) Get(_ context.Context, r portal.Request) (portal.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reqs = append(p.reqs, r)
	return p.resp, nil
}

func (p *fakePortal) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.reqs)
}

// env is one temporary machine root with fakes for everything external.
type env struct {
	t      *testing.T
	root   string
	app    *App
	fs     *rootFS
	sys    *initsys.Fake
	tp     *teleportfake.Fake
	portal *fakePortal
	uid    int
	notify func(c chan<- os.Signal)
	envs   map[string]string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"etc", "var/lib", "run"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	e := &env{
		t:    t,
		root: root,
		fs:   &rootFS{reads: map[string]int{}},
		sys:  &initsys.Fake{Active: "active"},
		tp: &teleportfake.Fake{Current: teleport.Status{
			Managed: labelsProd, Joined: true, Active: true,
		}},
		portal: &fakePortal{resp: portal.Response{
			Outcome: portal.OutcomeOK, HTTPStatus: 200,
			Expected: portal.Expected{Labels: portal.Labels(labelsProd), Revision: testRev},
		}},
		envs: map[string]string{},
	}
	e.app = &App{
		Version:   "4.0.0",
		FS:        e.fs,
		TokenPath: filepath.Join(root, "etc/stvor-agent/token"),
		StatePath: filepath.Join(root, "var/lib/stvor-agent/state.json"),

		TelemetryTokenPath: filepath.Join(root, "etc/stvor-agent-telemetry/token"),
		TelemetryStateDir:  filepath.Join(root, "var/lib/stvor-agent-telemetry"),
		TelemetryRunDir:    filepath.Join(root, "run/stvor-agent-telemetry"),
		Lock:               daemon.FileLock{Path: filepath.Join(root, "run/stvor-agent.lock")},
		Init:               e.sys,
		Teleport:           e.tp,
		Portal:             e.portal,
		Now:                func() time.Time { return t0 },
		Getuid:             func() int { return e.uid },
		Getenv:             func(k string) string { return e.envs[k] },
		Notify: func(c chan<- os.Signal, _ ...os.Signal) {
			if e.notify != nil {
				e.notify(c)
			}
		},
		StopNotify: func(chan<- os.Signal) {},
	}
	return e
}

// run executes one command with stdin and returns the code and both outputs.
func (e *env) run(stdin string, args ...string) (code int, stdout, stderr string) {
	e.t.Helper()
	var out, errOut bytes.Buffer
	code = e.app.Run(args, strings.NewReader(stdin), &out, &errOut)
	return code, out.String(), errOut.String()
}

func (e *env) writeToken() {
	e.t.Helper()
	if err := token.Write(e.fs, e.app.TokenPath, token.Token{URL: testURL, Token: testToken}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) saveState(st state.State) {
	e.t.Helper()
	if err := os.MkdirAll(filepath.Dir(e.app.StatePath), 0o700); err != nil {
		e.t.Fatal(err)
	}
	if err := state.Save(e.fs, e.app.StatePath, st); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) loadState() (state.State, error) {
	return state.Load(e.fs, e.app.StatePath)
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// stateInEffect is a state whose expected labels are labelsProd and applied.
func stateInEffect() state.State {
	st := state.New(testURL)
	st.Expected = &portal.Expected{Labels: portal.Labels(labelsProd), Revision: testRev}
	st.AppliedRevision = testRev
	st.LastPoll = &state.Poll{At: t0, Outcome: "not_modified", HTTPStatus: 304}
	return st
}

// assertNoToken fails when the token shows up anywhere it must never be.
func (e *env) assertNoToken(outputs ...string) {
	e.t.Helper()
	for _, s := range outputs {
		if strings.Contains(s, testToken) {
			e.t.Errorf("token leaked into output %q", s)
		}
	}
	for _, c := range e.sys.Calls() {
		if strings.Contains(c, testToken) {
			e.t.Errorf("token leaked into systemctl argv %q", c)
		}
	}
	for _, l := range e.tp.SetCalls() {
		if strings.Contains(l.Env+l.Space, testToken) {
			e.t.Errorf("token leaked into set-managed %+v", l)
		}
	}
}

// holdLock takes the agent lock for the rest of the test.
func (e *env) holdLock() {
	e.t.Helper()
	unlock, err := daemon.FileLock{Path: e.app.Lock.Path}.TryLock()
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(unlock)
}

// fastLockClock makes a waiting lock give up at once.
func (e *env) fastLockClock() {
	var mu sync.Mutex
	now := t0
	e.app.Lock.Now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		now = now.Add(time.Minute)
		return now
	}
	e.app.Lock.After = func(time.Duration) <-chan time.Time {
		c := make(chan time.Time, 1)
		c <- t0
		return c
	}
}

// sendOnNotify makes Notify deliver sig into the service's channel at once.
func (e *env) sendOnNotify(sig syscall.Signal) {
	e.notify = func(c chan<- os.Signal) { c <- sig }
}
