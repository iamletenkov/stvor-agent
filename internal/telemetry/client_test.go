package telemetry

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"maps"
	"math"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamletenkov/stvor-agent/internal/buffer"
	"github.com/iamletenkov/stvor-agent/internal/inventory"
)

// The compiled-in contract is the byte copy pinned by
// tools/telemetry-contract.sha256 and parses with the ranges the agent uses.
func TestContractPin(t *testing.T) {
	t.Parallel()
	f, err := os.Open("../../tools/telemetry-contract.sha256")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var pin string
	for sc := bufio.NewScanner(f); sc.Scan(); {
		if line := sc.Text(); line != "" && !strings.HasPrefix(line, "#") {
			pin = strings.Fields(line)[0]
			break
		}
	}
	sum := sha256.Sum256(contractV1)
	if got := hex.EncodeToString(sum[:]); got != pin {
		t.Fatalf("contract/v1.json sha256 %s, pin %s", got, pin)
	}
	k, err := ContractV1()
	if err != nil {
		t.Fatal(err)
	}
	if k.Ranges.FastIntervalS != [2]int{5, 300} || k.Ranges.DetailMaxS != 900 || k.Limits.BodyGzipMax != 1<<20 ||
		k.Metrics["system.cpu.time"] != "counter" || !k.Capabilities["psi"] || k.BackoffMax != 600 {
		t.Fatalf("contract parsed as %+v", k)
	}
	if _, err := parseContract([]byte(`{"contract":"stvor-robot-telemetry","version":1,"schema":1}`)); err == nil {
		t.Fatal("a contract without limits and ranges parsed")
	}
}

func TestParseCredentials(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, in string
		ok       bool
	}{
		{"valid", "# от run\nurl=https://stvor.example:8443/robot-api/v1\ntoken=" + testToken + "\n", true},
		{"http", "url=http://stvor.example/robot-api/v1\ntoken=" + testToken, false},
		{"other path", "url=https://stvor.example/elsewhere\ntoken=" + testToken, false},
		{"userinfo", "url=https://u@stvor.example/robot-api/v1\ntoken=" + testToken, false},
		{"short token", "url=https://stvor.example/robot-api/v1\ntoken=abc", false},
		{"token with space", "url=https://stvor.example/robot-api/v1\ntoken=" + testToken + " x", false},
		{"unknown key", "url=https://stvor.example/robot-api/v1\ntoken=" + testToken + "\ncmd=rm", false},
		{"twice", "url=https://a/robot-api/v1\nurl=https://b/robot-api/v1\ntoken=" + testToken, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, err := ParseCredentials([]byte(tc.in))
			if (err == nil) != tc.ok {
				t.Fatalf("err = %v, want ok=%v", err, tc.ok)
			}
			if err != nil && (!errors.Is(err, ErrBadCredentials) || strings.Contains(err.Error(), testToken)) {
				t.Fatalf("err %q: want ErrBadCredentials without the token", err)
			}
			if tc.ok && c.Token != testToken {
				t.Fatalf("token %q", c.Token)
			}
		})
	}
}

// A fresh batch goes to POST /metrics as gzip JSON by the contract with the
// identity of the buffer chunk; after 200 it leaves the buffer.
func TestStepSendsBatchByContract(t *testing.T) {
	t.Parallel()
	e := newEnv(t, ok)
	for range 3 {
		e.tick(t)
	}
	if r := e.step(t); r.Outcome != OutcomeSent || r.HTTPStatus != 200 || r.Wait != 0 {
		t.Fatalf("step = %+v", r)
	}
	reqs := e.portal.requests()
	if len(reqs) != 1 {
		t.Fatalf("%d requests", len(reqs))
	}
	q := reqs[0]
	if q.Method != http.MethodPost || q.Path != "/robot-api/v1/metrics" || q.Auth != "Bearer "+testToken || q.Encoding != "gzip" {
		t.Fatalf("request %s %s auth=%q enc=%q", q.Method, q.Path, q.Auth, q.Encoding)
	}
	b := q.Body
	if b["schema"] != 1.0 || b["agent_version"] != "4.1.0" || b["stream"] != e.buf.Stream() || b["boot_id"] != testBoot ||
		b["machine_id_hash"] != testMID || b["clock_synced"] != true || b["sent_at"] != "2026-10-22T12:00:30.000Z" ||
		len(b["batch_id"].(string)) != 26 || b["seq"].(float64) < 1 || b["started_at"] == nil || b["backfill"] != nil {
		t.Fatalf("batch %v", b)
	}
	if caps := b["capabilities"].([]any); len(caps) != 2 || caps[0] != "psi" || caps[1] != "thermal" {
		t.Fatalf("capabilities %v, want the known ones", caps)
	}
	if fu := b["failed_units"].([]any); len(fu) != 1 || fu[0] != "a.service" {
		t.Fatalf("failed_units %v", fu)
	}
	if got := pointsIn(b); got != 9 {
		t.Fatalf("points %d, want 9", got)
	}
	ser := b["series"].([]any)[0].(map[string]any)
	if _, ok := ser["labels"].(map[string]any); !ok {
		t.Fatalf("labels %v, want an object", ser["labels"])
	}
	if p := ser["points"].([]any)[0].([]any); len(p) != 2 {
		t.Fatalf("point %v, want [offset_ms, value]", p)
	}
	if r := e.step(t); r.Outcome != OutcomeIdle {
		t.Fatalf("after ack %+v, want idle", r)
	}
}

// A retry keeps batch_id and seq and carries a new sent_at; the pause grows
// 1 s → 10 min with full jitter (the test jitter returns the ceiling).
func TestRetryKeepsIdentityNewSentAt(t *testing.T) {
	t.Parallel()
	fails := 12
	e := newEnv(t, func(_ request, n int) (int, map[string]string, string) {
		if n <= fails {
			return http.StatusInternalServerError, nil, ""
		}
		return http.StatusOK, nil, `{"status":"accepted"}`
	})
	e.tick(t)
	var waits []time.Duration
	for range fails {
		r := e.step(t)
		if r.Outcome != OutcomeRetry || r.HTTPStatus != 500 {
			t.Fatalf("step %+v", r)
		}
		waits = append(waits, r.Wait)
		e.clk.advance(time.Minute)
	}
	if r := e.step(t); r.Outcome != OutcomeSent {
		t.Fatalf("final %+v", r)
	}
	want := []time.Duration{1, 2, 4, 8, 16, 32, 64, 128, 256, 512, 600, 600}
	for i, w := range waits {
		if w != want[i]*time.Second-1 {
			t.Fatalf("wait %d = %v, want just under %ds", i, w, want[i])
		}
	}
	reqs := e.portal.requests()
	first, last := reqs[0].Body, reqs[len(reqs)-1].Body
	if first["batch_id"] != last["batch_id"] || first["seq"] != last["seq"] || first["stream"] != last["stream"] {
		t.Fatalf("identity changed: %v/%v → %v/%v", first["batch_id"], first["seq"], last["batch_id"], last["seq"])
	}
	if first["sent_at"] == last["sent_at"] || first["sent_mono_ms"] == last["sent_mono_ms"] {
		t.Fatalf("sent_at %v not renewed", last["sent_at"])
	}
	if first["started_at"] != last["started_at"] {
		t.Fatal("started_at changed on retry")
	}
}

// 429 and 503 honour Retry-After (seconds or a date, capped at an hour);
// without it — the exponential pause.
func TestRetryAfter(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status int
		header string
		want   time.Duration
	}{
		{"429 seconds", 429, "7", 7 * time.Second},
		{"503 date", 503, "Thu, 22 Oct 2026 12:01:10 GMT", 60 * time.Second},
		{"429 huge", 429, "99999999", time.Hour},
		{"503 none", 503, "", time.Second - 1},
		{"429 garbage", 429, "soon", time.Second - 1},
		{"429 zero", 429, "0", time.Second},
		{"503 date in the past", 503, "Thu, 22 Oct 2026 11:00:00 GMT", time.Second},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t, func(request, int) (int, map[string]string, string) {
				return tc.status, map[string]string{"Retry-After": tc.header}, ""
			})
			e.tick(t)
			r := e.step(t)
			if r.Outcome != OutcomeRetry || r.Wait != tc.want {
				t.Fatalf("step %+v, want wait %v", r, tc.want)
			}
		})
	}
}

// 413 splits the batch: the halves go with new identities, every point
// arrives exactly once.
func TestSplitOn413(t *testing.T) {
	t.Parallel()
	e := newEnv(t, func(r request, _ int) (int, map[string]string, string) {
		if pointsIn(r.Body) > 6 {
			return http.StatusRequestEntityTooLarge, nil, ""
		}
		return http.StatusOK, nil, `{"status":"accepted"}`
	})
	for range 8 {
		e.tick(t)
	}
	var outcomes []Outcome
	for range 20 {
		r := e.step(t)
		if r.Outcome == OutcomeIdle {
			break
		}
		outcomes = append(outcomes, r.Outcome)
	}
	accepted, seqs := 0, map[float64]bool{}
	for _, q := range e.portal.requests() {
		if n := pointsIn(q.Body); n <= 6 {
			accepted += n
			if seqs[q.Body["seq"].(float64)] {
				t.Fatalf("seq %v accepted twice", q.Body["seq"])
			}
			seqs[q.Body["seq"].(float64)] = true
		}
	}
	if accepted != 24 {
		t.Fatalf("accepted %d points, want 24 (outcomes %v)", accepted, outcomes)
	}
	if outcomes[0] != OutcomeSplit {
		t.Fatalf("outcomes %v, want a split first", outcomes)
	}
}

// One record that the portal still refuses with 413 is dropped, not resent
// forever.
func TestUnsplittableDropped(t *testing.T) {
	t.Parallel()
	e := newEnv(t, func(request, int) (int, map[string]string, string) {
		return http.StatusRequestEntityTooLarge, nil, ""
	})
	e.tick(t)
	if r := e.step(t); r.Outcome != OutcomeDropped {
		t.Fatalf("step %+v, want dropped", r)
	}
	if r := e.step(t); r.Outcome != OutcomeIdle {
		t.Fatalf("after drop %+v", r)
	}
}

// 400 (contract v1 statuses: the batch does not decode or breaks the
// contract) drops the chunk: acked, a journal line with the code, never
// retried, and the next batch goes as usual. 3xx is not followed.
func TestPermanentAndRedirect(t *testing.T) {
	t.Parallel()
	t.Run("400 dropped", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, func(_ request, n int) (int, map[string]string, string) {
			if n == 1 {
				return http.StatusBadRequest, nil, `{"code":"telemetry.invalid"}`
			}
			return http.StatusOK, nil, `{"status":"accepted"}`
		})
		e.tick(t)
		if r := e.step(t); r.Outcome != OutcomeDropped || r.HTTPStatus != 400 || r.Wait != 0 {
			t.Fatalf("step %+v, want dropped 400 without a pause", r)
		}
		if r := e.step(t); r.Outcome != OutcomeIdle || len(e.portal.requests()) != 1 {
			t.Fatalf("the chunk was retried after a 400: %+v, %d requests", r, len(e.portal.requests()))
		}
		logs := e.log.String()
		if !hasLine(logs, `"event":"telemetry.dropped"`) || !hasLine(logs, "telemetry.invalid") {
			t.Fatalf("no journal line for the dropped batch:\n%s", logs)
		}
		e.tick(t)
		if r := e.step(t); r.Outcome != OutcomeSent {
			t.Fatalf("next batch after a 400: %+v", r)
		}
	})
	t.Run("302 not followed", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, func(r request, _ int) (int, map[string]string, string) {
			if r.Path != "/robot-api/v1/metrics" {
				t.Errorf("redirect followed to %s", r.Path)
			}
			return http.StatusFound, map[string]string{"Location": "/elsewhere"}, ""
		})
		e.tick(t)
		if r := e.step(t); r.Outcome != OutcomeRetry || r.HTTPStatus != 302 {
			t.Fatalf("step %+v", r)
		}
		if n := len(e.portal.requests()); n != 1 {
			t.Fatalf("%d requests, want 1", n)
		}
	})
}

// 401: the token is re-read; the same token is reported as rejected and not
// sent again until `run` rotates it; a rotated one goes at once.
func TestUnauthorized(t *testing.T) {
	t.Parallel()
	e := newEnv(t, func(r request, _ int) (int, map[string]string, string) {
		if r.Auth != "Bearer "+testToken2 {
			return http.StatusUnauthorized, nil, ""
		}
		return http.StatusOK, nil, `{"status":"accepted"}`
	})
	e.tick(t)
	r := e.step(t)
	if r.Outcome != OutcomeUnauthorized || r.Wait != TokenWait || e.tokens.rejected != 1 {
		t.Fatalf("step %+v, rejected %d", r, e.tokens.rejected)
	}
	if r := e.step(t); r.Outcome != OutcomeUnauthorized || len(e.portal.requests()) != 1 {
		t.Fatalf("rejected token sent again: %+v, %d requests", r, len(e.portal.requests()))
	}
	e.tokens.set(testToken2)
	if r := e.step(t); r.Outcome != OutcomeSent {
		t.Fatalf("after rotation %+v", r)
	}
	if e.tokens.rejected != 1 {
		t.Fatalf("rejected %d", e.tokens.rejected)
	}
}

// A token rotated while the request was in flight is tried at once, without
// a rejected marker.
func TestUnauthorizedRotatedMeanwhile(t *testing.T) {
	t.Parallel()
	var e *env
	e = newEnv(t, func(r request, _ int) (int, map[string]string, string) {
		if r.Auth == "Bearer "+testToken {
			e.tokens.set(testToken2)
			return http.StatusUnauthorized, nil, ""
		}
		return http.StatusOK, nil, `{"status":"accepted"}`
	})
	e.tick(t)
	if r := e.step(t); r.Outcome != OutcomeUnauthorized || r.Wait != 0 || e.tokens.rejected != 0 {
		t.Fatalf("step %+v, rejected %d", r, e.tokens.rejected)
	}
	if r := e.step(t); r.Outcome != OutcomeSent {
		t.Fatalf("with the new token %+v", r)
	}
}

// Bodies the portal really sends (backend/app/modules/robot_telemetry,
// ingest.py _error and config_body): the agent is tested against them, not
// against bodies of its own.
const (
	portalCloneBody    = `{"code":"clone_rejected","message":"Пачка отклонена: под токеном робота пишет другая машина","error":"telemetry.clone_rejected"}`
	portalNeedBodyBody = `{"code":"need_body","message":"Инвентарь изменился — пришлите тело целиком","status":"need_body"}`
	portalDuplicate    = `{"status":"duplicate","config_hash":"0000000000000000000000000000000000000000000000000000000000000000","code":"duplicate"}`
	portalTailBody     = `{"code":"tail_rejected","message":"Хвост прежней загрузки не принят"}`
)

// 409 clone_rejected (the portal's body) pauses sending for an hour and
// records the event.
func TestCloneRejected(t *testing.T) {
	t.Parallel()
	e := newEnv(t, func(_ request, n int) (int, map[string]string, string) {
		if n == 1 {
			return http.StatusConflict, nil, portalCloneBody
		}
		return http.StatusOK, nil, `{"status":"accepted"}`
	})
	e.tick(t)
	if r := e.step(t); r.Outcome != OutcomeCloneRejected || r.Wait != time.Hour {
		t.Fatalf("step %+v", r)
	}
	e.clk.advance(30 * time.Minute)
	if r := e.step(t); r.Outcome != OutcomeCloneRejected || r.Wait != 30*time.Minute || len(e.portal.requests()) != 1 {
		t.Fatalf("during the pause %+v", r)
	}
	e.clk.advance(30 * time.Minute)
	var events []any
	for range 5 {
		if r := e.step(t); r.Outcome == OutcomeIdle {
			break
		}
	}
	for _, q := range e.portal.requests() {
		if q.Path == "/robot-api/v1/events" {
			events = append(events, q.Body["events"].([]any)...)
		}
	}
	if len(events) != 1 || events[0].(map[string]any)["kind"] != "portal.clone_rejected" {
		t.Fatalf("events %v, want portal.clone_rejected", events)
	}
	ev := events[0].(map[string]any)
	if ev["params"].(map[string]any)["until"] != "2026-10-22T13:00:10.000Z" || len(ev["event_id"].(string)) != 26 {
		t.Fatalf("event %v", ev)
	}
}

// The configuration of an answer applies only inside the compiled-in
// ranges: out of range is ignored and logged, detail_until is cut to 15 min,
// unknown metrics never get enabled, the quota reaches the buffer.
func TestConfigApplied(t *testing.T) {
	t.Parallel()
	hash := strings.Repeat("ab", 32)
	e := newEnv(t, func(_ request, n int) (int, map[string]string, string) {
		if n == 1 {
			return http.StatusAccepted, nil, `{"status":"accepted","config_hash":"` + hash + `",
				"metrics":["system.cpu.time","system.memory.usage","/etc/shadow"],
				"fast_interval_s":30,"slow_interval_s":1000,"inventory_check_s":4,"per_core":true,
				"buffer_quota_mib":64,"autonomy_days":99,"detail_until":"2026-10-22T14:00:00Z",
				"next_after_ms":5000,"dropped":{"series":1}}`
		}
		return http.StatusOK, nil, `{"status":"duplicate","code":"duplicate","next_after_ms":900000}`
	})
	e.tick(t)
	r := e.step(t)
	if r.Outcome != OutcomeSent || r.Wait != 5*time.Second {
		t.Fatalf("step %+v, want the 202 pace", r)
	}
	c := e.s.Config()
	now := e.clk.Now()
	if c.Hash != hash || c.FastInterval != 30*time.Second || c.SlowInterval != time.Minute ||
		c.InventoryCheck != time.Minute || !c.PerCore || c.BufferQuota != 64<<20 || c.Autonomy != 30*24*time.Hour ||
		!c.DetailUntil.Equal(now.Add(15*time.Minute)) || !c.Detail(now) || c.Detail(now.Add(16*time.Minute)) {
		t.Fatalf("config %+v", c)
	}
	if got := c.enabledList(); strings.Join(got, ",") != "system.cpu.time,system.memory.usage" {
		t.Fatalf("metrics %v", got)
	}
	if e.buf.Stats().QuotaBytes != 64<<20 {
		t.Fatalf("buffer quota %d", e.buf.Stats().QuotaBytes)
	}
	logs := e.log.String()
	for _, f := range []string{"slow_interval_s=1000", "inventory_check_s=4", "autonomy_days=99", "metrics=1 вне", "detail_until"} {
		if !hasLine(logs, f) {
			t.Errorf("journal lacks %s:\n%s", f, logs)
		}
	}
	e.tick(t)
	r = e.step(t)
	if r.Outcome != OutcomeDuplicate || r.Wait != 0 {
		t.Fatalf("step %+v: out-of-range next_after_ms or fresh data must not pause", r)
	}
	last := e.portal.requests()[1].Body
	if got := strings.Join(seriesNames(last), ","); got != "system.cpu.time,system.memory.usage" {
		t.Fatalf("series %s, want only the enabled metrics", got)
	}
}

// A config body of the wrong shape is ignored; the batch still counts as
// accepted.
func TestConfigGarbageIgnored(t *testing.T) {
	t.Parallel()
	e := newEnv(t, func(request, int) (int, map[string]string, string) {
		return http.StatusOK, nil, `{"status":"accepted","fast_interval_s":"exec rm -rf /"}`
	})
	before := e.s.Config()
	e.tick(t)
	if r := e.step(t); r.Outcome != OutcomeSent {
		t.Fatalf("step %+v", r)
	}
	if after := e.s.Config(); after.FastInterval != before.FastInterval || len(after.Metrics) != len(before.Metrics) {
		t.Fatalf("config changed: %+v", after)
	}
}

// Events go to POST /events by event_id; the backlog paces by next_after_ms.
func TestEventsSent(t *testing.T) {
	t.Parallel()
	e := newEnv(t, ok)
	for _, k := range []string{"unit.failed", "unit.recovered"} {
		if err := e.buf.AddEvent(buffer.Event{Kind: k, Severity: "info", Params: map[string]any{"unit": "a.service"}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.buf.AddEvent(buffer.Event{Kind: "usb.added", Severity: "info",
		Params: map[string]any{"product": strings.Repeat("x", 4096)}}); err != nil {
		t.Fatal(err)
	}
	if r := e.step(t); r.Outcome != OutcomeSent {
		t.Fatalf("step %+v", r)
	}
	q := e.portal.requests()[0]
	evs := q.Body["events"].([]any)
	if q.Path != "/robot-api/v1/events" || q.Body["schema"] != 1.0 || len(evs) != 3 {
		t.Fatalf("request %s %v", q.Path, q.Body)
	}
	first := evs[0].(map[string]any)
	if len(first["event_id"].(string)) != 26 || first["kind"] != "unit.failed" ||
		first["params"].(map[string]any)["unit"] != "a.service" || !strings.HasSuffix(first["at"].(string), "Z") {
		t.Fatalf("event %v", first)
	}
	if p := evs[2].(map[string]any)["params"].(map[string]any); len(p) != 0 {
		t.Fatalf("oversized params %v, want {}", p)
	}
	if r := e.step(t); r.Outcome != OutcomeIdle {
		t.Fatalf("after ack %+v", r)
	}
}

// Inventory: the hash first; the body only on 409 need_body; the hash
// excludes collected_at.
func TestSyncInventory(t *testing.T) {
	t.Parallel()
	known := ""
	e := newEnv(t, func(r request, _ int) (int, map[string]string, string) {
		if r.Method != http.MethodPut || r.Path != "/robot-api/v1/inventory" {
			return http.StatusNotFound, nil, ""
		}
		h := r.Body["hash"].(string)
		if body, ok := r.Body["body"]; ok && body != nil {
			known = h
			return http.StatusOK, nil, `{"status":"accepted"}`
		}
		if h == known {
			return http.StatusOK, nil, `{"status":"match"}`
		}
		return http.StatusConflict, nil, portalNeedBodyBody
	})
	body := inventory.Body{CollectedAt: "2026-10-22T12:00:00.000Z", System: inventory.System{Arch: "amd64"},
		OS: inventory.OS{ID: "debian", Kernel: "6.1.0"}, CPU: inventory.CPU{Model: "x", Threads: 4},
		SecureBoot: inventory.SecureBoot{State: inventory.SecureBootUnknown}}
	if r := e.s.SyncInventory(context.Background(), body); r.Outcome != OutcomeSent {
		t.Fatalf("first sync %+v", r)
	}
	reqs := e.portal.requests()
	want, _ := inventory.Hash(body)
	if len(reqs) != 2 || reqs[0].Body["body"] != nil || reqs[1].Body["hash"] != want ||
		reqs[1].Body["body"].(map[string]any)["collected_at"] == nil {
		t.Fatalf("requests %v", reqs)
	}
	body.CollectedAt = "2026-10-22T12:01:00.000Z"
	if r := e.s.SyncInventory(context.Background(), body); r.Outcome != OutcomeMatch {
		t.Fatalf("second sync %+v", r)
	}
	if n := len(e.portal.requests()); n != 3 {
		t.Fatalf("%d requests, want the hash only", n)
	}
}

// Neither the journal nor the errors carry the token, whatever the answer.
func TestNoSecretsInLogs(t *testing.T) {
	t.Parallel()
	answers := []int{401, 503, 500, 302, 400, 409, 200}
	e := newEnv(t, func(_ request, n int) (int, map[string]string, string) {
		if n > len(answers) {
			return 200, nil, `{"status":"accepted","metrics":["` + testToken + `"]}`
		}
		return answers[n-1], nil, `{"code":"x","echo":"` + testToken + `"}`
	})
	var errs []string
	for i := range 10 {
		e.tick(t)
		r := e.step(t)
		if r.Err != nil {
			errs = append(errs, r.Err.Error())
		}
		if i == 0 {
			e.tokens.set(testToken2) // after the 401, so the rest goes on
		}
	}
	e.portal.srv.Close()
	e.tick(t)
	if r := e.step(t); r.Outcome != OutcomeRetry || r.Err == nil {
		t.Fatalf("closed portal %+v", r)
	} else {
		errs = append(errs, r.Err.Error())
	}
	all := e.log.String() + strings.Join(errs, "\n")
	for _, tok := range []string{testToken, testToken2} {
		if strings.Contains(all, tok) || strings.Contains(all, tok[:20]) {
			t.Fatalf("token leaked:\n%s", all)
		}
	}
}

// The fuse over 400 (controller ruling, Task 21): BadRequestsInRow answers
// 400 on different batches without a 2xx between them are a portal failure,
// not bad batches — from that one on the data stays in the buffer behind a
// backoff pause with telemetry.portal_rejecting in the journal; the first 2xx
// resets the count and the kept batch goes out unchanged.
func TestBadRequestFuse(t *testing.T) {
	t.Parallel()
	accept := false
	var mu sync.Mutex
	e := newEnv(t, func(request, int) (int, map[string]string, string) {
		mu.Lock()
		defer mu.Unlock()
		if accept {
			return http.StatusOK, nil, `{"status":"accepted"}`
		}
		return http.StatusBadRequest, nil, `{"code":"telemetry.invalid"}`
	})
	for i := 1; i < BadRequestsInRow; i++ {
		e.tick(t)
		if r := e.step(t); r.Outcome != OutcomeDropped {
			t.Fatalf("400 #%d: %+v, want dropped", i, r)
		}
	}
	if hasLine(e.log.String(), "telemetry.portal_rejecting") {
		t.Fatal("portal_rejecting before the fuse")
	}
	e.tick(t)
	for i := range 3 {
		r := e.step(t)
		if r.Outcome != OutcomeRejected || r.HTTPStatus != 400 || r.Wait <= 0 {
			t.Fatalf("400 past the fuse, step %d: %+v, want kept with a pause", i, r)
		}
	}
	if n := strings.Count(e.log.String(), "telemetry.portal_rejecting"); n != 3 {
		t.Fatalf("portal_rejecting lines = %d, want one a pause (3)", n)
	}
	mu.Lock()
	accept = true
	mu.Unlock()
	if r := e.step(t); r.Outcome != OutcomeSent {
		t.Fatalf("after the portal recovered: %+v", r)
	}
	reqs := e.portal.requests()
	kept := reqs[BadRequestsInRow-1].Body
	if last := reqs[len(reqs)-1].Body; last["batch_id"] != kept["batch_id"] || pointsIn(last) == 0 {
		t.Fatalf("the kept batch changed: %v/%v", kept["batch_id"], last["batch_id"])
	}
	if r := e.step(t); r.Outcome != OutcomeIdle {
		t.Fatalf("after accept %+v", r)
	}

	// The 2xx reset the count: a single 400 is a dropped batch again.
	mu.Lock()
	accept = false
	mu.Unlock()
	e.tick(t)
	if r := e.step(t); r.Outcome != OutcomeDropped {
		t.Fatalf("400 after a 2xx: %+v, want dropped", r)
	}
}

// The codes of a 400 that drops a batch come from contract v1 statuses."400".
func TestContractDropCodes(t *testing.T) {
	t.Parallel()
	k, err := ContractV1()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"telemetry.invalid", "telemetry.inventory_hash_mismatch", "telemetry.malformed",
		"telemetry.schema_unsupported"}
	if got := slices.Sorted(maps.Keys(k.DropCodes)); !slices.Equal(got, want) {
		t.Fatalf("drop codes %v, want %v", got, want)
	}
}

// A 400 without a code of the contract (HAProxy, Traefik, any proxy) is not a
// bad batch: the data stays in the buffer behind a backoff pause, as on 422,
// and the answer counts toward the fuse.
func TestBadRequestWithoutContractCode(t *testing.T) {
	t.Parallel()
	for _, body := range []string{`<html><body><h1>400 Bad request</h1></body></html>`, `{"code":"other"}`, ``} {
		t.Run(body, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t, func(_ request, n int) (int, map[string]string, string) {
				switch n {
				case 1:
					return http.StatusBadRequest, nil, body
				case 2, 3:
					return http.StatusBadRequest, nil, `{"code":"telemetry.invalid"}`
				}
				return http.StatusOK, nil, `{"status":"accepted"}`
			})
			e.tick(t)
			r := e.step(t)
			if r.Outcome != OutcomeRejected || r.HTTPStatus != 400 || r.Wait <= 0 {
				t.Fatalf("400 without a contract code: %+v, want kept with a pause", r)
			}
			if hasLine(e.log.String(), "telemetry.dropped") {
				t.Fatal("a batch was dropped on a proxy 400")
			}
			// The same batch again, now a contract 400: the 2nd in a row — dropped.
			if r := e.step(t); r.Outcome != OutcomeDropped {
				t.Fatalf("contract 400 #2: %+v, want dropped", r)
			}
			// The 3rd 400 in a row trips the fuse although only two had a code.
			e.tick(t)
			if r := e.step(t); r.Outcome != OutcomeRejected || r.Wait <= 0 {
				t.Fatalf("contract 400 #3: %+v, want kept by the fuse", r)
			}
			if !hasLine(e.log.String(), "telemetry.portal_rejecting") {
				t.Fatal("no portal_rejecting line at the fuse")
			}
		})
	}
}

// The inventory shares the fuse: its 2xx resets the count, and its 400 is
// the third in a row after two dropped batches.
func TestBadRequestFuseInventory(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	invStatus := http.StatusBadRequest
	e := newEnv(t, func(r request, _ int) (int, map[string]string, string) {
		if r.Path == "/robot-api/v1/inventory" {
			mu.Lock()
			defer mu.Unlock()
			if invStatus == http.StatusOK {
				return http.StatusOK, nil, `{"status":"match"}`
			}
			return http.StatusBadRequest, nil, `{"code":"telemetry.inventory_hash_mismatch"}`
		}
		return http.StatusBadRequest, nil, `{"code":"telemetry.invalid"}`
	})
	body := inventory.Body{System: inventory.System{Arch: "amd64"}, OS: inventory.OS{ID: "debian", Kernel: "6.1.0"},
		CPU: inventory.CPU{Model: "x", Threads: 4}, SecureBoot: inventory.SecureBoot{State: inventory.SecureBootUnknown}}
	for i := range 2 {
		e.tick(t)
		if r := e.step(t); r.Outcome != OutcomeDropped {
			t.Fatalf("batch 400 #%d: %+v", i+1, r)
		}
	}
	if r := e.s.SyncInventory(context.Background(), body); r.Outcome != OutcomeRejected || r.Wait <= 0 {
		t.Fatalf("inventory 400 as the 3rd in a row: %+v, want kept by the fuse", r)
	}
	mu.Lock()
	invStatus = http.StatusOK
	mu.Unlock()
	if r := e.s.SyncInventory(context.Background(), body); r.Outcome != OutcomeMatch {
		t.Fatalf("inventory 2xx: %+v", r)
	}
	e.tick(t)
	if r := e.step(t); r.Outcome != OutcomeDropped {
		t.Fatalf("a batch 400 after the inventory 2xx: %+v, want dropped (count reset)", r)
	}
}

// Contract v1 reply_codes: the outcome comes from "code" only; "error" and
// "status" are for people and journals.
func TestReplyCodesByCodeOnly(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, body string
		want       Outcome
	}{
		{"clone", portalCloneBody, OutcomeCloneRejected},
		{"clone code only", `{"code":"clone_rejected"}`, OutcomeCloneRejected},
		{"clone in error only", `{"code":"other","error":"telemetry.clone_rejected"}`, OutcomeRetry},
		{"clone spelled as error", `{"code":"telemetry.clone_rejected"}`, OutcomeRetry},
		{"tail", portalTailBody, OutcomeDropped},
		{"tail in error only", `{"error":"telemetry.tail_rejected"}`, OutcomeRetry},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t, func(request, int) (int, map[string]string, string) { return http.StatusConflict, nil, tc.body })
			e.tick(t)
			if r := e.step(t); r.Outcome != tc.want {
				t.Fatalf("step %+v, want %s", r, tc.want)
			}
		})
	}
	t.Run("duplicate by status only", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, func(request, int) (int, map[string]string, string) {
			return http.StatusOK, nil, `{"status":"duplicate"}`
		})
		e.tick(t)
		if r := e.step(t); r.Outcome != OutcomeSent {
			t.Fatalf("step %+v, want sent (no code)", r)
		}
	})
	t.Run("need_body by status only", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, func(r request, _ int) (int, map[string]string, string) {
			if r.Body["body"] != nil {
				t.Error("full inventory sent without code need_body")
			}
			return http.StatusConflict, nil, `{"status":"need_body"}`
		})
		if r := e.s.SyncInventory(context.Background(), testInventory()); r.Outcome == OutcomeSent {
			t.Fatalf("inventory %+v", r)
		}
	})
}

// Inventory and events carry the sender identity of the metrics batches —
// machine_id_hash, boot_id, sent_mono_ms — all three together (contract v1),
// or none when one of them is unknown.
func TestSenderIdentityOnInventoryAndEvents(t *testing.T) {
	t.Parallel()
	check := func(t *testing.T, e *env, want bool) {
		t.Helper()
		if err := e.buf.AddEvent(buffer.Event{Kind: "unit.failed", Severity: "warning", At: e.clk.Now(),
			Params: map[string]any{"unit": "a.service"}}); err != nil {
			t.Fatal(err)
		}
		if r := e.step(t); r.Outcome != OutcomeSent {
			t.Fatalf("events %+v", r)
		}
		if r := e.s.SyncInventory(context.Background(), testInventory()); r.Outcome != OutcomeSent {
			t.Fatalf("inventory %+v", r)
		}
		reqs := e.portal.requests()
		if len(reqs) != 3 {
			t.Fatalf("%d requests, want events + inventory hash + body", len(reqs))
		}
		for _, q := range reqs {
			mid, hasMID := q.Body["machine_id_hash"]
			boot, hasBoot := q.Body["boot_id"]
			mono, hasMono := q.Body["sent_mono_ms"]
			if !want {
				if hasMID || hasBoot || hasMono {
					t.Errorf("%s: partial or unknown identity sent: %v %v %v", q.Path, mid, boot, mono)
				}
				continue
			}
			if mid != testMID || boot != testBoot || !hasMono || mono.(float64) <= 0 {
				t.Errorf("%s: identity %v %v %v", q.Path, mid, boot, mono)
			}
		}
	}
	t.Run("known", func(t *testing.T) {
		t.Parallel()
		check(t, newEnv(t, inventoryPortal), true)
	})
	t.Run("boot unknown", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, inventoryPortal)
		meta := e.s.opt.Meta
		e.s.opt.Meta = func() Meta { m := meta(); m.BootID = ""; return m }
		check(t, e, false)
	})
}

func testInventory() inventory.Body {
	return inventory.Body{System: inventory.System{Arch: "amd64"}, OS: inventory.OS{ID: "debian", Kernel: "6.1.0"},
		CPU: inventory.CPU{Model: "x", Threads: 4}, SecureBoot: inventory.SecureBoot{State: inventory.SecureBootUnknown}}
}

// inventoryPortal takes events and wants the inventory body once.
func inventoryPortal(r request, _ int) (int, map[string]string, string) {
	if r.Path == "/robot-api/v1/inventory" && r.Body["body"] == nil {
		return http.StatusConflict, nil, portalNeedBodyBody
	}
	return http.StatusOK, nil, `{"status":"accepted"}`
}

// The portal's next_after_ms of 5000 for the tail is waited as it is: the
// 3 s gap adds nothing on top of a longer pause.
func TestNextAfterOverGap(t *testing.T) {
	t.Parallel()
	var e *env
	var at []int64
	var mu sync.Mutex
	e = newEnv(t, func(request, int) (int, map[string]string, string) {
		mu.Lock()
		at = append(at, e.clk.MonoMS())
		mu.Unlock()
		return http.StatusAccepted, nil, `{"status":"accepted","next_after_ms":5000}`
	})
	e.tick(t)
	r := e.step(t)
	if r.Outcome != OutcomeSent || r.Wait != 5*time.Second {
		t.Fatalf("step %+v, want the 5 s pace", r)
	}
	e.clk.advance(r.Wait) // the loop waits it
	e.record(t)
	if r := e.step(t); r.Outcome != OutcomeSent {
		t.Fatalf("step %+v", r)
	}
	mu.Lock()
	defer mu.Unlock()
	if gap := time.Duration(at[1]-at[0]) * time.Millisecond; gap != 5*time.Second {
		t.Fatalf("requests %v apart, want exactly next_after (5 s)", gap)
	}
}

// 200 duplicate as the portal sends it: the chunk is acked, not resent.
func TestDuplicatePortalBody(t *testing.T) {
	t.Parallel()
	e := newEnv(t, func(request, int) (int, map[string]string, string) { return http.StatusOK, nil, portalDuplicate })
	e.tick(t)
	if r := e.step(t); r.Outcome != OutcomeDuplicate {
		t.Fatalf("step %+v, want duplicate", r)
	}
	if r := e.step(t); r.Outcome != OutcomeIdle || len(e.portal.requests()) != 1 {
		t.Fatalf("after duplicate %+v, %d requests", r, len(e.portal.requests()))
	}
}

// 409 tail_rejected: the portal will never take this batch (the tail of an
// earlier boot with an approximate clock). It is acked — the head of the
// buffer no longer blocks the fresh data — logged as telemetry.tail_rejected,
// never retried and not counted toward the 400 fuse.
func TestTailRejected(t *testing.T) {
	t.Parallel()
	for _, body := range []string{portalTailBody} {
		t.Run(body, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t, func(_ request, n int) (int, map[string]string, string) {
				switch n {
				case 1:
					return http.StatusConflict, nil, body
				case 2, 3:
					return http.StatusBadRequest, nil, `{"code":"telemetry.invalid"}`
				}
				return http.StatusOK, nil, `{"status":"accepted"}`
			})
			e.tick(t)
			if r := e.step(t); r.Outcome != OutcomeDropped || r.HTTPStatus != http.StatusConflict || r.Wait != 0 {
				t.Fatalf("tail_rejected: %+v, want dropped without a pause", r)
			}
			if !hasLine(e.log.String(), `"event":"telemetry.tail_rejected"`) {
				t.Fatalf("no telemetry.tail_rejected line:\n%s", e.log.String())
			}
			if r := e.step(t); r.Outcome != OutcomeIdle || len(e.portal.requests()) != 1 {
				t.Fatalf("the rejected tail was resent: %+v", r)
			}
			// Two contract 400 after it are both dropped: tail_rejected is not
			// in the fuse count (with it, the second would trip the fuse).
			for i := range 2 {
				e.tick(t)
				if r := e.step(t); r.Outcome != OutcomeDropped {
					t.Fatalf("400 #%d after tail_rejected: %+v", i+1, r)
				}
			}
			e.tick(t)
			if r := e.step(t); r.Outcome != OutcomeSent {
				t.Fatalf("fresh batch after the rejected tail: %+v", r)
			}
		})
	}
}

// Requests to the portal are at least MinRequestGap apart — batches, events
// and the inventory together (≤ 20 a minute, the Traefik limit per robot);
// next_after_ms still applies on top of it.
func TestRequestPacing(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var at []int64
	var e *env
	e = newEnv(t, func(r request, _ int) (int, map[string]string, string) {
		mu.Lock()
		at = append(at, e.clk.MonoMS())
		mu.Unlock()
		if r.Path == "/robot-api/v1/inventory" {
			return http.StatusOK, nil, `{"status":"match"}`
		}
		return http.StatusOK, nil, `{"status":"accepted"}`
	})
	body := inventory.Body{System: inventory.System{Arch: "amd64"}, OS: inventory.OS{ID: "debian", Kernel: "6.1.0"},
		CPU: inventory.CPU{Model: "x", Threads: 4}, SecureBoot: inventory.SecureBoot{State: inventory.SecureBootUnknown}}
	e.tick(t)
	if r := e.step(t); r.Outcome != OutcomeSent {
		t.Fatalf("step %+v", r)
	}
	if r := e.s.SyncInventory(context.Background(), body); r.Outcome != OutcomeMatch {
		t.Fatalf("inventory %+v", r)
	}
	e.tick(t) // +10 s: past the gap, no wait
	if r := e.step(t); r.Outcome != OutcomeSent {
		t.Fatalf("step %+v", r)
	}
	if err := e.buf.AddEvent(buffer.Event{Kind: "system.boot", Severity: "info", At: e.clk.Now()}); err != nil {
		t.Fatal(err)
	}
	if r := e.step(t); r.Outcome != OutcomeSent {
		t.Fatalf("events step %+v", r)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(at) != 4 {
		t.Fatalf("%d requests, want 4", len(at))
	}
	for i := 1; i < len(at); i++ {
		if gap := time.Duration(at[i]-at[i-1]) * time.Millisecond; gap < MinRequestGap {
			t.Errorf("requests %d and %d %v apart, want ≥ %v", i, i+1, gap, MinRequestGap)
		}
	}
	if gap := time.Duration(at[2]-at[1]) * time.Millisecond; gap != 10*time.Second {
		t.Errorf("a request past the gap waited: %v apart, want the 10 s of the tick", gap)
	}
}

// A series of 422 never empties the buffer: backoff, then a long pause after
// RejectsInRow refusals; once the portal accepts, the same batch arrives.
func TestRejectSeriesKeepsBuffer(t *testing.T) {
	t.Parallel()
	accept := false
	var mu sync.Mutex
	e := newEnv(t, func(request, int) (int, map[string]string, string) {
		mu.Lock()
		defer mu.Unlock()
		if accept {
			return http.StatusOK, nil, `{"status":"accepted"}`
		}
		return http.StatusUnprocessableEntity, nil, `{"code":"telemetry.schema"}`
	})
	e.tick(t)
	e.tick(t)
	for i := 1; i <= RejectsInRow+2; i++ {
		r := e.step(t)
		if r.Outcome != OutcomeRejected {
			t.Fatalf("step %d: %+v", i, r)
		}
		if long := i >= RejectsInRow; long != (r.Wait == RejectPause) {
			t.Fatalf("step %d: wait %v", i, r.Wait)
		}
	}
	mu.Lock()
	accept = true
	mu.Unlock()
	if r := e.step(t); r.Outcome != OutcomeSent {
		t.Fatalf("after the portal recovered: %+v", r)
	}
	reqs := e.portal.requests()
	first, last := reqs[0].Body, reqs[len(reqs)-1].Body
	if first["batch_id"] != last["batch_id"] || pointsIn(last) != pointsIn(first) || pointsIn(last) == 0 {
		t.Fatalf("batch changed: %v/%v, %d/%d points", first["batch_id"], last["batch_id"], pointsIn(first), pointsIn(last))
	}
	if r := e.step(t); r.Outcome != OutcomeIdle {
		t.Fatalf("after accept %+v", r)
	}
}

// 409 without clone_rejected on /metrics is a retry; the data stays.
func TestConflictNotCloneOnMetrics(t *testing.T) {
	t.Parallel()
	e := newEnv(t, func(_ request, n int) (int, map[string]string, string) {
		if n == 1 {
			return http.StatusConflict, nil, `{"code":"other"}`
		}
		return http.StatusOK, nil, `{"status":"accepted"}`
	})
	e.tick(t)
	if r := e.step(t); r.Outcome != OutcomeRetry || r.HTTPStatus != 409 {
		t.Fatalf("step %+v", r)
	}
	if r := e.step(t); r.Outcome != OutcomeSent {
		t.Fatalf("retry %+v", r)
	}
}

// Events are checked against events.kinds before they leave.
func TestEventsValidatedByContract(t *testing.T) {
	t.Parallel()
	e := newEnv(t, ok)
	for _, ev := range []buffer.Event{
		{Kind: "made.up", Severity: "info"},
		{Kind: "unit.failed", Severity: "fatal", Params: map[string]any{"unit": "a.service", "extra": "x", "pid": 7}},
		{Kind: "disk.threshold", Severity: "warning", Params: map[string]any{"mountpoint": "/", "used_pct": math.NaN(), "threshold_pct": 90.0}},
	} {
		if err := e.buf.AddEvent(ev); err != nil {
			t.Fatal(err)
		}
	}
	if r := e.step(t); r.Outcome != OutcomeSent {
		t.Fatalf("step %+v", r)
	}
	evs := e.portal.requests()[0].Body["events"].([]any)
	if len(evs) != 2 {
		t.Fatalf("events %v, want the unknown kind dropped", evs)
	}
	unit := evs[0].(map[string]any)
	if unit["severity"] != "warning" || len(unit["params"].(map[string]any)) != 1 {
		t.Errorf("unit.failed %v: severity from the contract, params only unit", unit)
	}
	disk := evs[1].(map[string]any)["params"].(map[string]any)
	if _, nan := disk["used_pct"]; nan || disk["threshold_pct"] != 90.0 || disk["mountpoint"] != "/" {
		t.Errorf("disk params %v: NaN dropped, the rest kept", disk)
	}

	only := newEnv(t, ok)
	if err := only.buf.AddEvent(buffer.Event{Kind: "made.up", Severity: "info"}); err != nil {
		t.Fatal(err)
	}
	if r := only.step(t); r.Outcome != OutcomeDropped || len(only.portal.requests()) != 0 {
		t.Fatalf("a chunk of invalid events: %+v, %d requests", r, len(only.portal.requests()))
	}
	if r := only.step(t); r.Outcome != OutcomeIdle {
		t.Fatalf("after the drop %+v", r)
	}
}

// Without the token-rejected marker `run` would never rotate the token: the
// refusal is not remembered, the next Step tries (and marks) again.
func TestUnauthorizedMarkerFailure(t *testing.T) {
	t.Parallel()
	e := newEnv(t, func(request, int) (int, map[string]string, string) {
		return http.StatusUnauthorized, nil, ""
	})
	e.tokens.rejectErr = errors.New("read-only")
	e.tick(t)
	if r := e.step(t); r.Outcome != OutcomeUnauthorized || r.Err == nil || r.Wait != TokenWait {
		t.Fatalf("step %+v", r)
	}
	e.tokens.mu.Lock()
	e.tokens.rejectErr = nil
	e.tokens.mu.Unlock()
	e.step(t)
	if n := len(e.portal.requests()); n != 2 || e.tokens.rejected != 1 {
		t.Fatalf("requests %d, markers %d; want 2 and 1", n, e.tokens.rejected)
	}
}

// Config and RTT do not wait for a request in flight.
func TestConfigNotBlockedByRequest(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	// A failed wait must not leave the handler blocked: the test server
	// would wait for it on Close.
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	entered := make(chan struct{}, 1)
	e := newEnv(t, func(request, int) (int, map[string]string, string) {
		entered <- struct{}{}
		<-release
		return http.StatusOK, nil, `{"status":"accepted"}`
	})
	e.tick(t)
	done := make(chan Result, 1)
	go func() { done <- e.s.Step(context.Background()) }()
	<-entered
	got := make(chan struct{})
	go func() { _ = e.s.Config(); _ = e.s.RTT(); close(got) }()
	select {
	case <-got:
	case <-time.After(3 * time.Second):
		t.Fatal("Config blocked by the request in flight")
	}
	unblock()
	if r := <-done; r.Outcome != OutcomeSent {
		t.Fatalf("step %+v", r)
	}
}
