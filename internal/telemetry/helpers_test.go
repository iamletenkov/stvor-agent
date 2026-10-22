package telemetry

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamletenkov/stvor-agent/internal/buffer"
)

const (
	testToken  = "tEsT_tOkEn-0123456789abcdefghijklmnopqrstuv"
	testToken2 = "rotated_token-0123456789abcdefghijklmnopqrs"
	testBoot   = "11111111-2222-3333-4444-555555555555"
	testMID    = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

// clock is the wall clock and CLOCK_BOOTTIME of a test.
type clock struct {
	mu   sync.Mutex
	now  time.Time
	mono int64
}

func newClock() *clock {
	return &clock{now: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC), mono: 1_000}
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) MonoMS() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mono
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	c.mono += d.Milliseconds()
}

// fakeTokens is the token file of a test.
type fakeTokens struct {
	mu        sync.Mutex
	creds     Credentials
	rejected  int
	rejectErr error // Rejected fails with it (marker not written)
}

func (f *fakeTokens) Credentials() (Credentials, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.creds, nil
}

func (f *fakeTokens) Rejected() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rejectErr != nil {
		return f.rejectErr
	}
	f.rejected++
	return nil
}

func (f *fakeTokens) set(token string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creds.Token = token
}

// request is what the fake portal received.
type request struct {
	Method, Path, Auth, Encoding string
	Body                         map[string]any
}

// portal is an httptest TLS server answering with reply.
type portal struct {
	srv   *httptest.Server
	mu    sync.Mutex
	reqs  []request
	reply func(r request, n int) (status int, header map[string]string, body string)
}

func newPortal(t *testing.T, reply func(r request, n int) (int, map[string]string, string)) *portal {
	t.Helper()
	p := &portal{reply: reply}
	p.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		zr, err := gzip.NewReader(r.Body)
		if err != nil {
			http.Error(w, "gzip", http.StatusBadRequest)
			return
		}
		raw, err := io.ReadAll(zr)
		if err != nil {
			http.Error(w, "gzip", http.StatusBadRequest)
			return
		}
		req := request{Method: r.Method, Path: r.URL.Path, Auth: r.Header.Get("Authorization"),
			Encoding: r.Header.Get("Content-Encoding")}
		if err := json.Unmarshal(raw, &req.Body); err != nil {
			http.Error(w, "json", http.StatusBadRequest)
			return
		}
		p.mu.Lock()
		p.reqs = append(p.reqs, req)
		n := len(p.reqs)
		p.mu.Unlock()
		status, header, body := p.reply(req, n)
		for k, v := range header {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *portal) requests() []request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]request(nil), p.reqs...)
}

func ok(request, int) (int, map[string]string, string) {
	return http.StatusOK, nil, `{"status":"accepted"}`
}

// env is a sender over a real buffer and a fake portal.
type env struct {
	s      *Sender
	buf    *buffer.Buffer
	tokens *fakeTokens
	clk    *clock
	log    *bytes.Buffer
	portal *portal
}

func newEnv(t *testing.T, reply func(r request, n int) (int, map[string]string, string)) *env {
	t.Helper()
	p := newPortal(t, reply)
	clk := newClock()
	buf, err := buffer.Open(buffer.Config{Dir: t.TempDir(), Now: clk.Now, MonoMS: clk.MonoMS})
	if err != nil {
		t.Fatal(err)
	}
	pool := p.srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	tokens := &fakeTokens{creds: Credentials{BaseURL: p.srv.URL + "/robot-api/v1", Token: testToken}}
	logs := &bytes.Buffer{}
	e := &env{buf: buf, tokens: tokens, clk: clk, log: logs, portal: p}
	e.s, err = NewSender(Options{
		Client: ClientConfig{RootCAs: pool, Version: "4.1.0", RequestTimeout: 5 * time.Second},
		Tokens: tokens, Queue: buf,
		Meta: func() Meta {
			return Meta{AgentVersion: "4.1.0", MachineIDHash: testMID, BootID: testBoot,
				Capabilities: []string{"psi", "bogus", "thermal"}, FailedUnits: []string{"a.service"}}
		},
		Log:    slog.New(slog.NewJSONHandler(&syncWriter{w: logs}, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Now:    clk.Now,
		MonoMS: clk.MonoMS,
		Int64N: func(n int64) int64 { return n - 1 }, // the ceiling of the full jitter
		Sleep:  func(_ context.Context, d time.Duration) error { clk.advance(d); return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// tick adds one collector record.
func (e *env) tick(t *testing.T) {
	t.Helper()
	e.clk.advance(10 * time.Second)
	e.record(t)
}

// record adds one collector record at the current time.
func (e *env) record(t *testing.T) {
	t.Helper()
	ms := float64(e.clk.MonoMS())
	err := e.buf.Add(buffer.Record{
		At: e.clk.Now(), MonoMS: e.clk.MonoMS(), BootID: testBoot, ClockSynced: true,
		Samples: []buffer.Sample{
			{Name: "system.cpu.time", Kind: buffer.Counter, Labels: []buffer.Label{{K: "mode", V: "user"}}, Value: ms / 3},
			{Name: "system.memory.usage", Kind: buffer.Gauge, Labels: []buffer.Label{{K: "state", V: "used"}}, Value: 1e9 + ms},
			{Name: "system.cpu.load_average.1m", Kind: buffer.Gauge, Value: 0.5},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (e *env) step(t *testing.T) Result {
	t.Helper()
	return e.s.Step(context.Background())
}

func seriesNames(body map[string]any) []string {
	var out []string
	for _, s := range body["series"].([]any) {
		out = append(out, s.(map[string]any)["name"].(string))
	}
	return out
}

func pointsIn(body map[string]any) int {
	n := 0
	for _, s := range body["series"].([]any) {
		n += len(s.(map[string]any)["points"].([]any))
	}
	return n
}

func hasLine(logs, sub string) bool { return strings.Contains(logs, sub) }
