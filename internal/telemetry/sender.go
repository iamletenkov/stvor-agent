package telemetry

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"

	"github.com/iamletenkov/stvor-agent/internal/buffer"
)

// Queue is the buffer side the sender drives (internal/buffer.Buffer).
type Queue interface {
	Next() (*buffer.Chunk, error)
	Ack(c *buffer.Chunk) error
	Split(c *buffer.Chunk) (*buffer.Chunk, *buffer.Chunk, error)
	Stats() buffer.Stats
	AddEvent(e buffer.Event) error
	SetLimits(quotaBytes int64, autonomy time.Duration) error
}

// Meta is what a batch says about the agent and the machine.
type Meta struct {
	AgentVersion  string
	MachineIDHash string   // sha256 hex of /etc/machine-id (clone detector)
	BootID        string   // the current boot (inventory and events); "" — not known yet
	Capabilities  []string // what the collectors found; unknown ones are dropped
	FailedUnits   []string // failed systemd units, sent with fresh batches only
}

// Options configure a Sender; Tokens, Queue and Meta are required.
type Options struct {
	Client ClientConfig
	Tokens TokenSource
	Queue  Queue
	Meta   func() Meta
	Log    *slog.Logger        // nil — discard
	Now    func() time.Time    // nil — time.Now
	MonoMS func() int64        // CLOCK_BOOTTIME, ms; nil — 0
	Int64N func(n int64) int64 // [0, n) for the full jitter; nil — math/rand/v2
	// Sleep waits d or until ctx is done (MinRequestGap); nil — a timer.
	Sleep func(ctx context.Context, d time.Duration) error
}

// Outcome is how one Step or inventory sync ended.
type Outcome string

// Outcomes.
const (
	OutcomeIdle          Outcome = "idle"           // nothing to send
	OutcomeSent          Outcome = "sent"           // 200/202 accepted
	OutcomeDuplicate     Outcome = "duplicate"      // 200 duplicate: the portal had it
	OutcomeMatch         Outcome = "match"          // inventory hash known to the portal
	OutcomeSplit         Outcome = "split"          // 413 or over a limit: halves queued
	OutcomeDropped       Outcome = "dropped"        // dropped: 400 or 409 tail_rejected from the portal, or locally (over a limit, nothing valid)
	OutcomeRejected      Outcome = "rejected"       // 422, 400 past the fuse: kept in the buffer, retried after a pause
	OutcomeRetry         Outcome = "retry"          // network, 429, 5xx, 3xx: wait and repeat
	OutcomeUnauthorized  Outcome = "unauthorized"   // 401: wait for a new token
	OutcomeCloneRejected Outcome = "clone_rejected" // 409 clone_rejected: pause 1 h
)

// Result is one Step: Wait is the pause before the next Step.
type Result struct {
	Outcome    Outcome
	HTTPStatus int
	Wait       time.Duration
	Err        error // for the journal; never carries the token
}

// Pauses.
const (
	ClonePause = time.Hour
	// RejectPause is the pause after RejectsInRow refusals (422) in a row:
	// the data stays in the buffer — a portal bug must not empty it.
	RejectPause  = time.Hour
	RejectsInRow = 5
	// BadRequestsInRow is the fuse over 400 (controller ruling, Stvor plan
	// Task 21): a 400 drops its batch (contract v1), but this many 400 in a row
	// on different batches without a 2xx between them is a portal failure, not
	// bad batches — from the BadRequestsInRow-th on the data stays in the
	// buffer behind a backoff pause, so a broken portal cannot empty it. The
	// first 2xx resets the count; the inventory shares it.
	BadRequestsInRow = 3
	// TokenWait is the pause while the token is missing or rejected: `run`
	// writes a new one on its next tick (≤ 60 s + jitter).
	TokenWait     = 60 * time.Second
	maxRetryAfter = time.Hour
	// MinRequestGap is the least time between two requests to the portal of
	// any kind — fresh batches, backlog, events, inventory: at most 20 a
	// minute, the Traefik limit per robot (robot-telemetry-rate). The portal's
	// next_after_ms is waited on top of it.
	MinRequestGap = 3 * time.Second
)

// reply_codes of contract v1: the outcome of an answer comes from its "code"
// only; "error" (telemetry.<code>) and "message" are for people and journals.
const (
	codeDuplicate = "duplicate"
	codeNeedBody  = "need_body"
	codeClone     = "clone_rejected"
	codeTail      = "tail_rejected"
)

// Sender sends the buffer to the portal one request at a time. It is safe
// for concurrent use: requests are serialized by send (contract
// retry.in_flight = 1); Config and RTT take only mu and never wait for a
// request in flight.
type Sender struct {
	send sync.Mutex // held for a whole Step or SyncInventory
	opt  Options
	k    *Contract
	http *client
	// Under send only.
	failures   int
	rejects    int       // 422 in a row
	badReqs    int       // 400 in a row without a 2xx between (BadRequestsInRow)
	lastReq    time.Time // the last request to the portal (MinRequestGap)
	cloneUntil time.Time
	rejected   [32]byte // sha256 of the token the portal refused
	hasRejects bool
	gz         gzipper

	mu  sync.RWMutex // cfg and rtt: written under send and mu, read under mu
	cfg Config
	rtt time.Duration
}

// NewSender builds a sender over the compiled-in contract with its default
// configuration.
func NewSender(o Options) (*Sender, error) {
	if o.Tokens == nil || o.Queue == nil || o.Meta == nil {
		return nil, errors.New("телеметрия: нужны Tokens, Queue и Meta")
	}
	k, err := ContractV1()
	if err != nil {
		return nil, err
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.MonoMS == nil {
		o.MonoMS = func() int64 { return 0 }
	}
	if o.Int64N == nil {
		o.Int64N = rand.Int64N
	}
	if o.Sleep == nil {
		o.Sleep = sleep
	}
	return &Sender{opt: o, k: k, http: newClient(o.Client), cfg: k.DefaultConfig()}, nil
}

// Config is the configuration in effect.
func (s *Sender) Config() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// RTT is the answer time of the last accepted request (stvor.portal.rtt).
func (s *Sender) RTT() time.Duration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.rtt
}

// Step sends the next chunk of the buffer once: fresh points → events →
// backlog, the order of the buffer. A retry of the same chunk keeps its
// identity (stream, seq, batch_id) and gets a new sent_at.
func (s *Sender) Step(ctx context.Context) Result {
	s.send.Lock()
	defer s.send.Unlock()
	now := s.opt.Now()
	cr, res, ok := s.credentials(now)
	if !ok {
		return res
	}
	c, err := s.opt.Queue.Next()
	if err != nil {
		return s.backoff(0, fmt.Errorf("буфер: %w", err))
	}
	if c == nil {
		return Result{Outcome: OutcomeIdle}
	}
	if err := s.pace(ctx); err != nil {
		return Result{Outcome: OutcomeRetry, Err: err}
	}
	now = s.opt.Now() // sent_at is the moment of this attempt
	var (
		path string
		raw  []byte
		over string
	)
	if c.Kind == buffer.ChunkEvents {
		path = "/events"
		raw, over, err = s.eventsBody(c)
	} else {
		path = "/metrics"
		raw, over, err = s.metricsBody(c, now)
	}
	if err == nil && raw == nil && over == "" {
		err = errors.New("ни одного годного события")
	}
	if err != nil {
		// Local: the same chunk would fail the same way on every retry.
		s.opt.Log.Warn("пачка не собрана — отброшена", "event", "telemetry.dropped", "err", err)
		if aerr := s.opt.Queue.Ack(c); aerr != nil {
			return s.backoff(0, fmt.Errorf("подтверждение в буфере: %w", aerr))
		}
		return Result{Outcome: OutcomeDropped, Err: err}
	}
	var gz []byte
	if over == "" {
		if gz, err = s.gz.gzip(raw); err != nil {
			return s.backoff(0, err)
		}
		if len(gz) > s.k.Limits.BodyGzipMax {
			over = fmt.Sprintf("gzip %d байт", len(gz))
		}
	}
	if over != "" {
		s.opt.Log.Info("пачка больше лимита контракта — делится", "event", "telemetry.split", "reason", over)
		return s.split(c, 0)
	}
	start := s.opt.MonoMS()
	a := s.request(ctx, http.MethodPost, cr, path, gz, now)
	return s.settle(a, cr, start, now, chunkHandler{s: s, c: c})
}

// pace waits until MinRequestGap has passed since the last request.
func (s *Sender) pace(ctx context.Context) error {
	if s.lastReq.IsZero() {
		return nil
	}
	if wait := MinRequestGap - s.opt.Now().Sub(s.lastReq); wait > 0 {
		return s.opt.Sleep(ctx, wait)
	}
	return nil
}

// request makes one request to the portal and notes its time for pace.
func (s *Sender) request(ctx context.Context, method string, cr Credentials, path string, gz []byte, now time.Time) answer {
	a := s.http.do(ctx, method, cr, path, gz, now)
	s.lastReq = s.opt.Now()
	return a
}

// sleep is the default Options.Sleep.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// handler is what an accepted or too-large answer does to the request.
type handler interface {
	ack() error
	tooLarge(status int) Result
	backlog() bool
}

type chunkHandler struct {
	s *Sender
	c *buffer.Chunk
}

func (h chunkHandler) ack() error                 { return h.s.opt.Queue.Ack(h.c) }
func (h chunkHandler) tooLarge(status int) Result { return h.s.split(h.c, status) }
func (h chunkHandler) backlog() bool              { return h.c.Backfill }

// credentials reads the token; a token the portal already refused is not
// sent again — the service waits for `run` to rotate it.
func (s *Sender) credentials(now time.Time) (Credentials, Result, bool) {
	if now.Before(s.cloneUntil) {
		return Credentials{}, Result{Outcome: OutcomeCloneRejected, Wait: s.cloneUntil.Sub(now)}, false
	}
	cr, err := s.opt.Tokens.Credentials()
	if err == nil {
		err = cr.validate()
	}
	if err != nil {
		return Credentials{}, Result{Outcome: OutcomeUnauthorized, Wait: TokenWait, Err: err}, false
	}
	if s.hasRejects && sha256.Sum256([]byte(cr.Token)) == s.rejected {
		return Credentials{}, Result{Outcome: OutcomeUnauthorized, Wait: TokenWait}, false
	}
	return cr, Result{}, true
}

// settle classifies an answer.
func (s *Sender) settle(a answer, cr Credentials, start int64, now time.Time, h handler) Result {
	switch a.status {
	case 0:
		return s.backoff(0, a.err)
	case http.StatusOK, http.StatusAccepted:
		if err := h.ack(); err != nil {
			return s.backoff(a.status, fmt.Errorf("подтверждение в буфере: %w", err))
		}
		s.failures, s.rejects, s.badReqs = 0, 0, 0
		s.mu.Lock()
		s.rtt = time.Duration(max(s.opt.MonoMS()-start, 0)) * time.Millisecond
		s.mu.Unlock()
		status, nextAfter := s.accept(a, now)
		res := Result{Outcome: OutcomeSent, HTTPStatus: a.status}
		switch {
		case replyCode(a.body) == codeDuplicate:
			res.Outcome = OutcomeDuplicate
		case status == "match": // inventory: 200 {"status":"match"} has no reply code
			res.Outcome = OutcomeMatch
		}
		if a.status == http.StatusAccepted || h.backlog() {
			res.Wait = nextAfter
		}
		return res
	case http.StatusRequestEntityTooLarge:
		return h.tooLarge(a.status)
	case http.StatusUnauthorized:
		return s.unauthorized(cr)
	case http.StatusConflict:
		switch {
		case replyCode(a.body) == codeClone:
			return s.clone(now)
		case replyCode(a.body) == codeTail:
			// The portal will never take this batch (the tail of an earlier
			// boot it cannot place in time): acked so that the head of the
			// buffer does not hold back the fresh data; not a portal failure,
			// so not in the 400 fuse.
			s.opt.Log.Warn("портал не примет хвост прежней загрузки — пачка удалена из буфера",
				"event", "telemetry.tail_rejected", "status", a.status)
			if err := h.ack(); err != nil {
				return s.backoff(a.status, fmt.Errorf("подтверждение в буфере: %w", err))
			}
			return Result{Outcome: OutcomeDropped, HTTPStatus: a.status, Err: errors.New("портал отверг хвост (409 tail_rejected)")}
		}
		return s.backoff(a.status, errors.New("портал ответил 409"))
	case http.StatusTooManyRequests, http.StatusServiceUnavailable:
		if a.hasRetry {
			s.failures++
			// Retry-After: 0 or a date in the past is no reason to hammer.
			wait := max(a.retryAfter, time.Duration(s.k.BackoffMin)*time.Second)
			return Result{Outcome: OutcomeRetry, HTTPStatus: a.status, Wait: min(wait, maxRetryAfter)}
		}
		return s.backoff(a.status, fmt.Errorf("портал ответил %d без Retry-After", a.status))
	case http.StatusBadRequest:
		// Contract v1 statuses: a 400 with a code of the contract
		// (telemetry.malformed | invalid | schema_unsupported |
		// inventory_hash_mismatch) — the same bytes never pass: acked and
		// logged, never retried. A 400 without such a code comes from a proxy
		// in front of the portal: the data stays, as on 422. Every 400 counts
		// toward the fuse (BadRequestsInRow).
		s.badReqs++
		code := errorCode(a.body)
		qcode := quoteCut(code)
		switch {
		case s.badReqs >= BadRequestsInRow:
			res := s.backoff(a.status, fmt.Errorf("портал отвергает пачки подряд (400 %s)", qcode))
			res.Outcome = OutcomeRejected
			s.opt.Log.Warn("портал отвергает пачки подряд — считаем это сбоем портала, данные остаются в буфере",
				"event", "telemetry.portal_rejecting", "status", a.status, "code", qcode,
				"in_row", s.badReqs, "wait", res.Wait)
			return res
		case !s.k.DropCodes[code]:
			res := s.backoff(a.status, fmt.Errorf("400 без кода контракта (%s)", qcode))
			res.Outcome = OutcomeRejected
			s.opt.Log.Warn("400 без кода контракта — похоже на прокси, данные остаются в буфере",
				"event", "telemetry.rejected", "status", a.status, "code", qcode,
				"in_row", s.badReqs, "wait", res.Wait)
			return res
		}
		s.opt.Log.Warn("портал отверг пачку как неисправимую — удалена из буфера", "event", "telemetry.dropped",
			"status", a.status, "code", qcode, "in_row", s.badReqs)
		if err := h.ack(); err != nil {
			return s.backoff(a.status, fmt.Errorf("подтверждение в буфере: %w", err))
		}
		return Result{Outcome: OutcomeDropped, HTTPStatus: a.status,
			Err: fmt.Errorf("портал отверг пачку (400 %s)", qcode)}
	case http.StatusUnprocessableEntity:
		// Not in contract v1: never acked — a portal that refuses everything
		// (a bug) must not empty the buffer. Backoff, then a long pause.
		s.rejects++
		res := s.backoff(a.status, fmt.Errorf("портал отверг запрос (%d)", a.status))
		res.Outcome = OutcomeRejected
		if s.rejects >= RejectsInRow {
			res.Wait = RejectPause
		}
		s.opt.Log.Warn("портал отверг запрос — данные остаются в буфере, повтор после паузы",
			"event", "telemetry.rejected", "status", a.status, "code", quoteCut(errorCode(a.body)),
			"in_row", s.rejects, "wait", res.Wait)
		return res
	}
	return s.backoff(a.status, fmt.Errorf("портал ответил %d", a.status))
}

// accept applies the configuration of an accepted answer.
func (s *Sender) accept(a answer, now time.Time) (status string, nextAfter time.Duration) {
	w, err := parseConfig(a.body)
	if err != nil || w == nil {
		if err != nil {
			s.opt.Log.Warn("конфигурация в ответе не разобрана — действует прежняя", "event", "telemetry.config", "err", err)
		}
		return "", 0
	}
	ap := s.k.apply(s.cfg, *w, now)
	for _, f := range ap.ignored {
		s.opt.Log.Warn("значение конфигурации вне диапазона контракта — пропущено", "event", "telemetry.config", "field", f)
	}
	if ap.cfg.BufferQuota != s.cfg.BufferQuota || ap.cfg.Autonomy != s.cfg.Autonomy {
		if err := s.opt.Queue.SetLimits(ap.cfg.BufferQuota, ap.cfg.Autonomy); err != nil {
			s.opt.Log.Warn("квота буфера не применена", "event", "telemetry.config", "err", err)
			ap.cfg.BufferQuota, ap.cfg.Autonomy = s.cfg.BufferQuota, s.cfg.Autonomy
		}
	}
	s.mu.Lock()
	s.cfg = ap.cfg
	s.mu.Unlock()
	return w.Status, ap.nextAfter
}

// unauthorized re-reads the token: a rotated one is tried at once, the same
// one is reported as rejected and not sent again.
func (s *Sender) unauthorized(used Credentials) Result {
	res := Result{Outcome: OutcomeUnauthorized, HTTPStatus: http.StatusUnauthorized, Wait: TokenWait}
	if cr, err := s.opt.Tokens.Credentials(); err == nil && cr.validate() == nil && cr.Token != used.Token {
		res.Wait = 0
		return res
	}
	s.opt.Log.Warn("портал не принял токен телеметрии — ждём новый", "event", "telemetry.unauthorized")
	if err := s.opt.Tokens.Rejected(); err != nil {
		// Without the marker `run` never asks for a new token: the token is
		// not marked as refused, so the next Step tries (and marks) again.
		res.Err = fmt.Errorf("маркер отказа токена: %w", err)
		return res
	}
	s.rejected, s.hasRejects = sha256.Sum256([]byte(used.Token)), true
	return res
}

// clone pauses sending for an hour and records the event.
func (s *Sender) clone(now time.Time) Result {
	s.cloneUntil = now.Add(ClonePause)
	s.opt.Log.Warn("портал считает машину клоном — пауза", "event", "telemetry.clone_rejected", "until", s.cloneUntil)
	err := s.opt.Queue.AddEvent(buffer.Event{
		Kind: "portal.clone_rejected", Severity: "danger", At: now,
		Params: map[string]any{"until": rfc3339(s.cloneUntil)},
	})
	return Result{Outcome: OutcomeCloneRejected, HTTPStatus: http.StatusConflict, Wait: ClonePause, Err: err}
}

// split answers a 413 (or a body over a limit): the chunk is replaced by
// its halves; one record that does not fit is dropped.
func (s *Sender) split(c *buffer.Chunk, status int) Result {
	_, _, err := s.opt.Queue.Split(c)
	switch {
	case errors.Is(err, buffer.ErrUnsplittable):
		s.opt.Log.Warn("одна запись больше лимита — отброшена", "event", "telemetry.dropped", "status", status)
		if err := s.opt.Queue.Ack(c); err != nil {
			return s.backoff(status, fmt.Errorf("подтверждение в буфере: %w", err))
		}
		return Result{Outcome: OutcomeDropped, HTTPStatus: status}
	case err != nil:
		return s.backoff(status, fmt.Errorf("деление пачки: %w", err))
	}
	return Result{Outcome: OutcomeSplit, HTTPStatus: status}
}

// backoff is the exponential pause with full jitter: U[0, min(max, min·2^n)).
func (s *Sender) backoff(status int, err error) Result {
	s.failures++
	base := time.Duration(s.k.BackoffMin) * time.Second
	ceil := min(base<<min(s.failures-1, 20), s.maxBackoff())
	s.opt.Log.Debug("повтор отправки телеметрии", "event", "telemetry.retry", "status", status,
		"failures", s.failures, "err", err)
	return Result{Outcome: OutcomeRetry, HTTPStatus: status, Wait: time.Duration(s.opt.Int64N(int64(ceil))), Err: err}
}

func (s *Sender) maxBackoff() time.Duration { return time.Duration(s.k.BackoffMax) * time.Second }

// replyCode is the "code" of an answer body (contract v1 reply_codes).
func replyCode(body []byte) string {
	var e struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(body, &e) != nil {
		return ""
	}
	return e.Code
}

// errorCode reads {"code": …} or {"error": …} or {"status": …} of an error body.
func errorCode(body []byte) string {
	var e struct {
		Code   string `json:"code"`
		Error  string `json:"error"`
		Status string `json:"status"`
	}
	if json.Unmarshal(body, &e) != nil {
		return ""
	}
	switch {
	case e.Code != "":
		return e.Code
	case e.Error != "":
		return e.Error
	}
	return e.Status
}

// rfc3339 is the contract time: UTC with milliseconds.
func rfc3339(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z07:00") }
