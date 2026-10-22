package telemetry

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/iamletenkov/stvor-agent/internal/version"
)

// Credentials are the base URL of the robot API and the telemetry token.
type Credentials struct {
	BaseURL string // https://<host>[:port]/robot-api/v1
	Token   string // only ever put into Authorization
}

// TokenSource gives the telemetry service its credentials. The root service
// `run` requests the token (POST /robot-api/v1/telemetry-token) and writes
// it with the base URL to /etc/stvor-agent-telemetry/token (Stvor plan R4;
// the implementation over that file is the service's, Task 18).
type TokenSource interface {
	// Credentials re-reads the credentials: `run` may have rotated them.
	Credentials() (Credentials, error)
	// Rejected reports that the portal refused the token Credentials gave:
	// the service writes the token-rejected marker, `run` asks for a new one.
	Rejected() error
}

// ErrBadCredentials means the token file is not by the format; the text never
// carries the token.
var ErrBadCredentials = errors.New("файл токена телеметрии не по формату")

var (
	reBaseURL = regexp.MustCompile(`^https://[a-z0-9.-]+(:[0-9]{1,5})?/robot-api/v1$`)
	reToken   = regexp.MustCompile(`^[A-Za-z0-9_-]{32,128}$`)
)

// maxCredentialsBytes bounds the token file.
const maxCredentialsBytes = 4 << 10

// ParseCredentials reads the token file: lines url=<base> and token=<token>;
// empty lines and # comments are skipped, anything else is an error.
func ParseCredentials(data []byte) (Credentials, error) {
	if len(data) > maxCredentialsBytes {
		return Credentials{}, fmt.Errorf("%w: больше %d байт", ErrBadCredentials, maxCredentialsBytes)
	}
	var c Credentials
	sc := bufio.NewScanner(bytes.NewReader(data))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		switch {
		case ok && k == "url" && c.BaseURL == "":
			c.BaseURL = v
		case ok && k == "token" && c.Token == "":
			c.Token = v
		default:
			return Credentials{}, fmt.Errorf("%w: строка %d", ErrBadCredentials, n)
		}
	}
	return c, c.validate()
}

func (c Credentials) validate() error {
	switch {
	case !reBaseURL.MatchString(c.BaseURL):
		return fmt.Errorf("%w: url не https://<хост>[:порт]/robot-api/v1", ErrBadCredentials)
	case !reToken.MatchString(c.Token):
		return fmt.Errorf("%w: токен не 32–128 знаков [A-Za-z0-9_-]", ErrBadCredentials)
	}
	return nil
}

// Default timeouts, as for the labels client.
const (
	DefaultConnectTimeout = 10 * time.Second
	DefaultRequestTimeout = 30 * time.Second
	// maxAnswer bounds the answer body the agent reads.
	maxAnswer = 64 << 10
)

// ClientConfig configures the HTTP side. Zero values take the defaults.
type ClientConfig struct {
	// RootCAs are the trusted roots; nil — the system roots.
	RootCAs        *x509.CertPool
	Version        string // User-Agent: stvor-agent/<version>
	ConnectTimeout time.Duration
	RequestTimeout time.Duration
}

// client is the HTTP transport: https only, TLS ≥ 1.2, proxy from the
// environment (HTTPS_PROXY), redirects never followed.
type client struct {
	http      *http.Client
	userAgent string
}

func newClient(cfg ClientConfig) *client {
	connect := cfg.ConnectTimeout
	if connect <= 0 {
		connect = DefaultConnectTimeout
	}
	total := cfg.RequestTimeout
	if total <= 0 {
		total = DefaultRequestTimeout
	}
	tr := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         (&net.Dialer{Timeout: connect}).DialContext,
		TLSHandshakeTimeout: connect,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: cfg.RootCAs},
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        1,
		IdleConnTimeout:     90 * time.Second,
	}
	return &client{
		http: &http.Client{
			Transport:     tr,
			Timeout:       total,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		userAgent: "stvor-agent/" + version.Short(cfg.Version),
	}
}

// answer is one HTTP exchange.
type answer struct {
	status     int // 0 — no answer
	body       []byte
	retryAfter time.Duration
	hasRetry   bool
	err        error // network; never carries the token
}

// do sends gz (a gzip JSON body) to base+path.
func (c *client) do(ctx context.Context, method string, cr Credentials, path string, gz []byte, now time.Time) answer {
	req, err := http.NewRequestWithContext(ctx, method, cr.BaseURL+path, bytes.NewReader(gz))
	if err != nil {
		return answer{err: errors.New("адрес портала не разобран")}
	}
	req.Header.Set("Authorization", "Bearer "+cr.Token)
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	resp, err := c.http.Do(req)
	if err != nil {
		// *url.Error quotes the URL (no secret in it) and the cause.
		return answer{err: fmt.Errorf("запрос к порталу: %w", err)}
	}
	defer func() { _ = resp.Body.Close() }()
	a := answer{status: resp.StatusCode}
	a.body, err = io.ReadAll(io.LimitReader(resp.Body, maxAnswer+1))
	switch {
	case err != nil:
		a.err = fmt.Errorf("чтение ответа портала: %w", err)
	case len(a.body) > maxAnswer:
		a.body, a.err = nil, errors.New("ответ портала больше 64 КиБ")
	}
	a.retryAfter, a.hasRetry = parseRetryAfter(resp.Header.Get("Retry-After"), now)
	return a
}

// parseRetryAfter reads delta-seconds or an HTTP date.
func parseRetryAfter(v string, now time.Time) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		if n < 0 {
			return 0, false
		}
		return time.Duration(min(n, 1<<20)) * time.Second, true
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(t.Sub(now), 0), true
	}
	return 0, false
}

// gzipper compresses JSON bodies with one reused writer. A new writer at
// BestCompression allocates and zeroes ~1 MB of tables: on a Jetson Nano that
// was a third of the bytes the service allocated and an eighth of its CPU
// (docs/research/2026-10-22-telemetry-budget-jetson.md). Not safe for
// concurrent use: the Sender calls it under its send lock.
type gzipper struct{ zw *gzip.Writer }

// gzip compresses raw into a new slice.
func (g *gzipper) gzip(raw []byte) ([]byte, error) {
	var buf bytes.Buffer
	if g.zw == nil {
		zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		if err != nil {
			return nil, err
		}
		g.zw = zw
	} else {
		g.zw.Reset(&buf)
	}
	if _, err := g.zw.Write(raw); err != nil {
		return nil, err
	}
	if err := g.zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
