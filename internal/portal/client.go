package portal

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"time"

	"github.com/iamletenkov/stvor-agent/internal/version"
)

// Outcome is how a poll ended. The values are exactly those of
// reconcile.Outcome (spec §7); a separate type keeps portal free of an import
// of reconcile, which imports portal. reconcile converts with
// reconcile.Outcome(resp.Outcome).
type Outcome string

// Poll outcomes (spec §7).
const (
	OutcomeOK           Outcome = "ok"           // 200 with a valid body
	OutcomeNotModified  Outcome = "not_modified" // 304
	OutcomeUnauthorized Outcome = "unauthorized" // 401: token revoked
	OutcomeRateLimited  Outcome = "rate_limited" // 429
	OutcomeNetwork      Outcome = "network"      // DNS, TCP, TLS, timeout, cancel
	OutcomeRedirect     Outcome = "redirect"     // 3xx other than 304; never followed
	OutcomeHTTPError    Outcome = "http_error"   // 5xx and every other code
	OutcomeBadBody      Outcome = "bad_body"     // 200 body not by the contract
)

// Default timeouts (spec §5.4).
const (
	DefaultConnectTimeout = 10 * time.Second
	DefaultRequestTimeout = 30 * time.Second
)

// ErrInvalidRequest means the request is refused before any network activity
// (URL not by the contract, token or revision of a wrong form, a report
// without a revision). The text never carries the token.
var ErrInvalidRequest = errors.New("запрос к порталу не сформирован")

var (
	reURL   = regexp.MustCompile(`^https://[a-z0-9.-]+(:[0-9]{1,5})?/robot-api/v1/labels$`)
	reToken = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
)

// Config configures a Client. Zero values take the defaults.
type Config struct {
	// RootCAs are the trusted roots; nil means the system roots
	// (/etc/ssl/certs). Tests pass the pool of their httptest server.
	RootCAs *x509.CertPool
	// Version goes to User-Agent: stvor-agent/<version.Short(Version)>.
	Version string
	// ConnectTimeout bounds TCP connect and TLS handshake (default 10 s).
	ConnectTimeout time.Duration
	// RequestTimeout bounds the whole request including the body (default 30 s).
	RequestTimeout time.Duration
}

// Request is one poll. X-Applied-Revision cannot go without If-None-Match of
// the same revision: ReportApplied only reports Revision.
type Request struct {
	URL   string // https://<host>[:port]/robot-api/v1/labels
	Token string // 43 chars [A-Za-z0-9_-]; only ever put into Authorization
	// Revision is the remembered revision, sent as If-None-Match: "<rev>";
	// empty — no If-None-Match.
	Revision string
	// ReportApplied adds X-Applied-Revision: <Revision> (labels in effect).
	ReportApplied bool
}

// Response is the classified answer of the portal.
type Response struct {
	Outcome    Outcome
	HTTPStatus int      // 0 when no response was received
	Expected   Expected // set only when Outcome == OutcomeOK
	// Err explains OutcomeNetwork and OutcomeBadBody for the journal; nil
	// otherwise. It never carries the token or the response body.
	Err error
}

// Client polls GET /robot-api/v1/labels. It is safe for concurrent use.
type Client struct {
	http      *http.Client
	userAgent string
}

// New builds a client: https only, TLS ≥ 1.2, proxy from the environment,
// redirects never followed.
func New(cfg Config) *Client {
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
	return &Client{
		http: &http.Client{
			Transport: tr,
			Timeout:   total,
			// The 3xx answer itself is returned and classified as redirect.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		userAgent: "stvor-agent/" + version.Short(cfg.Version),
	}
}

// Get makes one poll. A non-nil error (ErrInvalidRequest) means nothing was
// sent; every network or HTTP failure is a Response with its Outcome.
func (c *Client) Get(ctx context.Context, r Request) (Response, error) {
	switch {
	case !reURL.MatchString(r.URL):
		return Response{}, fmt.Errorf("%w: адрес не https://<хост>[:порт]/robot-api/v1/labels", ErrInvalidRequest)
	case !reToken.MatchString(r.Token):
		return Response{}, fmt.Errorf("%w: токен не 43 знака [A-Za-z0-9_-]", ErrInvalidRequest)
	case r.Revision != "" && !reRevision.MatchString(r.Revision):
		return Response{}, fmt.Errorf("%w: ревизия не 16 hex", ErrInvalidRequest)
	case r.ReportApplied && r.Revision == "":
		return Response{}, fmt.Errorf("%w: X-Applied-Revision без If-None-Match", ErrInvalidRequest)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.URL, http.NoBody)
	if err != nil {
		return Response{}, fmt.Errorf("%w: адрес не разобран", ErrInvalidRequest)
	}
	req.Header.Set("Authorization", "Bearer "+r.Token)
	req.Header.Set("User-Agent", c.userAgent)
	if r.Revision != "" {
		req.Header.Set("If-None-Match", `"`+r.Revision+`"`)
		if r.ReportApplied {
			req.Header.Set("X-Applied-Revision", r.Revision)
		}
	}

	resp, err := c.http.Do(req)
	if err != nil {
		// *url.Error quotes the URL (no secret in it) and the cause; the
		// token lives only in a header and never reaches the text.
		return Response{Outcome: OutcomeNetwork, Err: fmt.Errorf("запрос к порталу: %w", err)}, nil
	}
	defer func() { _ = resp.Body.Close() }()

	out := Response{HTTPStatus: resp.StatusCode}
	switch code := resp.StatusCode; {
	case code == http.StatusOK:
		body, err := io.ReadAll(io.LimitReader(resp.Body, MaxBody+1))
		if err != nil {
			return Response{Outcome: OutcomeNetwork, HTTPStatus: code, Err: fmt.Errorf("чтение ответа портала: %w", err)}, nil
		}
		exp, err := ParseBody(body)
		if err != nil {
			out.Outcome, out.Err = OutcomeBadBody, err
			return out, nil
		}
		out.Outcome, out.Expected = OutcomeOK, exp
	case code == http.StatusNotModified:
		out.Outcome = OutcomeNotModified
	case code == http.StatusUnauthorized:
		out.Outcome = OutcomeUnauthorized
	case code == http.StatusTooManyRequests:
		out.Outcome = OutcomeRateLimited
	case code >= 300 && code < 400:
		out.Outcome = OutcomeRedirect
	default:
		out.Outcome = OutcomeHTTPError
	}
	return out, nil
}
