package daemon

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"slices"
	"sync"
	"time"

	"github.com/iamletenkov/stvor-agent/internal/buffer"
	"github.com/iamletenkov/stvor-agent/internal/collect"
	"github.com/iamletenkov/stvor-agent/internal/inventory"
	"github.com/iamletenkov/stvor-agent/internal/telemetry"
)

// The loop of `stvor-agent telemetry` (Stvor spec "robot telemetry" §6, plan
// Task 18): collectors by the contract groups (fast 10 s, slow 60 s; fast 5 s
// in the detail mode), the inventory by its hash, agent events, the buffer
// and one request to the portal at a time. One goroutine: a send blocks the
// next sample for at most one request (the client's timeouts), and the
// sender backs off exponentially while the portal is away.

// Send cadence: a batch about every SendInterval with ±SendJitter.
const (
	SendInterval = 60 * time.Second
	SendJitter   = 6 * time.Second
	// firstSend is the upper bound of the random delay of the first send.
	firstSend = 10 * time.Second
)

// TelemetryCollector reads the machine; perCore comes from the portal config.
type TelemetryCollector interface {
	Fast(ctx context.Context, perCore bool, s *collect.Snapshot) error
	Slow(ctx context.Context, s *collect.Snapshot) error
}

// TelemetryInventory reads the inventory; inventory.Source implements it.
type TelemetryInventory interface {
	Collect() (inventory.Body, error)
}

// TelemetrySender is the client; *telemetry.Sender implements it.
type TelemetrySender interface {
	Step(ctx context.Context) telemetry.Result
	SyncInventory(ctx context.Context, body inventory.Body) telemetry.Result
	Config() telemetry.Config
}

// TelemetryBuffer is the buffer; *buffer.Buffer implements it.
type TelemetryBuffer interface {
	Add(r buffer.Record) error
	AddEvent(e buffer.Event) error
	Stats() buffer.Stats
	Close() error
}

// TelemetryReport is the state of the loop for `status --json`.
type TelemetryReport struct {
	At            time.Time
	Outcome       telemetry.Outcome
	HTTPStatus    int
	Buffer        buffer.Stats
	ConfigHash    string
	DetailUntil   time.Time
	InventoryHash string
}

// TelemetryLoop is the telemetry service. Collector, Inventory, Sender and
// Buffer are required; zero optional fields take real defaults.
type TelemetryLoop struct {
	Collector TelemetryCollector
	Inventory TelemetryInventory
	Sender    TelemetrySender
	Buffer    TelemetryBuffer
	// Now is the wall clock, MonoMS CLOCK_BOOTTIME in ms.
	Now    func() time.Time
	MonoMS func() int64
	// After is the sleep; nil — time.After.
	After func(time.Duration) <-chan time.Time
	// Rand draws from [0, n); nil — math/rand/v2.
	Rand func(n int64) int64
	// LastBoot and SaveBoot keep the boot_id of the previous start (event
	// system.boot); nil — no such event.
	LastBoot func() string
	SaveBoot func(id string) error
	// Report gets the state after every send and inventory step; nil — none.
	Report func(TelemetryReport)
	Log    *slog.Logger

	mu       sync.Mutex
	fastCaps []string
	slowCaps []string
	failed   []string
	bootID   string
	synced   bool
	units    map[string]bool // failed units seen by the last slow tick; nil — none yet
	invHash  string
	// invRejected is the hash the portal dropped, held back until
	// invRejectedUntil (InventoryRejectPause).
	invRejected      string
	invRejectedUntil time.Time
	lastSend         telemetry.Result
	lastSendT        time.Time
}

// BootID is the current boot_id ("" before the first slow tick): the
// sender identity of the inventory and the events.
func (l *TelemetryLoop) BootID() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.bootID
}

// Meta is what the batches say about the machine; the caller passes it to
// the sender (telemetry.Options.Meta) with the version and machine-id hash.
func (l *TelemetryLoop) Meta() (capabilities, failedUnits []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	caps := slices.Clone(l.fastCaps)
	for _, c := range l.slowCaps {
		if !slices.Contains(caps, c) {
			caps = append(caps, c)
		}
	}
	return caps, slices.Clone(l.failed)
}

// Run collects and sends until ctx is done, then flushes the buffer to disk
// (graceful stop: nothing collected is lost) and returns its error.
func (l *TelemetryLoop) Run(ctx context.Context) error {
	log := l.log()
	now := l.now()
	nextCollect, nextSlow, nextInv := now, now, now
	nextSend := now.Add(time.Duration(l.rand(int64(firstSend))))
	// idle: nextSend is the agent's own idle pause, which the detail mode
	// may shorten; a pause asked by the portal (Retry-After, next_after) or
	// set by a failure (backoff, the reject pause) is never shortened.
	// paused: the sender waits out such a failure pause until nextSend.
	idle, paused := true, false
	for {
		cfg := l.Sender.Config()
		if now = l.now(); !now.Before(nextCollect) {
			slow := !now.Before(nextSlow)
			l.collect(ctx, now, cfg.PerCore, slow)
			if slow {
				nextSlow = now.Add(cfg.SlowInterval)
			}
			fast := cfg.FastInterval
			if cfg.Detail(now) {
				fast = cfg.DetailSample
			}
			nextCollect = now.Add(fast)
		}
		if now = l.now(); !now.Before(nextInv) {
			nextInv = now.Add(l.Sender.Config().InventoryCheck)
			if paused && now.Before(nextSend) {
				// The portal asked to be left alone: the inventory waits too.
				nextInv = later(nextInv, nextSend)
			} else {
				l.inventory(ctx)
				nextInv = l.now().Add(l.Sender.Config().InventoryCheck)
			}
		}
		if now = l.now(); !now.Before(nextSend) {
			var wait time.Duration
			wait, idle, paused = l.send(ctx)
			nextSend = l.now().Add(wait)
		} else if cfg := l.Sender.Config(); idle && cfg.Detail(now) && nextSend.Sub(now) > cfg.DetailSend {
			// The detail mode came on: no need to wait out the idle minute.
			nextSend = now.Add(cfg.DetailSend)
		}
		next := nextCollect
		for _, t := range []time.Time{nextInv, nextSend} {
			if t.Before(next) {
				next = t
			}
		}
		wait := max(next.Sub(l.now()), 0)
		select {
		case <-ctx.Done():
		case <-l.after(wait):
		}
		if ctx.Err() != nil {
			err := l.Buffer.Close()
			if err != nil {
				log.Error("буфер телеметрии не сброшен на диск", "event", "stop", "err", err)
			}
			log.Info("остановка службы телеметрии", "event", "stop")
			return err
		}
	}
}

// collect takes one sample (the slow group too when due) into the buffer.
func (l *TelemetryLoop) collect(ctx context.Context, now time.Time, perCore, slow bool) {
	var s collect.Snapshot
	if err := l.Collector.Fast(ctx, perCore, &s); err != nil {
		l.log().Debug("быстрая группа собрана с ошибками", "event", "collect", "err", err)
	}
	fastCaps := slices.Clone(s.Capabilities)
	if slow {
		s.Capabilities = s.Capabilities[:0]
		if err := l.Collector.Slow(ctx, &s); err != nil {
			l.log().Debug("медленная группа собрана с ошибками", "event", "collect", "err", err)
		}
		l.slowTick(&s, now)
	}
	l.mu.Lock()
	l.fastCaps = fastCaps
	rec := buffer.Record{At: now, MonoMS: l.mono(), BootID: l.bootID, ClockSynced: l.synced,
		Samples: make([]buffer.Sample, 0, len(s.Points))}
	l.mu.Unlock()
	for _, p := range s.Points {
		kind := buffer.Gauge
		if p.Kind == collect.Counter {
			kind = buffer.Counter
		}
		var labels []buffer.Label
		for _, a := range p.Attrs {
			labels = append(labels, buffer.Label{K: a.Key, V: a.Value})
		}
		rec.Samples = append(rec.Samples, buffer.Sample{Name: p.Name, Kind: kind, Labels: labels, Value: p.Value})
	}
	if err := l.Buffer.Add(rec); err != nil {
		l.log().Warn("точки не записаны в буфер", "event", "collect", "err", err)
	}
}

// slowTick remembers the system state and turns its changes into events:
// system.boot (a new boot_id since the previous start), unit.failed and
// unit.recovered (the failed set changed since the previous slow tick).
func (l *TelemetryLoop) slowTick(s *collect.Snapshot, now time.Time) {
	l.mu.Lock()
	first := l.bootID == ""
	l.slowCaps = slices.Clone(s.Capabilities)
	l.failed = slices.Clone(s.FailedUnits)
	if s.BootID != "" {
		l.bootID = s.BootID
	}
	l.synced = s.ClockKnown && s.ClockSynced
	prev := l.units
	cur := map[string]bool{}
	for _, u := range s.FailedUnits {
		cur[u] = true
	}
	if s.FailedUnits != nil {
		l.units = cur
	}
	boot := l.bootID
	l.mu.Unlock()

	var events []buffer.Event
	if first && boot != "" && l.LastBoot != nil {
		if was := l.LastBoot(); was != "" && was != boot {
			events = append(events, buffer.Event{Kind: "system.boot", Severity: "info", At: now,
				Params: map[string]any{"boot_id": boot, "previous_boot_id": was, "uptime_s": uptime(s)}})
		}
		if l.SaveBoot != nil {
			if err := l.SaveBoot(boot); err != nil {
				l.log().Warn("boot_id не сохранён", "event", "collect", "err", err)
			}
		}
	}
	if prev != nil && s.FailedUnits != nil {
		for _, u := range sortedKeys(cur) {
			if !prev[u] {
				events = append(events, buffer.Event{Kind: "unit.failed", Severity: "warning", At: now,
					Params: map[string]any{"unit": u}})
			}
		}
		for _, u := range sortedKeys(prev) {
			if !cur[u] {
				events = append(events, buffer.Event{Kind: "unit.recovered", Severity: "info", At: now,
					Params: map[string]any{"unit": u}})
			}
		}
	}
	for _, e := range events {
		if err := l.Buffer.AddEvent(e); err != nil {
			l.log().Warn("событие не записано в буфер", "event", "collect", "kind", e.Kind, "err", err)
		}
	}
}

func uptime(s *collect.Snapshot) float64 {
	for _, p := range s.Points {
		if p.Name == collect.MetricUptime {
			return p.Value
		}
	}
	return 0
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// InventoryRejectPause holds back an inventory the portal dropped (400 with a
// contract code, controller ruling Task 21): the same hash is not sent again
// before it ends — one 400 an hour at most if the body is really bad — but it
// is sent after it, as the 400 may have been a portal failure since fixed. A
// change of the hardware is sent at once.
const InventoryRejectPause = telemetry.RejectPause

// inventory sends the inventory when its hash (without collected_at and the
// interface addresses) is not the one the portal last accepted, and is not
// one it dropped less than InventoryRejectPause ago.
func (l *TelemetryLoop) inventory(ctx context.Context) {
	body, err := l.Inventory.Collect()
	if err != nil {
		l.log().Debug("инвентарь собран с ошибками", "event", "inventory", "err", err)
	}
	hash, herr := inventory.Hash(body)
	if herr != nil {
		l.log().Warn("хэш инвентаря не посчитан", "event", "inventory", "err", herr)
		return
	}
	now := l.now()
	l.mu.Lock()
	skip := hash == l.invHash || (hash == l.invRejected && now.Before(l.invRejectedUntil))
	l.mu.Unlock()
	if skip {
		return
	}
	r := l.Sender.SyncInventory(ctx, body)
	switch r.Outcome {
	case telemetry.OutcomeSent, telemetry.OutcomeMatch, telemetry.OutcomeDuplicate:
		l.mu.Lock()
		l.invHash = hash
		l.mu.Unlock()
		l.log().Info("инвентарь отправлен", "event", "inventory", "outcome", r.Outcome)
	case telemetry.OutcomeDropped:
		// 400 with a contract code or a body that cannot be sent: not again
		// before InventoryRejectPause unless the hardware changes.
		l.mu.Lock()
		l.invRejected, l.invRejectedUntil = hash, now.Add(InventoryRejectPause)
		l.mu.Unlock()
		l.log().Warn("инвентарь отвергнут — повтор через паузу или после изменения", "event", "inventory",
			"outcome", r.Outcome, "status", r.HTTPStatus, "wait", InventoryRejectPause, "err", r.Err)
	default:
		l.log().Debug("инвентарь не отправлен — повтор на следующей проверке", "event", "inventory",
			"outcome", r.Outcome, "status", r.HTTPStatus, "err", r.Err)
	}
	l.report()
}

// send makes one request and returns the pause before the next one: at once
// while the buffer drains, the portal's next_after, the sender's pause on a
// failure, about a minute (10 s in the detail mode) once idle. idle is true
// for the agent's own idle pause only; paused — for a failure pause.
func (l *TelemetryLoop) send(ctx context.Context) (wait time.Duration, idle, paused bool) {
	r := l.Sender.Step(ctx)
	l.mu.Lock()
	if r.Outcome != telemetry.OutcomeIdle {
		l.lastSend, l.lastSendT = r, l.now()
	}
	l.mu.Unlock()
	l.report()
	switch r.Outcome {
	case telemetry.OutcomeSent, telemetry.OutcomeDuplicate, telemetry.OutcomeMatch,
		telemetry.OutcomeSplit, telemetry.OutcomeDropped:
		return r.Wait, false, false
	case telemetry.OutcomeIdle:
		cfg := l.Sender.Config()
		if cfg.Detail(l.now()) {
			return cfg.DetailSend, true, false
		}
		return SendInterval - SendJitter + time.Duration(l.rand(int64(2*SendJitter))), true, false
	}
	l.log().Debug("отправка телеметрии отложена", "event", "send", "outcome", r.Outcome,
		"status", r.HTTPStatus, "wait", r.Wait, "err", r.Err)
	if r.Wait <= 0 {
		return SendInterval, false, true
	}
	return r.Wait, false, true
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func (l *TelemetryLoop) report() {
	if l.Report == nil {
		return
	}
	cfg := l.Sender.Config()
	l.mu.Lock()
	rep := TelemetryReport{At: l.lastSendT, Outcome: l.lastSend.Outcome, HTTPStatus: l.lastSend.HTTPStatus,
		ConfigHash: cfg.Hash, DetailUntil: cfg.DetailUntil, InventoryHash: l.invHash}
	l.mu.Unlock()
	rep.Buffer = l.Buffer.Stats()
	l.Report(rep)
}

func (l *TelemetryLoop) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

func (l *TelemetryLoop) mono() int64 {
	if l.MonoMS != nil {
		return l.MonoMS()
	}
	return 0
}

func (l *TelemetryLoop) after(d time.Duration) <-chan time.Time {
	if l.After != nil {
		return l.After(d)
	}
	return time.After(d)
}

func (l *TelemetryLoop) rand(n int64) int64 {
	if n <= 0 {
		return 0
	}
	if l.Rand != nil {
		return l.Rand(n)
	}
	return rand.Int64N(n)
}

func (l *TelemetryLoop) log() *slog.Logger {
	if l.Log != nil {
		return l.Log
	}
	return slog.New(slog.DiscardHandler)
}
