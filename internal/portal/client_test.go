package portal_test

import (
	"context"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamletenkov/stvor-agent/internal/portal"
	"github.com/iamletenkov/stvor-agent/internal/reconcile"
)

const (
	token   = "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789_-abcd" // 42
	apiPath = "/robot-api/v1/labels"

	testToken = token + "Z" // 43 chars
)

// fakePortal is an httptest TLS server that answers with a fixed status and
// body and records what it received.
type fakePortal struct {
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []*http.Request
	hits atomic.Int32
}

func newFakePortal(t *testing.T, h func(w http.ResponseWriter, r *http.Request)) *fakePortal {
	t.Helper()
	fp := &fakePortal{}
	fp.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fp.hits.Add(1)
		fp.mu.Lock()
		fp.reqs = append(fp.reqs, r.Clone(context.Background()))
		fp.mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(fp.srv.Close)
	return fp
}

func (fp *fakePortal) url() string { return fp.srv.URL + apiPath }

func (fp *fakePortal) pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(fp.srv.Certificate())
	return p
}

func (fp *fakePortal) last(t *testing.T) *http.Request {
	t.Helper()
	fp.mu.Lock()
	defer fp.mu.Unlock()
	if len(fp.reqs) == 0 {
		t.Fatal("portal received no request")
	}
	return fp.reqs[len(fp.reqs)-1]
}

func reply(code int, body string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}
}

func newClient(pool *x509.CertPool) *portal.Client {
	return portal.New(portal.Config{RootCAs: pool, Version: "4.0.0"})
}

// assertNoToken checks that the token leaks into no error text.
func assertNoToken(t *testing.T, errs ...error) {
	t.Helper()
	for _, err := range errs {
		if err != nil && strings.Contains(err.Error(), testToken) {
			t.Fatalf("error %q contains the token", err)
		}
	}
}

func TestOutcomesMatchReconcile(t *testing.T) {
	t.Parallel()
	tests := []struct {
		got  portal.Outcome
		want reconcile.Outcome
	}{
		{portal.OutcomeOK, reconcile.OutcomeOK},
		{portal.OutcomeNotModified, reconcile.OutcomeNotModified},
		{portal.OutcomeUnauthorized, reconcile.OutcomeUnauthorized},
		{portal.OutcomeRateLimited, reconcile.OutcomeRateLimited},
		{portal.OutcomeNetwork, reconcile.OutcomeNetwork},
		{portal.OutcomeRedirect, reconcile.OutcomeRedirect},
		{portal.OutcomeHTTPError, reconcile.OutcomeHTTPError},
		{portal.OutcomeBadBody, reconcile.OutcomeBadBody},
	}
	for _, tc := range tests {
		if string(tc.got) != string(tc.want) {
			t.Errorf("portal outcome %q != reconcile outcome %q", tc.got, tc.want)
		}
	}
}

func TestGetClassifiesStatus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		code     int
		body     string
		want     portal.Outcome
		wantErr  bool
		expected portal.Expected
	}{
		{"200", 200, good, portal.OutcomeOK, false,
			portal.Expected{Labels: portal.Labels{Env: "prod", Space: "sp-1a2b3c4d"}, Revision: revA}},
		{"200 битое тело", 200, `{"labels":{}}`, portal.OutcomeBadBody, true, portal.Expected{}},
		{"200 пустое тело", 200, "", portal.OutcomeBadBody, true, portal.Expected{}},
		{"304", 304, "", portal.OutcomeNotModified, false, portal.Expected{}},
		{"401", 401, "token " + testToken + " revoked", portal.OutcomeUnauthorized, false, portal.Expected{}},
		{"429", 429, "slow down", portal.OutcomeRateLimited, false, portal.Expected{}},
		{"500", 500, "boom " + testToken, portal.OutcomeHTTPError, false, portal.Expected{}},
		{"503", 503, "", portal.OutcomeHTTPError, false, portal.Expected{}},
		{"404", 404, "", portal.OutcomeHTTPError, false, portal.Expected{}},
		{"204", 204, "", portal.OutcomeHTTPError, false, portal.Expected{}},
		{"201 с верным телом", 201, good, portal.OutcomeHTTPError, false, portal.Expected{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fp := newFakePortal(t, reply(tc.code, tc.body))
			resp, err := newClient(fp.pool()).Get(context.Background(), portal.Request{URL: fp.url(), Token: testToken})
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if resp.Outcome != tc.want {
				t.Fatalf("Outcome = %q, want %q (err %v)", resp.Outcome, tc.want, resp.Err)
			}
			if resp.HTTPStatus != tc.code {
				t.Fatalf("HTTPStatus = %d, want %d", resp.HTTPStatus, tc.code)
			}
			if resp.Expected != tc.expected {
				t.Fatalf("Expected = %+v, want %+v", resp.Expected, tc.expected)
			}
			if (resp.Err != nil) != tc.wantErr {
				t.Fatalf("Err = %v, wantErr %v", resp.Err, tc.wantErr)
			}
			if tc.want == portal.OutcomeBadBody && !errors.Is(resp.Err, portal.ErrBadBody) {
				t.Fatalf("Err = %v, want ErrBadBody", resp.Err)
			}
			// The body of 401 and of errors is neither returned nor logged.
			assertNoToken(t, resp.Err)
			if resp.Err != nil && tc.body != "" && strings.Contains(resp.Err.Error(), tc.body) {
				t.Fatalf("Err %q carries the body", resp.Err)
			}
		})
	}
}

func TestGetDoesNotFollowRedirects(t *testing.T) {
	t.Parallel()
	for _, code := range []int{301, 302, 303, 307, 308} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			t.Parallel()
			second := newFakePortal(t, reply(200, good))
			first := newFakePortal(t, func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, second.url(), code)
			})
			pool := first.pool()
			pool.AddCert(second.srv.Certificate())
			resp, err := newClient(pool).Get(context.Background(), portal.Request{URL: first.url(), Token: testToken})
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if resp.Outcome != portal.OutcomeRedirect || resp.HTTPStatus != code {
				t.Fatalf("resp = %q/%d, want redirect/%d", resp.Outcome, resp.HTTPStatus, code)
			}
			if n := second.hits.Load(); n != 0 {
				t.Fatalf("redirect target received %d requests, want 0", n)
			}
			if resp.Expected != (portal.Expected{}) {
				t.Fatalf("Expected = %+v, want zero", resp.Expected)
			}
			assertNoToken(t, resp.Err)
		})
	}
}

func TestGetTimeout(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	fp := newFakePortal(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
		w.WriteHeader(http.StatusOK)
	})
	defer close(release)
	c := portal.New(portal.Config{RootCAs: fp.pool(), Version: "4.0.0", RequestTimeout: 200 * time.Millisecond})
	start := time.Now()
	resp, err := c.Get(context.Background(), portal.Request{URL: fp.url(), Token: testToken})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if resp.Outcome != portal.OutcomeNetwork || resp.HTTPStatus != 0 || resp.Err == nil {
		t.Fatalf("resp = %+v, want network/0 with Err", resp)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("timeout took %v", d)
	}
	assertNoToken(t, resp.Err)
}

func TestGetContextCanceled(t *testing.T) {
	t.Parallel()
	fp := newFakePortal(t, reply(200, good))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	resp, err := newClient(fp.pool()).Get(ctx, portal.Request{URL: fp.url(), Token: testToken})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if resp.Outcome != portal.OutcomeNetwork {
		t.Fatalf("Outcome = %q, want network", resp.Outcome)
	}
}

func TestGetUnknownCA(t *testing.T) {
	t.Parallel()
	fp := newFakePortal(t, reply(200, good))
	// An empty pool, and the system roots (nil) do not know the test CA either.
	for name, pool := range map[string]*x509.CertPool{"пустой пул": x509.NewCertPool(), "системные корни": nil} {
		resp, err := newClient(pool).Get(context.Background(), portal.Request{URL: fp.url(), Token: testToken})
		if err != nil {
			t.Fatalf("%s: Get: %v", name, err)
		}
		if resp.Outcome != portal.OutcomeNetwork || resp.Err == nil {
			t.Fatalf("%s: resp = %+v, want network", name, resp)
		}
		assertNoToken(t, resp.Err)
	}
	if n := fp.hits.Load(); n != 0 {
		t.Fatalf("handler ran %d times over an untrusted TLS", n)
	}
}

func TestGetBodyLimit(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		size int
		want portal.Outcome
	}{
		{"4096 байт", 4096, portal.OutcomeOK},
		{"4097 байт", 4097, portal.OutcomeBadBody},
		{"1 МБ", 1 << 20, portal.OutcomeBadBody},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body := good + strings.Repeat(" ", tc.size-len(good))
			fp := newFakePortal(t, reply(200, body))
			resp, err := newClient(fp.pool()).Get(context.Background(), portal.Request{URL: fp.url(), Token: testToken})
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if resp.Outcome != tc.want {
				t.Fatalf("Outcome = %q, want %q (err %v)", resp.Outcome, tc.want, resp.Err)
			}
		})
	}
}

func TestGetHeaders(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		req         portal.Request
		ifNoneMatch string
		applied     string
	}{
		{"без ревизии", portal.Request{}, "", ""},
		{"с ревизией", portal.Request{Revision: revA}, `"` + revA + `"`, ""},
		{"с ревизией и отчётом", portal.Request{Revision: revA, ReportApplied: true}, `"` + revA + `"`, revA},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fp := newFakePortal(t, reply(304, ""))
			req := tc.req
			req.URL, req.Token = fp.url(), testToken
			if _, err := newClient(fp.pool()).Get(context.Background(), req); err != nil {
				t.Fatalf("Get: %v", err)
			}
			r := fp.last(t)
			if r.Method != http.MethodGet {
				t.Errorf("method = %s, want GET", r.Method)
			}
			if r.URL.Path != apiPath {
				t.Errorf("path = %s", r.URL.Path)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer "+testToken {
				t.Errorf("Authorization = %q", got)
			}
			if got := r.Header.Get("User-Agent"); got != "stvor-agent/4.0.0" {
				t.Errorf("User-Agent = %q", got)
			}
			if got, ok := header(r, "If-None-Match"); got != tc.ifNoneMatch || ok != (tc.ifNoneMatch != "") {
				t.Errorf("If-None-Match = %q (present %v), want %q", got, ok, tc.ifNoneMatch)
			}
			if got, ok := header(r, "X-Applied-Revision"); got != tc.applied || ok != (tc.applied != "") {
				t.Errorf("X-Applied-Revision = %q (present %v), want %q", got, ok, tc.applied)
			}
		})
	}
}

func header(r *http.Request, name string) (string, bool) {
	v, ok := r.Header[http.CanonicalHeaderKey(name)]
	if !ok {
		return "", false
	}
	return strings.Join(v, ","), true
}

func TestGetUserAgentDev(t *testing.T) {
	t.Parallel()
	fp := newFakePortal(t, reply(304, ""))
	c := portal.New(portal.Config{RootCAs: fp.pool(), Version: " v4.1.0 "})
	if _, err := c.Get(context.Background(), portal.Request{URL: fp.url(), Token: testToken}); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got := fp.last(t).Header.Get("User-Agent"); got != "stvor-agent/4.1.0" {
		t.Fatalf("User-Agent = %q", got)
	}
}

// Requests that must not reach the network at all.
func TestGetRejectsBeforeNetwork(t *testing.T) {
	t.Parallel()
	var plainHits atomic.Int32
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		plainHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(plain.Close)
	fp := newFakePortal(t, reply(200, good))
	tests := []struct {
		name string
		req  portal.Request
	}{
		{"http://", portal.Request{URL: plain.URL + apiPath, Token: testToken}},
		{"другой путь", portal.Request{URL: fp.srv.URL + "/robot-api/v1/other", Token: testToken}},
		{"путь с хвостом", portal.Request{URL: fp.url() + "?x=1", Token: testToken}},
		{"userinfo", portal.Request{URL: strings.Replace(fp.url(), "https://", "https://u:p@", 1), Token: testToken}},
		{"кавычка", portal.Request{URL: fp.url() + `"`, Token: testToken}},
		{"пустой URL", portal.Request{Token: testToken}},
		{"пустой токен", portal.Request{URL: fp.url()}},
		{"короткий токен", portal.Request{URL: fp.url(), Token: token}},
		{"токен с переводом строки", portal.Request{URL: fp.url(), Token: token + "\n"}},
		{"токен с пробелом", portal.Request{URL: fp.url(), Token: token[:42] + " "}},
		{"ревизия не hex", portal.Request{URL: fp.url(), Token: testToken, Revision: "xyz"}},
		{"ревизия в кавычках", portal.Request{URL: fp.url(), Token: testToken, Revision: `"` + revA + `"`}},
		{"отчёт без ревизии", portal.Request{URL: fp.url(), Token: testToken, ReportApplied: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := newClient(fp.pool()).Get(context.Background(), tc.req)
			if !errors.Is(err, portal.ErrInvalidRequest) {
				t.Fatalf("Get err = %v, want ErrInvalidRequest", err)
			}
			if resp != (portal.Response{}) {
				t.Fatalf("resp = %+v, want zero", resp)
			}
			assertNoToken(t, err)
		})
	}
	if n := plainHits.Load() + fp.hits.Load(); n != 0 {
		t.Fatalf("servers received %d requests, want 0", n)
	}
}
