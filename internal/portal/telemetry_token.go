package portal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
)

// Telemetry token (Stvor spec "robot telemetry" §5, plan R4): the root
// service asks POST <base>/telemetry-token with the labels token and hands
// the answer to the telemetry service. Additive to the /labels contract
// (ADR-0013 of Stvor): /labels is not changed.

// TelemetryTokenPath is the path of the endpoint under the robot API base.
const TelemetryTokenPath = "/telemetry-token"

// maxTokenAnswer bounds the answer body: {"token": "…", "url": "…"}.
const maxTokenAnswer = 4 << 10

var reTelemetryToken = regexp.MustCompile(`^[A-Za-z0-9_-]{32,128}$`)

// TelemetryToken is a telemetry token; every print shows it hidden.
type TelemetryToken string

// String hides the token.
func (TelemetryToken) String() string { return "<скрыт>" }

// GoString hides the token for %#v.
func (TelemetryToken) GoString() string { return "<скрыт>" }

// TokenResponse is the classified answer of POST /telemetry-token.
type TokenResponse struct {
	Outcome    Outcome // ok, unauthorized, rate_limited, network, redirect, http_error, bad_body
	HTTPStatus int
	Token      TelemetryToken // set only when Outcome == OutcomeOK
	// Err explains network and bad_body; it never carries a token.
	Err error
}

// RobotAPIBase is the base https://<host>[:port]/robot-api/v1 of a labels
// address; false — the address is not a labels address.
func RobotAPIBase(labelsURL string) (string, bool) {
	if !reURL.MatchString(labelsURL) {
		return "", false
	}
	return strings.TrimSuffix(labelsURL, "/labels"), true
}

// TelemetryToken asks the portal for a new telemetry token of this robot. r
// is the labels request (URL and token); revision fields are ignored. Every
// call makes the portal issue a new token and revoke the previous one. A
// non-nil error (ErrInvalidRequest) means nothing was sent.
func (c *Client) TelemetryToken(ctx context.Context, r Request) (TokenResponse, error) {
	base, ok := RobotAPIBase(r.URL)
	switch {
	case !ok:
		return TokenResponse{}, fmt.Errorf("%w: адрес не https://<хост>[:порт]/robot-api/v1/labels", ErrInvalidRequest)
	case !reToken.MatchString(r.Token):
		return TokenResponse{}, fmt.Errorf("%w: токен не 43 знака [A-Za-z0-9_-]", ErrInvalidRequest)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+TelemetryTokenPath, http.NoBody)
	if err != nil {
		return TokenResponse{}, fmt.Errorf("%w: адрес не разобран", ErrInvalidRequest)
	}
	req.Header.Set("Authorization", "Bearer "+r.Token)
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return TokenResponse{Outcome: OutcomeNetwork, Err: fmt.Errorf("запрос к порталу: %w", err)}, nil
	}
	defer func() { _ = resp.Body.Close() }()

	out := TokenResponse{HTTPStatus: resp.StatusCode}
	switch code := resp.StatusCode; {
	case code == http.StatusOK:
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxTokenAnswer+1))
		if err != nil {
			return TokenResponse{Outcome: OutcomeNetwork, HTTPStatus: code, Err: fmt.Errorf("чтение ответа портала: %w", err)}, nil
		}
		tok, err := parseTelemetryToken(body)
		if err != nil {
			out.Outcome, out.Err = OutcomeBadBody, err
			return out, nil
		}
		out.Outcome, out.Token = OutcomeOK, tok
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

// parseTelemetryToken reads {"token": "…"}; other fields (url) are ignored —
// the telemetry service sends only to the base of the labels address (R4).
// Errors never quote the body.
func parseTelemetryToken(body []byte) (TelemetryToken, error) {
	if len(body) > maxTokenAnswer {
		return "", fmt.Errorf("ответ больше %d байт", maxTokenAnswer)
	}
	var a struct {
		Token *string `json:"token"`
	}
	if err := json.Unmarshal(body, &a); err != nil {
		return "", errors.New("ответ не JSON-объект с полем token")
	}
	switch {
	case a.Token == nil:
		return "", errors.New("в ответе нет поля token")
	case !reTelemetryToken.MatchString(*a.Token):
		return "", errors.New("token не 32–128 знаков [A-Za-z0-9_-]")
	}
	return TelemetryToken(*a.Token), nil
}
