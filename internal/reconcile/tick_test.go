package reconcile

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamletenkov/stvor-agent/internal/atomicfile"
	"github.com/iamletenkov/stvor-agent/internal/portal"
	"github.com/iamletenkov/stvor-agent/internal/state"
	"github.com/iamletenkov/stvor-agent/internal/teleport"
	"github.com/iamletenkov/stvor-agent/internal/teleport/teleportfake"
	"github.com/iamletenkov/stvor-agent/internal/token"
)

const (
	testURL   = "https://stvor.example/robot-api/v1/labels"
	otherURL  = "https://other.example/robot-api/v1/labels"
	testToken = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" // 43 chars
	revB      = "bbbbbbbbbbbbbbbb"
	revC      = "cccccccccccccccc"
)

var (
	labelsA = teleport.Labels{Env: "dev", Space: "sp-aaaaaaaa"}
	labelsB = teleport.Labels{Env: "prod", Space: "sp-bbbbbbbb"}
	labelsC = teleport.Labels{Env: "prod", Space: "sp-cccccccc"}
	t0      = time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
)

// --- fakes -----------------------------------------------------------------

type fakeTokens struct {
	tok token.Token
	err error
}

func (f *fakeTokens) Read() (token.Token, error) { return f.tok, f.err }

// memStore keeps state.json in memory; st == nil — no file.
type memStore struct {
	st      *state.State
	loadErr error
	saveErr error
	saves   int
}

func (m *memStore) Load() (state.State, error) {
	if m.loadErr != nil {
		return state.State{}, m.loadErr
	}
	if m.st == nil {
		return state.State{}, fmt.Errorf("mem: %w", state.ErrAbsent)
	}
	return *m.st, nil
}

func (m *memStore) Save(st state.State) error {
	m.saves++
	if m.saveErr != nil {
		return m.saveErr
	}
	m.st = &st
	return nil
}

// fakePortal answers with resp (or err) and records every request.
type fakePortal struct {
	resp portal.Response
	err  error
	reqs []portal.Request
}

func (p *fakePortal) Get(_ context.Context, r portal.Request) (portal.Response, error) {
	p.reqs = append(p.reqs, r)
	return p.resp, p.err
}

func (p *fakePortal) answer200(l teleport.Labels, rev string) {
	p.resp = portal.Response{Outcome: portal.OutcomeOK, HTTPStatus: 200,
		Expected: portal.Expected{Labels: portal.Labels(l), Revision: rev}}
}

func (p *fakePortal) answer304() {
	p.resp = portal.Response{Outcome: portal.OutcomeNotModified, HTTPStatus: 304}
}

func (p *fakePortal) last(t *testing.T) portal.Request {
	t.Helper()
	if len(p.reqs) == 0 {
		t.Fatal("портал не опрошен")
	}
	return p.reqs[len(p.reqs)-1]
}

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

// rig is a tick with all its fakes.
type rig struct {
	tk     *Ticker
	tokens *fakeTokens
	store  *memStore
	portal *fakePortal
	tp     *teleportfake.Fake
	clock  *clock
	log    *bytes.Buffer
}

// newRig: a joined, active node carrying labelsA; enabled for testURL; no state.
func newRig() *rig {
	r := &rig{
		tokens: &fakeTokens{tok: token.Token{URL: testURL, Token: testToken}},
		store:  &memStore{},
		portal: &fakePortal{},
		tp:     &teleportfake.Fake{Current: teleport.Status{Managed: labelsA, Joined: true, Active: true}},
		clock:  &clock{now: t0},
		log:    &bytes.Buffer{},
	}
	r.tk = &Ticker{
		Tokens:   r.tokens,
		States:   r.store,
		Portal:   r.portal,
		Teleport: r.tp,
		Now:      r.clock.Now,
		Log:      slog.New(slog.NewTextHandler(r.log, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	return r
}

// steady puts the robot in the steady state: labelsB at revB expected, applied and in effect.
func (r *rig) steady() {
	l, rev := labelsB, revB
	r.store.st = &state.State{URL: testURL,
		Expected:        &portal.Expected{Labels: portal.Labels(l), Revision: rev},
		AppliedRevision: rev}
	r.tp.Current = teleport.Status{Managed: l, Joined: true, Active: true}
}

func (r *rig) tick(t *testing.T, manual bool) Result {
	t.Helper()
	r.log.Reset()
	res := r.tk.Tick(context.Background(), manual, nil)
	if strings.Contains(r.log.String(), testToken) {
		t.Fatalf("токен в журнале: %s", r.log.String())
	}
	return res
}

func (r *rig) st(t *testing.T) state.State {
	t.Helper()
	if r.store.st == nil {
		t.Fatal("state.json не записан")
	}
	return *r.store.st
}

// unconfirmable: set-managed writes the labels, the restart is never confirmed.
func unconfirmable(f *teleportfake.Fake, l teleport.Labels) (teleport.SetResult, error) {
	f.Current.Managed = l
	f.Current.Pending = true
	return teleport.SetResult{Status: f.Current, Result: teleport.ResultUnconfirmed}, nil
}

// brief keeps the fields the tests compare whole; the end state (Expected,
// Teleport, InEffect) is checked by the tests that need it.
func brief(r Result) Result {
	return Result{Outcome: r.Outcome, HTTPStatus: r.HTTPStatus, Apply: r.Apply, TokenErr: r.TokenErr}
}

func lines(s string) int { return strings.Count(strings.TrimSpace(s), "\n") + 1 }

// --- 200 / 304 --------------------------------------------------------------

// test-labels.sh pull: «200 — метки применены», «один restart», labels-expected,
// labels-applied, «первый запрос без If-None-Match / X-Applied-Revision».
func TestTick200Applies(t *testing.T) {
	t.Parallel()
	r := newRig()
	r.portal.answer200(labelsB, revB)

	res := r.tick(t, false)

	want := Result{Outcome: OutcomeOK, HTTPStatus: 200, Apply: ApplyApplied}
	if !reflect.DeepEqual(brief(res), want) {
		t.Fatalf("result = %+v, want %+v", res, want)
	}
	if got := r.tp.SetCalls(); !reflect.DeepEqual(got, []teleport.Labels{labelsB}) {
		t.Fatalf("set-managed = %v, want ровно [%v]", got, labelsB)
	}
	req := r.portal.last(t)
	if req != (portal.Request{URL: testURL, Token: testToken}) {
		t.Fatalf("первый запрос = %+v: без If-None-Match и X-Applied-Revision", req)
	}
	st := r.st(t)
	if st.Expected == nil || st.Expected.Revision != revB || teleport.Labels(st.Expected.Labels) != labelsB {
		t.Fatalf("expected = %+v", st.Expected)
	}
	if st.AppliedRevision != revB {
		t.Fatalf("applied_revision = %q, want %q", st.AppliedRevision, revB)
	}
	if st.URL != testURL {
		t.Fatalf("url = %q", st.URL)
	}
}

// test-labels.sh pull: «304 — If-None-Match», «X-Applied-Revision», «304 без
// расхождения — агент не перезапускается», «тот же ответ 200 — не перезапускается».
func TestTickSteadyState(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		answer  func(*fakePortal)
		outcome Outcome
		status  int
	}{
		{"304", func(p *fakePortal) { p.answer304() }, OutcomeNotModified, 304},
		{"тот же 200", func(p *fakePortal) { p.answer200(labelsB, revB) }, OutcomeOK, 200},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig()
			r.steady()
			tc.answer(r.portal)

			res := r.tick(t, false)

			want := Result{Outcome: tc.outcome, HTTPStatus: tc.status}
			if !reflect.DeepEqual(brief(res), want) {
				t.Fatalf("result = %+v, want %+v", res, want)
			}
			if req := r.portal.last(t); req.Revision != revB || !req.ReportApplied {
				t.Fatalf("запрос = %+v: нужны If-None-Match и X-Applied-Revision %s", req, revB)
			}
			if n := len(r.tp.SetCalls()); n != 0 {
				t.Fatalf("set-managed вызван %d раз, метки уже действуют", n)
			}
			if st := r.st(t); st.AppliedRevision != revB {
				t.Fatalf("applied_revision = %q", st.AppliedRevision)
			}
		})
	}
}

// A new revision on 200 (the robot moved): applied, and the new revision marked.
func TestTick200NewRevision(t *testing.T) {
	t.Parallel()
	r := newRig()
	r.steady()
	r.portal.answer200(labelsC, revC)

	res := r.tick(t, false)

	if res.Apply != ApplyApplied {
		t.Fatalf("apply = %q", res.Apply)
	}
	if got := r.tp.SetCalls(); !reflect.DeepEqual(got, []teleport.Labels{labelsC}) {
		t.Fatalf("set-managed = %v", got)
	}
	st := r.st(t)
	if st.Expected.Revision != revC || st.AppliedRevision != revC {
		t.Fatalf("state = %+v / %q", st.Expected, st.AppliedRevision)
	}
}

// test-labels.sh pull: «304, конфиг разошёлся — снова применено», «расхождение —
// X-Applied-Revision не сообщается», «самовосстановление — один restart»,
// «ручной env исправлен». R3: the state is reconciled, not the revision.
func TestTick304HandDamage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		damage teleport.Labels
	}{
		{"space", teleport.Labels{Env: "prod", Space: "sp-cccccccc"}},
		{"env", teleport.Labels{Env: "dev", Space: "sp-bbbbbbbb"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig()
			r.steady()
			r.tp.Current.Managed = tc.damage
			r.portal.answer304()

			res := r.tick(t, false)

			if res.Apply != ApplyApplied || res.Outcome != OutcomeNotModified {
				t.Fatalf("result = %+v", res)
			}
			if req := r.portal.last(t); req.ReportApplied || req.Revision != revB {
				t.Fatalf("запрос = %+v: If-None-Match без X-Applied-Revision", req)
			}
			if got := r.tp.SetCalls(); !reflect.DeepEqual(got, []teleport.Labels{labelsB}) {
				t.Fatalf("set-managed = %v", got)
			}
			if r.tp.Current.Managed != labelsB {
				t.Fatalf("метки после тика = %v", r.tp.Current.Managed)
			}
		})
	}
}

// test-labels.sh R3: «убитый тик — перед перезапуском X-Applied-Revision не
// шлётся», «перезапуск на следующем тике», «ревизия отмечена после
// перезапуска»; «агент не поднялся» / «следующий тик — агент перезапущен».
// Labels written but pending (or the agent down): not in effect, set again.
func TestTickPendingOrInactiveReapplies(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		mod  func(*teleport.Status)
	}{
		{"pending", func(s *teleport.Status) { s.Pending = true }},
		{"агент неактивен", func(s *teleport.Status) { s.Active = false }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig()
			r.steady()
			r.store.st.Expected = &portal.Expected{Labels: portal.Labels(labelsC), Revision: revC}
			r.tp.Current.Managed = labelsC
			tc.mod(&r.tp.Current)
			r.portal.answer304()

			res := r.tick(t, false)

			if req := r.portal.last(t); req.ReportApplied {
				t.Fatalf("X-Applied-Revision ушёл при неподтверждённых метках: %+v", req)
			}
			if got := r.tp.SetCalls(); !reflect.DeepEqual(got, []teleport.Labels{labelsC}) {
				t.Fatalf("set-managed = %v", got)
			}
			if res.Apply != ApplyApplied {
				t.Fatalf("apply = %q", res.Apply)
			}
			if st := r.st(t); st.AppliedRevision != revC {
				t.Fatalf("applied_revision = %q, want %q", st.AppliedRevision, revC)
			}
		})
	}
}

// --- set-managed results ----------------------------------------------------

// test-labels.sh pull: «агент не поднялся — ревизия не отмечена», «ожидаемое
// записано», «агент лежит — X-Applied-Revision не сообщается»; R3: «restart
// упал — ревизия не отмечена», «пока маркер есть — X-Applied-Revision не шлётся».
func TestTickUnconfirmedCounts(t *testing.T) {
	t.Parallel()
	r := newRig()
	r.steady()
	r.tp.SetFunc = unconfirmable
	r.portal.answer200(labelsC, revC)

	res := r.tick(t, false)

	if res.Apply != ApplyUnconfirmed {
		t.Fatalf("apply = %q", res.Apply)
	}
	st := r.st(t)
	if st.RestartAttempts != (state.Attempts{Count: 1, Last: t0}) {
		t.Fatalf("restart_attempts = %+v", st.RestartAttempts)
	}
	if st.AppliedRevision != revB {
		t.Fatalf("applied_revision = %q: не подтверждено — остаётся прежняя", st.AppliedRevision)
	}
	if st.Expected.Revision != revC {
		t.Fatalf("ожидаемое не записано: %+v", st.Expected)
	}

	r.clock.now = t0.Add(time.Minute)
	r.portal.answer304()
	res = r.tick(t, false)
	if req := r.portal.last(t); req.ReportApplied || req.Revision != revC {
		t.Fatalf("запрос = %+v", req)
	}
	if res.Apply != ApplyUnconfirmed {
		t.Fatalf("второй тик apply = %q", res.Apply)
	}
	if got := r.st(t).RestartAttempts; got != (state.Attempts{Count: 2, Last: t0.Add(time.Minute)}) {
		t.Fatalf("restart_attempts = %+v", got)
	}
}

// test-labels.sh «пауза: …» — три попытки подряд, пропуск со строкой, за
// секунду до 15 минут — без перезапуска, через 15 минут — попытка 4, после
// неудачи снова 15 минут, восстановление сбрасывает счётчик и отмечает ревизию.
func TestTickPause(t *testing.T) {
	t.Parallel()
	r := newRig()
	r.steady()
	r.tp.SetFunc = unconfirmable
	r.portal.answer200(labelsC, revC)
	at := func(sec int) { r.clock.now = t0.Add(time.Duration(sec) * time.Second) }

	at(0)
	r.tick(t, false)
	r.portal.answer304()
	at(60)
	r.tick(t, false)
	at(120)
	if res := r.tick(t, false); res.Apply != ApplyUnconfirmed {
		t.Fatalf("третья попытка apply = %q", res.Apply)
	}
	if n := len(r.tp.SetCalls()); n != 3 {
		t.Fatalf("попыток = %d, want 3", n)
	}
	if got := r.st(t).RestartAttempts; got != (state.Attempts{Count: 3, Last: t0.Add(120 * time.Second)}) {
		t.Fatalf("restart_attempts = %+v", got)
	}

	at(180)
	res := r.tick(t, false)
	if res.Apply != ApplyPaused || res.Outcome != OutcomeNotModified {
		t.Fatalf("четвёртый тик = %+v, want paused", res)
	}
	if n := len(r.tp.SetCalls()); n != 3 {
		t.Fatalf("в паузе set-managed вызван: %d", n)
	}
	log := r.log.String()
	if !strings.Contains(log, "event=skip") || !strings.Contains(log, "reason=paused") ||
		!strings.Contains(log, "next_in=14m0s") || !strings.Contains(log, "3 неподтверждённых") {
		t.Fatalf("строка паузы: %s", log)
	}

	at(1019) // 120 + 900 - 1
	if res := r.tick(t, false); res.Apply != ApplyPaused {
		t.Fatalf("за секунду до 15 минут apply = %q", res.Apply)
	}
	if req := r.portal.last(t); req.ReportApplied {
		t.Fatal("в паузе ушёл X-Applied-Revision")
	}

	at(1020)
	if res := r.tick(t, false); res.Apply != ApplyUnconfirmed {
		t.Fatalf("через 15 минут apply = %q", res.Apply)
	}
	if n := len(r.tp.SetCalls()); n != 4 {
		t.Fatalf("попыток = %d, want 4", n)
	}
	at(1080)
	if res := r.tick(t, false); res.Apply != ApplyPaused {
		t.Fatalf("после неудачи снова пауза, apply = %q", res.Apply)
	}

	r.tp.SetFunc = nil // the restart comes through now
	at(1920)
	if res := r.tick(t, false); res.Apply != ApplyApplied {
		t.Fatalf("восстановление apply = %q", res.Apply)
	}
	st := r.st(t)
	if st.RestartAttempts != (state.Attempts{}) {
		t.Fatalf("счётчик не сброшен: %+v", st.RestartAttempts)
	}
	if st.AppliedRevision != revC {
		t.Fatalf("applied_revision = %q", st.AppliedRevision)
	}
}

// test-labels.sh «пауза: агент неактивен — перезапуск без паузы»,
// «пауза: set-managed руками — перезапуск без паузы» (здесь — sync, manual).
func TestTickNoPause(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		manual bool
		active bool
	}{
		{"агент неактивен", false, false},
		{"manual", true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig()
			r.steady()
			r.store.st.Expected = &portal.Expected{Labels: portal.Labels(labelsC), Revision: revC}
			r.store.st.RestartAttempts = state.Attempts{Count: 5, Last: t0}
			r.tp.Current = teleport.Status{Managed: labelsC, Pending: true, Joined: true, Active: tc.active}
			r.portal.answer304()
			r.clock.now = t0.Add(time.Minute)

			res := r.tick(t, tc.manual)

			if n := len(r.tp.SetCalls()); n != 1 {
				t.Fatalf("set-managed вызван %d раз, want 1 (без паузы)", n)
			}
			if res.Apply != ApplyApplied {
				t.Fatalf("apply = %q", res.Apply)
			}
		})
	}
}

// deferred (the node is not joined): attempts not counted, revision not marked.
func TestTickDeferred(t *testing.T) {
	t.Parallel()
	r := newRig()
	r.tp.Current = teleport.Status{Managed: labelsA}
	r.store.st = &state.State{URL: testURL, RestartAttempts: state.Attempts{Count: 2, Last: t0}}
	r.portal.answer200(labelsB, revB)
	r.clock.now = t0.Add(time.Hour)

	res := r.tick(t, false)

	if res.Apply != ApplyDeferred {
		t.Fatalf("apply = %q", res.Apply)
	}
	st := r.st(t)
	if st.RestartAttempts != (state.Attempts{Count: 2, Last: t0}) {
		t.Fatalf("deferred посчитан: %+v", st.RestartAttempts)
	}
	if st.AppliedRevision != "" {
		t.Fatalf("applied_revision = %q при незарегистрированном узле", st.AppliedRevision)
	}
}

// unchanged resets the counter like applied.
func TestTickUnchangedResetsAttempts(t *testing.T) {
	t.Parallel()
	r := newRig()
	r.steady()
	r.store.st.RestartAttempts = state.Attempts{Count: 1, Last: t0}
	r.tp.Current.Pending = true
	r.tp.SetFunc = func(f *teleportfake.Fake, _ teleport.Labels) (teleport.SetResult, error) {
		f.Current.Pending = false
		return teleport.SetResult{Status: f.Current, Result: teleport.ResultUnchanged}, nil
	}
	r.portal.answer304()

	res := r.tick(t, false)

	if res.Apply != ApplyUnchanged {
		t.Fatalf("apply = %q", res.Apply)
	}
	if got := r.st(t).RestartAttempts; got != (state.Attempts{}) {
		t.Fatalf("restart_attempts = %+v", got)
	}
}

// Cream exit 75/76 and other adapter errors: a failed tick, no attempt counted.
func TestTickSetManagedError(t *testing.T) {
	t.Parallel()
	for _, err := range []error{teleport.ErrLockBusy, teleport.ErrProtocol, teleport.ErrCream} {
		t.Run(err.Error(), func(t *testing.T) {
			t.Parallel()
			r := newRig()
			r.steady()
			r.store.st.RestartAttempts = state.Attempts{Count: 1, Last: t0}
			r.tp.Current.Managed = labelsA
			r.tp.SetErr = fmt.Errorf("cream: %w", err)
			r.portal.answer304()
			r.clock.now = t0.Add(time.Hour)

			res := r.tick(t, false)

			if res.Apply != ApplyError || res.Outcome != OutcomeNotModified {
				t.Fatalf("result = %+v", res)
			}
			if got := r.st(t).RestartAttempts; got != (state.Attempts{Count: 1, Last: t0}) {
				t.Fatalf("restart_attempts = %+v: ошибка cream не считается попыткой", got)
			}
			if !strings.Contains(r.log.String(), "event=apply") {
				t.Fatalf("нет строки event=apply: %s", r.log.String())
			}
		})
	}
}

// managed-status failed: nothing applied, X-Applied-Revision not sent; a 200
// is still remembered.
func TestTickStatusError(t *testing.T) {
	t.Parallel()
	r := newRig()
	r.steady()
	r.tp.StatusErr = errors.New("cream: exit 70")
	r.portal.answer200(labelsC, revC)

	res := r.tick(t, false)

	if res.Apply != ApplyError || res.Outcome != OutcomeOK {
		t.Fatalf("result = %+v", res)
	}
	if req := r.portal.last(t); req.ReportApplied {
		t.Fatal("X-Applied-Revision ушёл при неизвестном состоянии teleport")
	}
	if n := len(r.tp.SetCalls()); n != 0 {
		t.Fatalf("set-managed вызван %d раз при ошибке managed-status", n)
	}
	st := r.st(t)
	if st.Expected.Revision != revC || st.AppliedRevision != revB {
		t.Fatalf("state = %+v / %q", st.Expected, st.AppliedRevision)
	}
}

// managed-status fails only after set-managed: the result of set-managed
// stands, the revision is not marked.
func TestTickStatusErrorAfterSet(t *testing.T) {
	t.Parallel()
	r := newRig()
	r.portal.answer200(labelsB, revB)
	r.tp.SetFunc = func(f *teleportfake.Fake, l teleport.Labels) (teleport.SetResult, error) {
		f.Current.Managed = l
		f.StatusErr = errors.New("cream: exit 70")
		return teleport.SetResult{Status: f.Current, Result: teleport.ResultApplied}, nil
	}

	res := r.tick(t, false)

	if res.Apply != ApplyApplied {
		t.Fatalf("apply = %q", res.Apply)
	}
	if st := r.st(t); st.AppliedRevision != "" {
		t.Fatalf("applied_revision = %q без подтверждения managed-status", st.AppliedRevision)
	}
}

// --- portal failures ---------------------------------------------------------

// test-labels.sh pull: «401/429/500/down — код 0, одна строка, токена нет в
// журнале, config и labels-expected не тронуты, агент не перезапускается»;
// bad_body: «… — отказ, config/labels-expected не тронуты, не перезапускается».
func TestTickPortalFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		outcome portal.Outcome
		status  int
		err     error
	}{
		{portal.OutcomeNetwork, 0, errors.New("dial tcp: connection refused")},
		{portal.OutcomeUnauthorized, 401, nil},
		{portal.OutcomeRateLimited, 429, nil},
		{portal.OutcomeHTTPError, 500, nil},
		{portal.OutcomeRedirect, 302, nil},
		{portal.OutcomeBadBody, 200, portal.ErrBadBody},
	}
	for _, tc := range tests {
		t.Run(string(tc.outcome), func(t *testing.T) {
			t.Parallel()
			r := newRig()
			r.steady()
			r.store.st.Failures = 2
			r.tp.Current.Managed = labelsA // even a damaged node is not touched
			r.portal.resp = portal.Response{Outcome: tc.outcome, HTTPStatus: tc.status, Err: tc.err}

			res := r.tick(t, false)

			want := Result{Outcome: Outcome(tc.outcome), HTTPStatus: tc.status}
			if !reflect.DeepEqual(brief(res), want) {
				t.Fatalf("result = %+v, want %+v", res, want)
			}
			if n := len(r.tp.SetCalls()); n != 0 {
				t.Fatalf("set-managed вызван при %s", tc.outcome)
			}
			if n := lines(r.log.String()); n != 1 {
				t.Fatalf("строк журнала %d, want 1: %s", n, r.log.String())
			}
			st := r.st(t)
			if st.Expected.Revision != revB || teleport.Labels(st.Expected.Labels) != labelsB || st.AppliedRevision != revB {
				t.Fatalf("ожидаемое тронуто: %+v / %q", st.Expected, st.AppliedRevision)
			}
			if st.Failures != 3 {
				t.Fatalf("failures = %d, want 3", st.Failures)
			}
			wantPoll := state.Poll{At: t0, Outcome: string(tc.outcome), HTTPStatus: tc.status}
			if st.LastPoll == nil || *st.LastPoll != wantPoll {
				t.Fatalf("last_poll = %+v, want %+v", st.LastPoll, wantPoll)
			}
		})
	}
}

// last_poll and failures are written after every poll; success resets failures.
func TestTickLastPollOnSuccess(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		answer  func(*fakePortal)
		outcome string
		status  int
	}{
		{"200", func(p *fakePortal) { p.answer200(labelsB, revB) }, "ok", 200},
		{"304", func(p *fakePortal) { p.answer304() }, "not_modified", 304},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig()
			r.steady()
			r.store.st.Failures = 4
			tc.answer(r.portal)

			r.tick(t, false)

			st := r.st(t)
			if st.Failures != 0 {
				t.Fatalf("failures = %d после успеха", st.Failures)
			}
			want := state.Poll{At: t0, Outcome: tc.outcome, HTTPStatus: tc.status}
			if st.LastPoll == nil || *st.LastPoll != want {
				t.Fatalf("last_poll = %+v, want %+v", st.LastPoll, want)
			}
		})
	}
}

// A request that could not be formed (cannot happen with a valid token file):
// nothing is sent, the tick is a network failure.
func TestTickInvalidRequest(t *testing.T) {
	t.Parallel()
	r := newRig()
	r.portal.err = fmt.Errorf("%w: адрес", portal.ErrInvalidRequest)

	res := r.tick(t, false)

	if res.Outcome != OutcomeNetwork || res.Apply != ApplyNone {
		t.Fatalf("result = %+v", res)
	}
	if n := len(r.tp.SetCalls()); n != 0 {
		t.Fatal("set-managed вызван")
	}
}

// test-labels.sh pull: 304 without remembered labels — one line, nothing applied.
func TestTick304WithoutExpected(t *testing.T) {
	t.Parallel()
	r := newRig()
	r.portal.answer304()

	res := r.tick(t, false)

	if res.Outcome != OutcomeNotModified || res.Apply != ApplyNone {
		t.Fatalf("result = %+v", res)
	}
	if n := len(r.tp.SetCalls()); n != 0 {
		t.Fatal("set-managed вызван без ожидаемых меток")
	}
	if !strings.Contains(r.log.String(), "304 без запомненных меток") {
		t.Fatalf("журнал: %s", r.log.String())
	}
	if st := r.st(t); st.LastPoll == nil || st.LastPoll.Outcome != "not_modified" {
		t.Fatalf("last_poll = %+v", st.LastPoll)
	}
}

// --- token and state --------------------------------------------------------

// test-labels.sh pull: «без токена — код 0, запроса нет»; «испорченный файл
// токена — отказ, запроса нет»; «файл токена шире 0600 — отказ, запроса нет».
func TestTickToken(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		err     error
		wantErr bool
	}{
		{"нет файла", fmt.Errorf("x: %w", token.ErrAbsent), false},
		{"испорчен", fmt.Errorf("x: %w", token.ErrCorrupt), true},
		{"права", fmt.Errorf("x: %w", token.ErrBadMode), true},
		{"владелец", fmt.Errorf("x: %w", token.ErrBadOwner), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig()
			r.steady()
			r.tp.Current.Managed = labelsA
			r.tokens.err = tc.err
			before := *r.store.st

			res := r.tick(t, false)

			if res.Outcome != OutcomeDisabled || res.HTTPStatus != 0 || res.Apply != ApplyNone {
				t.Fatalf("result = %+v", res)
			}
			if (res.TokenErr != nil) != tc.wantErr {
				t.Fatalf("TokenErr = %v, want ошибка=%v", res.TokenErr, tc.wantErr)
			}
			if len(r.portal.reqs) != 0 {
				t.Fatal("запрос к порталу без годного токена")
			}
			if n := len(r.tp.SetCalls()) + r.tp.StatusCalls(); n != 0 {
				t.Fatal("teleport вызван без годного токена")
			}
			if r.store.saves != 0 || !reflect.DeepEqual(*r.store.st, before) {
				t.Fatal("state.json тронут без годного токена")
			}
			if n := lines(r.log.String()); n != 1 {
				t.Fatalf("строк журнала %d, want 1", n)
			}
		})
	}
}

// Another portal address: the remembered answer means nothing (spec §5.3).
func TestTickURLChangeResetsState(t *testing.T) {
	t.Parallel()
	r := newRig()
	r.steady()
	r.store.st.URL = otherURL
	r.store.st.RestartAttempts = state.Attempts{Count: 3, Last: t0}
	r.store.st.Failures = 7
	r.portal.answer304()

	r.tick(t, false)

	if req := r.portal.last(t); req.Revision != "" || req.ReportApplied || req.URL != testURL {
		t.Fatalf("запрос = %+v: ревизия чужого портала ушла", req)
	}
	st := r.st(t)
	if st.URL != testURL || st.Expected != nil || st.AppliedRevision != "" ||
		st.RestartAttempts != (state.Attempts{}) || st.Failures != 0 {
		t.Fatalf("состояние не сброшено: %+v", st)
	}
}

// A refused state.json: one journal line, the tick goes on from scratch.
func TestTickInvalidState(t *testing.T) {
	t.Parallel()
	r := newRig()
	r.store.loadErr = fmt.Errorf("state.json: схема не 1: %w", state.ErrInvalid)
	r.portal.answer200(labelsB, revB)

	res := r.tick(t, false)

	if res.Apply != ApplyApplied {
		t.Fatalf("apply = %q", res.Apply)
	}
	if req := r.portal.last(t); req.Revision != "" {
		t.Fatalf("запрос = %+v", req)
	}
	if !strings.Contains(r.log.String(), "схема не 1") {
		t.Fatalf("нет строки про state.json: %s", r.log.String())
	}
}

// A failed save is logged; the tick result stays.
func TestTickSaveError(t *testing.T) {
	t.Parallel()
	r := newRig()
	r.store.saveErr = errors.New("диск полон")
	r.portal.answer200(labelsB, revB)

	res := r.tick(t, false)

	if res.Outcome != OutcomeOK || res.Apply != ApplyApplied {
		t.Fatalf("result = %+v", res)
	}
	if !strings.Contains(r.log.String(), "диск полон") {
		t.Fatalf("ошибка записи не в журнале: %s", r.log.String())
	}
}

// --- actions check ---------------------------------------------------------

// Ruling 7: missing — ticks without applying; unknown — applying goes on.
func TestCheckActions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		err       error
		wantApply string
		wantSets  int
	}{
		{"ok", nil, ApplyApplied, 1},
		{"missing", fmt.Errorf("x: %w", teleport.ErrActionsMissing), ApplyNone, 0},
		{"unknown", errors.New("cream actions: exit 70"), ApplyApplied, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig()
			r.tp.ActionsErr = tc.err
			r.portal.answer200(labelsB, revB)

			r.log.Reset()
			if err := r.tk.CheckActions(context.Background()); !errors.Is(err, tc.err) {
				t.Fatalf("CheckActions = %v, want %v", err, tc.err)
			}
			if tc.err != nil && lines(r.log.String()) != 1 {
				t.Fatalf("журнал CheckActions: %q", r.log.String())
			}
			res := r.tick(t, false)

			if res.Apply != tc.wantApply || len(r.tp.SetCalls()) != tc.wantSets {
				t.Fatalf("apply = %q, set-managed %d раз", res.Apply, len(r.tp.SetCalls()))
			}
			if tc.wantSets == 0 && !strings.Contains(r.log.String(), "reason=actions_missing") {
				t.Fatalf("нет строки пропуска: %s", r.log.String())
			}
		})
	}
}

// --- adapters ----------------------------------------------------------------

// StateFile goes through the real state package: what a tick saves loads back.
func TestStateFileRoundTrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state", "state.json")
	r := newRig()
	r.tk.States = StateFile{FS: atomicfile.OS{}, Path: path}
	r.tp.SetFunc = unconfirmable
	r.portal.answer200(labelsB, revB)

	r.tick(t, false)

	st, err := state.Load(atomicfile.OS{}, path)
	if err != nil {
		t.Fatalf("state.Load: %v", err)
	}
	if st.Expected == nil || st.Expected.Revision != revB || st.RestartAttempts.Count != 1 ||
		st.LastPoll == nil || st.LastPoll.Outcome != "ok" {
		t.Fatalf("state = %+v", st)
	}
}

func TestInEffect(t *testing.T) {
	t.Parallel()
	ok := teleport.Status{Managed: labelsB, Joined: true, Active: true}
	tests := []struct {
		name string
		mod  func(*teleport.Status)
		want bool
	}{
		{"действуют", func(*teleport.Status) {}, true},
		{"другие метки", func(s *teleport.Status) { s.Managed = labelsC }, false},
		{"pending", func(s *teleport.Status) { s.Pending = true }, false},
		{"не зарегистрирован", func(s *teleport.Status) { s.Joined = false }, false},
		{"неактивен", func(s *teleport.Status) { s.Active = false }, false},
	}
	for _, tc := range tests {
		s := ok
		tc.mod(&s)
		if got := InEffect(s, labelsB); got != tc.want {
			t.Errorf("%s: InEffect = %v, want %v", tc.name, got, tc.want)
		}
	}
}
