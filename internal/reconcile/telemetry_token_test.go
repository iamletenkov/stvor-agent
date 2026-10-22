package reconcile

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamletenkov/stvor-agent/internal/atomicfile"
	"github.com/iamletenkov/stvor-agent/internal/portal"
	"github.com/iamletenkov/stvor-agent/internal/token"
)

const (
	telTok1 = "telemetry_token_number_one_0123456789abcd"
	telTok2 = "telemetry-token-number-two-0123456789abcd"
	telGID  = 990
)

// chownFS is the real file system of a temp dir; chown is recorded, not done
// (the tests do not run as root).
type chownFS struct {
	atomicfile.OS
	mu     sync.Mutex
	owners map[string][2]int
	modes  map[string]fs.FileMode
}

func newChownFS() *chownFS {
	return &chownFS{owners: map[string][2]int{}, modes: map[string]fs.FileMode{}}
}

func (c *chownFS) WriteAtomicOwned(path string, data []byte, mode fs.FileMode, uid, gid int) error {
	if err := c.WriteAtomic(path, data, mode); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.owners[path] = [2]int{uid, gid}
	return nil
}

func (c *chownFS) Chown(path string, uid, gid int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.owners[path] = [2]int{uid, gid}
	return nil
}

func (c *chownFS) Remove(path string) error { return os.Remove(path) }

func (c *chownFS) owner(path string) [2]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.owners[path]
}

// fakeTelemetryPortal hands out tokens in order.
type fakeTelemetryPortal struct {
	tokens []string
	resp   *portal.TokenResponse
	reqs   []portal.Request
}

func (f *fakeTelemetryPortal) TelemetryToken(_ context.Context, r portal.Request) (portal.TokenResponse, error) {
	f.reqs = append(f.reqs, r)
	if f.resp != nil {
		return *f.resp, nil
	}
	tok := f.tokens[0]
	f.tokens = f.tokens[1:]
	return portal.TokenResponse{Outcome: portal.OutcomeOK, HTTPStatus: 200, Token: portal.TelemetryToken(tok)}, nil
}

type telRig struct {
	p      *TelemetryToken
	fs     *chownFS
	portal *fakeTelemetryPortal
	clock  *clock
	starts int
	log    *bytes.Buffer
	root   string
}

func newTelRig(t *testing.T) *telRig {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"etc", "var/lib/stvor-agent-telemetry"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	r := &telRig{fs: newChownFS(), portal: &fakeTelemetryPortal{tokens: []string{telTok1, telTok2}},
		clock: &clock{now: t0}, log: &bytes.Buffer{}, root: root}
	r.p = &TelemetryToken{
		Portal:       r.portal,
		FS:           r.fs,
		LookupGroup:  func(string) (int, error) { return telGID, nil },
		Start:        func(context.Context) error { r.starts++; return nil },
		TokenPath:    filepath.Join(root, "etc/stvor-agent-telemetry/token"),
		RejectedPath: filepath.Join(root, "var/lib/stvor-agent-telemetry/token-rejected"),
		SourcePath:   filepath.Join(root, "var/lib/stvor-agent/telemetry-token.src"),
		Now:          r.clock.Now,
		Log:          slog.New(slog.NewTextHandler(r.log, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	return r
}

func (r *telRig) ensure(t *testing.T, labels string) {
	t.Helper()
	r.p.Ensure(context.Background(), token.Token{URL: "https://stvor.example:8443/robot-api/v1/labels", Token: labels})
	for _, s := range []string{testToken, otherToken, telTok1, telTok2} {
		if strings.Contains(r.log.String(), s) {
			t.Fatalf("token in the journal: %s", r.log.String())
		}
	}
}

func (r *telRig) file(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(r.p.TokenPath)
	if err != nil {
		t.Fatalf("token file: %v", err)
	}
	return string(data)
}

const otherToken = "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"

// The first tick writes the token: base of the labels address, file 0640 and
// directory 0750, both root:stvor-telemetry, and starts the unit; the next
// tick asks nothing.
func TestTelemetryTokenFirstFetch(t *testing.T) {
	t.Parallel()
	r := newTelRig(t)
	r.ensure(t, testToken)

	if want := "url=https://stvor.example:8443/robot-api/v1\ntoken=" + telTok1 + "\n"; r.file(t) != want {
		t.Errorf("file = %q, want %q", r.file(t), want)
	}
	st, _ := os.Stat(r.p.TokenPath)
	dst, _ := os.Stat(filepath.Dir(r.p.TokenPath))
	if st.Mode().Perm() != 0o640 || dst.Mode().Perm() != 0o750 {
		t.Errorf("modes: file %o dir %o, want 0640 and 0750", st.Mode().Perm(), dst.Mode().Perm())
	}
	for _, p := range []string{r.p.TokenPath, filepath.Dir(r.p.TokenPath)} {
		if got := r.fs.owner(p); got != [2]int{0, telGID} {
			t.Errorf("owner of %s = %v, want root:%d", p, got, telGID)
		}
	}
	if src, _ := os.Stat(r.p.SourcePath); src == nil || src.Mode().Perm() != 0o600 {
		t.Errorf("fingerprint file %v, want 0600", src)
	}
	if r.starts != 1 {
		t.Errorf("starts = %d, want 1", r.starts)
	}
	if req := r.portal.reqs[0]; req.Token != testToken || req.Revision != "" {
		t.Errorf("request %+v", req)
	}
	r.ensure(t, testToken)
	if len(r.portal.reqs) != 1 || r.starts != 1 {
		t.Errorf("second tick: %d requests, %d starts, want 1 and 1", len(r.portal.reqs), r.starts)
	}
}

// A rejected marker or a new labels token (reissue on the portal revokes the
// telemetry token) asks again; the marker is removed.
func TestTelemetryTokenRotation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		labels string
		setup  func(r *telRig)
	}{
		{"rejected marker", testToken, func(r *telRig) {
			_ = os.WriteFile(r.p.RejectedPath, []byte("rejected\n"), 0o600)
		}},
		{"labels token rotated", otherToken, func(*telRig) {}},
		{"file damaged", testToken, func(r *telRig) { _ = os.WriteFile(r.p.TokenPath, []byte("garbage"), 0o640) }},
		{"file removed", testToken, func(r *telRig) { _ = os.Remove(r.p.TokenPath) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newTelRig(t)
			r.ensure(t, testToken)
			tc.setup(r)
			r.ensure(t, tc.labels)
			if len(r.portal.reqs) != 2 || !strings.Contains(r.file(t), "token="+telTok2+"\n") {
				t.Fatalf("requests %d, file %q", len(r.portal.reqs), r.file(t))
			}
			if _, err := os.Lstat(r.p.RejectedPath); !errors.Is(err, fs.ErrNotExist) {
				t.Error("marker left behind")
			}
			r.ensure(t, tc.labels)
			if len(r.portal.reqs) != 2 {
				t.Errorf("a stable state asked again: %d requests", len(r.portal.reqs))
			}
		})
	}
}

// Failures pause the requests (1 min doubling up to 1 h) and leave the file
// as it was; a missing group asks nothing at all.
func TestTelemetryTokenFailures(t *testing.T) {
	t.Parallel()
	r := newTelRig(t)
	r.portal.resp = &portal.TokenResponse{Outcome: portal.OutcomeHTTPError, HTTPStatus: 404}
	r.ensure(t, testToken)
	r.ensure(t, testToken)
	if len(r.portal.reqs) != 1 {
		t.Fatalf("requests before the pause = %d, want 1", len(r.portal.reqs))
	}
	if _, err := os.Lstat(r.p.TokenPath); err == nil {
		t.Error("a failed request wrote the file")
	}
	for i, wait := range []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute} {
		r.clock.now = r.clock.now.Add(wait - time.Second)
		r.ensure(t, testToken)
		if len(r.portal.reqs) != i+1 {
			t.Fatalf("asked before %v", wait)
		}
		r.clock.now = r.clock.now.Add(time.Second)
		r.ensure(t, testToken)
		if len(r.portal.reqs) != i+2 {
			t.Fatalf("not asked after %v", wait)
		}
	}
	for range 20 {
		r.clock.now = r.clock.now.Add(TelemetryRetryCeil)
		r.ensure(t, testToken)
	}
	if n := len(r.portal.reqs); n != 24 {
		t.Errorf("requests = %d, want one per hour at most (24)", n)
	}
	r.portal.resp = nil
	r.clock.now = r.clock.now.Add(TelemetryRetryCeil)
	r.ensure(t, testToken)
	if !strings.Contains(r.file(t), telTok1) || r.p.failures != 0 {
		t.Errorf("recovery: file %q, failures %d", r.file(t), r.p.failures)
	}

	g := newTelRig(t)
	g.p.LookupGroup = func(string) (int, error) { return 0, errors.New("no such group") }
	g.ensure(t, testToken)
	if len(g.portal.reqs) != 0 {
		t.Error("asked the portal without the group: the token would be revoked for nothing")
	}
}

// The tick runs the step only after the portal accepted the labels token.
func TestTickTelemetryStep(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		resp portal.Response
		want int
	}{
		{"200", portal.Response{Outcome: portal.OutcomeOK, HTTPStatus: 200,
			Expected: portal.Expected{Labels: portal.Labels(labelsB), Revision: revB}}, 1},
		{"304", portal.Response{Outcome: portal.OutcomeNotModified, HTTPStatus: 304}, 1},
		{"401", portal.Response{Outcome: portal.OutcomeUnauthorized, HTTPStatus: 401}, 0},
		{"network", portal.Response{Outcome: portal.OutcomeNetwork}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig()
			r.steady()
			step := &countStep{}
			r.tk.Telemetry = step
			r.portal.resp = tc.resp
			r.tick(t, false)
			if step.n != tc.want {
				t.Errorf("telemetry step ran %d times, want %d", step.n, tc.want)
			}
			if tc.want == 1 && step.tok.Token != testToken {
				t.Error("step got another token")
			}
		})
	}
	r := newRig()
	r.steady()
	r.answer304()
	step := &countStep{}
	r.tk.Telemetry = step
	stop := make(chan struct{})
	close(stop)
	r.tk.Tick(context.Background(), false, stop)
	if step.n != 0 {
		t.Error("telemetry step ran while stopping")
	}
}

func (r *rig) answer304() { r.portal.answer304() }

type countStep struct {
	n   int
	tok token.Token
}

func (c *countStep) Ensure(_ context.Context, tok token.Token) { c.n++; c.tok = tok }
