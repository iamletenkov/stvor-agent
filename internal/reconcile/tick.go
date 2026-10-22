package reconcile

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/iamletenkov/stvor-agent/internal/portal"
	"github.com/iamletenkov/stvor-agent/internal/state"
	"github.com/iamletenkov/stvor-agent/internal/teleport"
	"github.com/iamletenkov/stvor-agent/internal/token"
)

// Restart pause of a service tick (spec §5.3): after PauseAfter unconfirmed
// set-managed restarts in a row, while the Teleport agent is active, at most
// one attempt per PauseFor. A person (manual) and an inactive agent are never
// paused.
const (
	PauseAfter = 3
	PauseFor   = 15 * time.Minute
)

// TokenSource reads the token file.
type TokenSource interface {
	// Read returns the token or an error wrapping token.ErrAbsent (the agent
	// is not enabled) or another token class (the file is unusable).
	Read() (token.Token, error)
}

// StateStore loads and saves state.json.
type StateStore interface {
	// Load returns the state or an error wrapping state.ErrAbsent (no file)
	// or state.ErrInvalid (refused file).
	Load() (state.State, error)
	Save(st state.State) error
}

// Portal polls /robot-api/v1/labels; *portal.Client implements it.
type Portal interface {
	Get(ctx context.Context, r portal.Request) (portal.Response, error)
}

// TokenFile is the TokenSource over the token file.
type TokenFile struct {
	FS   token.FS
	Path string
}

// Read implements TokenSource.
func (f TokenFile) Read() (token.Token, error) { return token.Read(f.FS, f.Path) }

// StateFile is the StateStore over state.json.
type StateFile struct {
	FS   state.FS
	Path string
}

// Load implements StateStore.
func (f StateFile) Load() (state.State, error) { return state.Load(f.FS, f.Path) }

// Save implements StateStore.
func (f StateFile) Save(st state.State) error { return state.Save(f.FS, f.Path, st) }

// Ticker runs ticks. The caller (daemon, cli) holds the agent lock around
// Tick; a Ticker does not run two ticks at once by itself.
type Ticker struct {
	Tokens   TokenSource
	States   StateStore
	Portal   Portal
	Teleport teleport.Teleport
	// Telemetry keeps the telemetry token after every poll the portal
	// accepted the labels token on (200, 304); nil — no telemetry step.
	Telemetry TelemetryStep
	// Now is the clock; nil — time.Now.
	Now func() time.Time
	// Log gets one line per event (event=poll|apply|skip); nil — discarded.
	// Neither the token nor a response body ever reaches it.
	Log *slog.Logger

	// noApply is set by CheckActions when teleport lacks the actions: ticks
	// poll and remember, but never call set-managed.
	noApply atomic.Bool
}

// CheckActions runs the soft start-up check of the teleport actions (spec
// §5.7, ruling 7) and returns its error. teleport.ErrActionsMissing turns off
// applying for the following ticks; any other error is one journal line and
// applying goes on.
func (t *Ticker) CheckActions(ctx context.Context) error {
	err := t.Teleport.CheckActions(ctx)
	switch {
	case err == nil:
		t.noApply.Store(false)
	case errors.Is(err, teleport.ErrActionsMissing):
		t.noApply.Store(true)
		t.log().Error("в расширении teleport нет действий managed-status/set-managed — метки не применяются",
			"event", "skip", "reason", "actions_missing", "err", err)
	default:
		t.log().Warn("список действий teleport не прочитан — применение продолжается",
			"event", "skip", "reason", "actions_unknown", "err", err)
	}
	return err
}

// InEffect reports whether teleport runs with exactly the labels w: written,
// restart confirmed, node joined, agent active (spec §5.3).
func InEffect(s teleport.Status, w teleport.Labels) bool {
	return s.Managed == w && !s.Pending && s.Joined && s.Active
}

// Tick is one pass of spec §5.3: read the token, poll the portal, compare the
// expected labels with teleport, apply, remember. manual (sync by a person)
// is never paused by the 3×/15 min rule. The tick writes last_poll and
// failures to state.json after every poll; it does not suggest a sleep.
//
// stop closed means the service is asked to stop (spec §5.8, TimeoutStopSec):
// the portal poll is cut through its own context, set-managed is not started,
// and the managed-status after it is skipped; a set-managed already running is
// waited for — ctx is never cancelled by a stop. nil — never stops (CLI).
func (t *Ticker) Tick(ctx context.Context, manual bool, stop <-chan struct{}) Result {
	log := t.log()
	tok, err := t.Tokens.Read()
	if errors.Is(err, token.ErrAbsent) {
		log.Info("метки с портала выключены: нет файла токена — stvor-agent enable", "event", "skip", "reason", "disabled")
		return Result{Outcome: OutcomeDisabled}
	}
	if err != nil {
		log.Error("файл токена не годен — токен не отправлен; выполните stvor-agent enable заново",
			"event", "skip", "reason", "token", "token", token.Kind(err), "err", err)
		return Result{Outcome: OutcomeDisabled, TokenErr: err}
	}

	st, err := t.States.Load()
	switch {
	case errors.Is(err, state.ErrAbsent):
		st = state.New(tok.URL)
	case err != nil:
		log.Warn("state.json не принят — начинаю с пустого состояния", "event", "poll", "err", err)
		st = state.New(tok.URL)
	}
	if st.URL != tok.URL {
		log.Info("токен выпущен для другого портала — прежнее состояние сброшено", "event", "poll", "url", tok.URL)
		st = state.New(tok.URL)
	}

	ts, tsErr := t.Teleport.Status(ctx)
	req := portal.Request{URL: tok.URL, Token: tok.Token}
	if st.Expected != nil {
		req.Revision = st.Expected.Revision
		// X-Applied-Revision: only the same revision, only when it is
		// already marked applied and its labels are seen in effect now.
		req.ReportApplied = st.AppliedRevision == st.Expected.Revision &&
			tsErr == nil && InEffect(ts, want(st.Expected))
	}

	resp, err := t.poll(ctx, req, stop)
	if stopped(stop) && err == nil && resp.Outcome == portal.OutcomeNetwork {
		// Cut by the stop, not a portal failure: neither counted nor saved.
		log.Info("остановка службы — опрос портала прерван", "event", "skip", "reason", "stopping")
		return Result{Outcome: OutcomeNetwork, Expected: st.Expected}
	}
	if err != nil {
		// Nothing was sent: the token file and state passed their checks,
		// so this is a broken invariant; count it as a network failure.
		resp = portal.Response{Outcome: portal.OutcomeNetwork, Err: err}
	}
	if t.Telemetry != nil && !stopped(stop) &&
		(resp.Outcome == portal.OutcomeOK || resp.Outcome == portal.OutcomeNotModified) {
		// The labels token was just accepted: the moment to ask for the
		// telemetry token with it (Stvor plan R4).
		t.Telemetry.Ensure(ctx, tok)
	}
	now := t.now()
	res := Result{Outcome: Outcome(resp.Outcome), HTTPStatus: resp.HTTPStatus}
	st.LastPoll = &state.Poll{At: now, Outcome: string(res.Outcome), HTTPStatus: res.HTTPStatus}

	switch resp.Outcome {
	case portal.OutcomeOK:
		st.Failures = 0
		changed := st.Expected == nil || *st.Expected != resp.Expected
		exp := resp.Expected
		st.Expected = &exp
		lvl := slog.LevelDebug
		if changed {
			lvl = slog.LevelInfo
		}
		log.Log(ctx, lvl, "портал: ожидаемые метки", "event", "poll", "outcome", res.Outcome,
			"status", res.HTTPStatus, "rev", exp.Revision, "env", exp.Env, "space", exp.Space)
		// Remember the answer before touching teleport (spec §5.3).
		t.save(st)
	case portal.OutcomeNotModified:
		st.Failures = 0
		if st.Expected == nil {
			log.Warn("портал ответил 304 без запомненных меток — повтор на следующем тике",
				"event", "poll", "outcome", res.Outcome, "status", res.HTTPStatus)
			t.save(st)
			return res
		}
		log.Debug("портал: метки без изменений", "event", "poll", "outcome", res.Outcome,
			"status", res.HTTPStatus, "rev", st.Expected.Revision)
	default:
		st.Failures++
		t.logPollFailure(resp)
		t.save(st)
		return res
	}

	w := want(st.Expected)
	rev := st.Expected.Revision
	exp := *st.Expected
	res.Expected = &exp
	if tsErr != nil {
		log.Error("состояние teleport неизвестно (managed-status) — метки не тронуты",
			"event", "apply", "result", ApplyError, "err", tsErr)
		res.Apply = ApplyError
		res.StatusErr = tsErr
		t.save(st)
		return res
	}
	res.Teleport = ts

	if !InEffect(ts, w) {
		if t.noApply.Load() {
			log.Warn("метки расходятся с порталом, но действия teleport недоступны — не применяю",
				"event", "skip", "reason", "actions_missing", "env", w.Env, "space", w.Space)
			t.save(st)
			return res
		}
		a := st.RestartAttempts
		if next := a.Last.Add(PauseFor); !manual && a.Count >= PauseAfter && ts.Active && now.Before(next) {
			log.Warn("перезапуск пропущен: "+strconv.Itoa(a.Count)+" неподтверждённых подряд, агент активен",
				"event", "skip", "reason", "paused", "attempts", a.Count, "next_in", next.Sub(now))
			res.Apply = ApplyPaused
			t.save(st)
			return res
		}
		if stopped(stop) {
			log.Info("остановка службы — set-managed не начат, метки применятся после старта",
				"event", "skip", "reason", "stopping", "env", w.Env, "space", w.Space, "rev", rev)
			res.Apply = ApplyStopped
			t.save(st)
			return res
		}
		r, err := t.Teleport.SetManaged(ctx, w)
		if err != nil {
			// Cream exit 75/76, a timeout, a broken answer: a failed tick,
			// not a restart attempt.
			log.Error("set-managed не выполнен", "event", "apply", "result", ApplyError,
				"env", w.Env, "space", w.Space, "err", err)
			res.Apply = ApplyError
			t.save(st)
			return res
		}
		res.Apply = r.Result
		switch r.Result {
		case teleport.ResultApplied, teleport.ResultUnchanged:
			st.RestartAttempts = state.Attempts{}
			log.Info("метки портала записаны в teleport", "event", "apply", "result", r.Result,
				"env", w.Env, "space", w.Space, "rev", rev)
		case teleport.ResultDeferred:
			log.Info("узел не зарегистрирован — метки применятся после join", "event", "apply",
				"result", r.Result, "env", w.Env, "space", w.Space, "rev", rev)
		case teleport.ResultUnconfirmed:
			st.RestartAttempts = state.Attempts{Count: a.Count + 1, Last: now}
			log.Error("агент teleport не подтвердил перезапуск с новыми метками", "event", "apply",
				"result", r.Result, "attempt", a.Count+1, "env", w.Env, "space", w.Space, "rev", rev)
		}
		if stopped(stop) {
			// Keep within the stop budget: the next tick after start checks.
			log.Info("остановка службы — состояние после set-managed проверит следующий тик",
				"event", "apply", "result", r.Result, "rev", rev)
			res.Teleport = r.Status
			t.save(st)
			return res
		}
		ts, tsErr = t.Teleport.Status(ctx)
		if tsErr != nil {
			log.Error("состояние teleport после set-managed неизвестно — ревизия не отмечена",
				"event", "apply", "result", r.Result, "err", tsErr)
			res.StatusErr = tsErr
			t.save(st)
			return res
		}
		res.Teleport = ts
	}

	res.InEffect = InEffect(ts, w)
	if res.InEffect && st.AppliedRevision != rev {
		st.AppliedRevision = rev
		log.Info("метки портала действуют — портал узнает на следующем тике", "event", "apply",
			"rev", rev, "env", w.Env, "space", w.Space)
	}
	t.save(st)
	return res
}

// poll asks the portal; a stop cancels the poll's own context (only it).
func (t *Ticker) poll(ctx context.Context, req portal.Request, stop <-chan struct{}) (portal.Response, error) {
	if stop == nil {
		return t.Portal.Get(ctx, req)
	}
	pctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-stop:
			cancel()
		case <-pctx.Done():
		}
	}()
	return t.Portal.Get(pctx, req)
}

// stopped reports whether stop is closed; a nil stop never is.
func stopped(stop <-chan struct{}) bool {
	select {
	case <-stop:
		return true
	default:
		return false
	}
}

// logPollFailure writes the one journal line of a failed poll. resp.Err never
// carries the token or the body (portal guarantees it).
func (t *Ticker) logPollFailure(resp portal.Response) {
	var msg string
	switch resp.Outcome {
	case portal.OutcomeUnauthorized:
		msg = "портал не принял токен (401) — выпустите токен на портале и выполните stvor-agent enable"
	case portal.OutcomeRateLimited:
		msg = "портал ограничил частоту запросов (429) — повтор на следующем тике"
	case portal.OutcomeRedirect:
		msg = "портал ответил перенаправлением — не следую, повтор на следующем тике"
	case portal.OutcomeBadBody:
		msg = "ответ портала не по контракту /robot-api/v1/labels — метки не тронуты"
	case portal.OutcomeNetwork:
		msg = "портал недоступен — повтор на следующем тике"
	default:
		msg = "портал ответил ошибкой — повтор на следующем тике"
	}
	attrs := []any{"event", "poll", "outcome", string(resp.Outcome), "status", resp.HTTPStatus}
	if resp.Err != nil {
		attrs = append(attrs, "err", resp.Err)
	}
	t.log().Warn(msg, attrs...)
}

func (t *Ticker) save(st state.State) {
	if err := t.States.Save(st); err != nil {
		t.log().Error("state.json не записан", "event", "poll", "err", err)
	}
}

func (t *Ticker) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

func (t *Ticker) log() *slog.Logger {
	if t.Log != nil {
		return t.Log
	}
	return slog.New(slog.DiscardHandler)
}

func want(e *portal.Expected) teleport.Labels { return teleport.Labels(e.Labels) }
