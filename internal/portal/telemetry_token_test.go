package portal_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/iamletenkov/stvor-agent/internal/portal"
)

const telemetryTok = "TeLe_metry-token-0123456789abcdefghijklmn"

func TestTelemetryToken(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		code    int
		body    string
		want    portal.Outcome
		wantTok portal.TelemetryToken
	}{
		{"ok", 200, `{"token":"` + telemetryTok + `","url":"https://other.example/robot-api/v1"}`, portal.OutcomeOK, telemetryTok},
		{"no token", 200, `{"url":"x"}`, portal.OutcomeBadBody, ""},
		{"short token", 200, `{"token":"abc"}`, portal.OutcomeBadBody, ""},
		{"token with space", 200, `{"token":"` + telemetryTok + ` x"}`, portal.OutcomeBadBody, ""},
		{"not json", 200, `token=` + telemetryTok, portal.OutcomeBadBody, ""},
		{"too big", 200, `{"token":"` + telemetryTok + `","pad":"` + strings.Repeat("a", 5000) + `"}`, portal.OutcomeBadBody, ""},
		{"401", 401, `{}`, portal.OutcomeUnauthorized, ""},
		{"429", 429, ``, portal.OutcomeRateLimited, ""},
		{"302", 302, ``, portal.OutcomeRedirect, ""},
		{"404 old portal", 404, ``, portal.OutcomeHTTPError, ""},
		{"500", 500, ``, portal.OutcomeHTTPError, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fp := newFakePortal(t, reply(tc.code, tc.body))
			resp, err := newClient(fp.pool()).TelemetryToken(context.Background(),
				portal.Request{URL: fp.url(), Token: testToken, Revision: "0123456789abcdef", ReportApplied: true})
			if err != nil {
				t.Fatalf("TelemetryToken: %v", err)
			}
			if resp.Outcome != tc.want || resp.HTTPStatus != tc.code || resp.Token != tc.wantTok {
				t.Errorf("got %s %d tok-ok=%t, want %s %d", resp.Outcome, resp.HTTPStatus, resp.Token == tc.wantTok, tc.want, tc.code)
			}
			r := fp.last(t)
			if r.Method != http.MethodPost || r.URL.Path != "/robot-api/v1/telemetry-token" {
				t.Errorf("request %s %s", r.Method, r.URL.Path)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer "+testToken {
				t.Errorf("Authorization = %q", got)
			}
			if r.Header.Get("If-None-Match") != "" || r.Header.Get("X-Applied-Revision") != "" {
				t.Error("labels revision headers sent to telemetry-token")
			}
			assertNoToken(t, resp.Err)
			if resp.Err != nil && strings.Contains(resp.Err.Error(), telemetryTok) {
				t.Errorf("error quotes the telemetry token: %v", resp.Err)
			}
		})
	}
}

func TestTelemetryTokenRefusesBeforeNetwork(t *testing.T) {
	t.Parallel()
	fp := newFakePortal(t, reply(200, `{}`))
	c := newClient(fp.pool())
	for _, r := range []portal.Request{
		{URL: "http://stvor.example/robot-api/v1/labels", Token: testToken},
		{URL: fp.url(), Token: "short"},
	} {
		if _, err := c.TelemetryToken(context.Background(), r); !errors.Is(err, portal.ErrInvalidRequest) {
			t.Errorf("%s: err = %v, want ErrInvalidRequest", r.URL, err)
		}
	}
	if fp.hits.Load() != 0 {
		t.Error("a refused request reached the portal")
	}
}

func TestTelemetryTokenHidden(t *testing.T) {
	t.Parallel()
	tok := portal.TelemetryToken(telemetryTok)
	for _, s := range []string{fmt.Sprint(tok), fmt.Sprintf("%v %+v %#v %s", tok, tok, tok, tok)} {
		if strings.Contains(s, telemetryTok) {
			t.Errorf("printed token: %q", s)
		}
	}
}

func TestRobotAPIBase(t *testing.T) {
	t.Parallel()
	if b, ok := portal.RobotAPIBase("https://stvor.example:8443/robot-api/v1/labels"); !ok || b != "https://stvor.example:8443/robot-api/v1" {
		t.Errorf("base = %q %t", b, ok)
	}
	if _, ok := portal.RobotAPIBase("https://stvor.example/robot-api/v1/metrics"); ok {
		t.Error("not a labels address accepted")
	}
}
