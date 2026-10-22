package daemon

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/iamletenkov/stvor-agent/internal/buffer"
	"github.com/iamletenkov/stvor-agent/internal/collect"
	"github.com/iamletenkov/stvor-agent/internal/inventory"
	"github.com/iamletenkov/stvor-agent/internal/telemetry"
)

var telT0 = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

const bootA = "11111111-2222-3333-4444-555555555555"

// fakeTime is the clock of a loop test: After advances it at once.
type fakeTime struct {
	mu     sync.Mutex
	now    time.Time
	until  time.Time
	cancel context.CancelFunc
}

func (f *fakeTime) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeTime) MonoMS() int64 { return f.Now().Sub(telT0).Milliseconds() + 1000 }

func (f *fakeTime) After(d time.Duration) <-chan time.Time {
	f.mu.Lock()
	f.now = f.now.Add(d)
	if !f.now.Before(f.until) {
		f.cancel()
	}
	f.mu.Unlock()
	c := make(chan time.Time, 1)
	c <- f.Now()
	return c
}

type fakeCollector struct {
	ft         *fakeTime
	fast, slow []time.Time
	perCore    []bool
	units      [][]string // failed units per slow tick; the last one repeats
}

func (c *fakeCollector) Fast(_ context.Context, perCore bool, s *collect.Snapshot) error {
	c.fast = append(c.fast, c.ft.Now())
	c.perCore = append(c.perCore, perCore)
	s.Points = append(s.Points,
		collect.Point{Name: collect.MetricCPUTime, Kind: collect.Counter, Value: 12.5, Attrs: []collect.Attr{{Key: "mode", Value: "user"}}},
		collect.Point{Name: collect.MetricMemoryLimit, Kind: collect.Gauge, Value: 4e9})
	s.Capabilities = append(s.Capabilities, collect.CapPSI)
	return nil
}

func (c *fakeCollector) Slow(_ context.Context, s *collect.Snapshot) error {
	c.slow = append(c.slow, c.ft.Now())
	s.BootID, s.ClockKnown, s.ClockSynced = bootA, true, true
	s.Points = append(s.Points, collect.Point{Name: collect.MetricUptime, Kind: collect.Gauge, Value: 42})
	s.Capabilities = append(s.Capabilities, collect.CapSystemd, collect.CapThermal)
	i := min(len(c.slow)-1, len(c.units)-1)
	if i >= 0 {
		s.FailedUnits = c.units[i]
	}
	return errors.New("one source failed") // the rest is still used
}

type fakeInventory struct {
	calls  int
	bodies []string // OS.ID per call; the last one repeats
}

func (f *fakeInventory) Collect() (inventory.Body, error) {
	f.calls++
	var b inventory.Body
	b.OS.ID = f.bodies[min(f.calls-1, len(f.bodies)-1)]
	b.CollectedAt = time.Now().String() // never part of the hash
	return b, nil
}

type fakeSender struct {
	ft      *fakeTime
	cfg     telemetry.Config
	steps   []time.Time
	script  []telemetry.Result // results in order; then Idle
	invs    []string
	invResp telemetry.Outcome
}

func (f *fakeSender) Step(context.Context) telemetry.Result {
	f.steps = append(f.steps, f.ft.Now())
	if len(f.script) == 0 {
		return telemetry.Result{Outcome: telemetry.OutcomeIdle}
	}
	r := f.script[0]
	f.script = f.script[1:]
	return r
}

func (f *fakeSender) SyncInventory(_ context.Context, b inventory.Body) telemetry.Result {
	f.invs = append(f.invs, b.OS.ID)
	return telemetry.Result{Outcome: f.invResp}
}

func (f *fakeSender) Config() telemetry.Config { return f.cfg }

type fakeBuffer struct {
	recs   []buffer.Record
	events []buffer.Event
	closed int
}

func (b *fakeBuffer) Add(r buffer.Record) error     { b.recs = append(b.recs, r); return nil }
func (b *fakeBuffer) AddEvent(e buffer.Event) error { b.events = append(b.events, e); return nil }
func (b *fakeBuffer) Stats() buffer.Stats           { return buffer.Stats{UsedBytes: int64(len(b.recs))} }
func (b *fakeBuffer) Close() error                  { b.closed++; return nil }
func (b *fakeBuffer) kinds() (out []string)         { return eventKinds(b.events) }
func eventKinds(es []buffer.Event) (out []string) {
	for _, e := range es {
		out = append(out, e.Kind)
	}
	return out
}

type loopRig struct {
	ft   *fakeTime
	col  *fakeCollector
	inv  *fakeInventory
	snd  *fakeSender
	buf  *fakeBuffer
	loop *TelemetryLoop
	reps []TelemetryReport
	boot string
}

func defaultTelemetryConfig() telemetry.Config {
	return telemetry.Config{FastInterval: 10 * time.Second, SlowInterval: 60 * time.Second,
		InventoryCheck: 60 * time.Second, DetailSample: 5 * time.Second, DetailSend: 10 * time.Second}
}

func newLoopRig() *loopRig {
	ft := &fakeTime{now: telT0}
	r := &loopRig{ft: ft, col: &fakeCollector{ft: ft}, inv: &fakeInventory{bodies: []string{"debian"}},
		snd: &fakeSender{ft: ft, cfg: defaultTelemetryConfig(), invResp: telemetry.OutcomeSent}, buf: &fakeBuffer{}}
	r.loop = &TelemetryLoop{
		Collector: r.col, Inventory: r.inv, Sender: r.snd, Buffer: r.buf,
		Now: ft.Now, MonoMS: ft.MonoMS, After: ft.After,
		Rand:     func(n int64) int64 { return n / 2 },
		LastBoot: func() string { return r.boot },
		SaveBoot: func(id string) error { r.boot = id; return nil },
		Report:   func(rep TelemetryReport) { r.reps = append(r.reps, rep) },
	}
	return r
}

// run runs the loop for d of fake time.
func (r *loopRig) run(t *testing.T, d time.Duration) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r.ft.until, r.ft.cancel = telT0.Add(d), cancel
	done := make(chan error, 1)
	go func() { done <- r.loop.Run(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the loop did not stop")
	}
}

func offsets(ts []time.Time) []time.Duration {
	out := make([]time.Duration, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.Sub(telT0))
	}
	return out
}

func every(step, upTo time.Duration) []time.Duration {
	var out []time.Duration
	for d := time.Duration(0); d <= upTo; d += step {
		out = append(out, d)
	}
	return out
}

// Contract groups: fast every 10 s, slow every 60 s, the inventory checked
// every 60 s and sent once while its hash holds; a send about every minute.
func TestTelemetryLoopSchedule(t *testing.T) {
	t.Parallel()
	r := newLoopRig()
	r.run(t, 5*time.Minute)

	if got, want := offsets(r.col.fast), every(10*time.Second, 5*time.Minute-10*time.Second); !slices.Equal(got, want) {
		t.Errorf("fast at %v, want %v", got, want)
	}
	if got, want := offsets(r.col.slow), every(time.Minute, 4*time.Minute); !slices.Equal(got, want) {
		t.Errorf("slow at %v, want %v", got, want)
	}
	if r.inv.calls != 5 || !slices.Equal(r.snd.invs, []string{"debian"}) {
		t.Errorf("inventory: %d checks, sent %v; want 5 checks, sent once", r.inv.calls, r.snd.invs)
	}
	// First send after U[0, 10 s) (Rand = n/2 → 5 s), then 60 ± 6 s.
	if got, want := offsets(r.snd.steps), every(time.Minute, 4*time.Minute); len(got) != len(want) || got[0] != 5*time.Second {
		t.Errorf("sends at %v, want 5 s then every minute", got)
	}
	if r.buf.closed != 1 {
		t.Errorf("buffer closed %d times, want 1 (flush on stop)", r.buf.closed)
	}
}

// A sample becomes a buffer record: kinds, labels, boot and clock of the
// slow group, the counter stays raw.
func TestTelemetryLoopRecords(t *testing.T) {
	t.Parallel()
	r := newLoopRig()
	r.run(t, 15*time.Second)
	if len(r.buf.recs) != 2 {
		t.Fatalf("records = %d, want 2", len(r.buf.recs))
	}
	first, second := r.buf.recs[0], r.buf.recs[1]
	if len(first.Samples) != 3 || len(second.Samples) != 2 {
		t.Errorf("samples: %d (fast+slow) and %d (fast), want 3 and 2", len(first.Samples), len(second.Samples))
	}
	s := first.Samples[0]
	if s.Kind != buffer.Counter || s.Value != 12.5 || !slices.Equal(s.Labels, []buffer.Label{{K: "mode", V: "user"}}) {
		t.Errorf("cpu sample %+v", s)
	}
	if first.Samples[1].Kind != buffer.Gauge {
		t.Error("gauge became a counter")
	}
	for _, rec := range r.buf.recs {
		if rec.BootID != bootA || !rec.ClockSynced || rec.MonoMS <= 0 {
			t.Errorf("record %v: boot %q synced %t mono %d", rec.At, rec.BootID, rec.ClockSynced, rec.MonoMS)
		}
	}
	caps, _ := r.loop.Meta()
	slices.Sort(caps)
	if want := []string{collect.CapPSI, collect.CapSystemd, collect.CapThermal}; !slices.Equal(caps, want) {
		t.Errorf("capabilities = %v, want %v", caps, want)
	}
}

// The detail mode (portal config): fast every 5 s, a send every 10 s.
func TestTelemetryLoopDetail(t *testing.T) {
	t.Parallel()
	r := newLoopRig()
	r.snd.cfg.DetailUntil = telT0.Add(time.Minute)
	r.snd.cfg.PerCore = true
	r.run(t, time.Minute)
	if got, want := offsets(r.col.fast), every(5*time.Second, 55*time.Second); !slices.Equal(got, want) {
		t.Errorf("fast at %v, want %v", got, want)
	}
	if got := offsets(r.snd.steps); len(got) != 6 || got[1]-got[0] != 10*time.Second {
		t.Errorf("sends at %v, want every 10 s", got)
	}
	if !r.col.perCore[0] {
		t.Error("per_core of the config not passed to the collector")
	}
}

// While the buffer drains, the next chunk goes at once; a failure waits the
// sender's pause; the report carries the last real outcome.
func TestTelemetryLoopDrainAndBackoff(t *testing.T) {
	t.Parallel()
	r := newLoopRig()
	r.loop.Rand = func(int64) int64 { return 0 }
	r.snd.script = []telemetry.Result{
		{Outcome: telemetry.OutcomeSent, HTTPStatus: 200},
		{Outcome: telemetry.OutcomeSent, HTTPStatus: 200},
		{Outcome: telemetry.OutcomeRetry, HTTPStatus: 503, Wait: 30 * time.Second},
		{Outcome: telemetry.OutcomeSent, HTTPStatus: 202, Wait: 2 * time.Second},
	}
	r.run(t, 40*time.Second)
	want := []time.Duration{0, 0, 0, 30 * time.Second, 32 * time.Second}
	if got := offsets(r.snd.steps); !slices.Equal(got, want) {
		t.Errorf("steps at %v, want %v", got, want)
	}
	last := r.reps[len(r.reps)-1]
	if last.Outcome != telemetry.OutcomeSent || last.HTTPStatus != 202 || last.InventoryHash == "" {
		t.Errorf("last report %+v", last)
	}
}

// The inventory is sent again when the hardware changes, and retried while
// the portal does not take it.
func TestTelemetryLoopInventory(t *testing.T) {
	t.Parallel()
	r := newLoopRig()
	r.inv.bodies = []string{"debian", "debian", "ubuntu"}
	r.snd.invResp = telemetry.OutcomeRetry
	r.run(t, 2*time.Minute+time.Second)
	if want := []string{"debian", "debian", "ubuntu"}; !slices.Equal(r.snd.invs, want) {
		t.Errorf("inventory sent %v, want %v (retry, then the change)", r.snd.invs, want)
	}
}

// A dropped inventory (400 with a contract code) is held back for
// InventoryRejectPause, then the same hash goes again (the 400 may have been a
// portal failure); a change of the hardware goes at once.
func TestTelemetryLoopInventoryDroppedPause(t *testing.T) {
	t.Parallel()
	changed := newLoopRig()
	changed.inv.bodies = []string{"debian", "debian", "ubuntu"}
	changed.snd.invResp = telemetry.OutcomeDropped
	changed.run(t, 2*time.Minute+time.Second)
	if want := []string{"debian", "ubuntu"}; !slices.Equal(changed.snd.invs, want) {
		t.Errorf("inventory sent %v, want %v (no retry within the pause, the change at once)", changed.snd.invs, want)
	}

	within := newLoopRig()
	within.inv.bodies = []string{"debian"}
	within.snd.invResp = telemetry.OutcomeDropped
	within.run(t, InventoryRejectPause-time.Minute)
	if len(within.snd.invs) != 1 {
		t.Errorf("within the pause: sent %d times, want 1", len(within.snd.invs))
	}

	after := newLoopRig()
	after.inv.bodies = []string{"debian"}
	after.snd.invResp = telemetry.OutcomeDropped
	after.run(t, InventoryRejectPause+time.Minute+time.Second)
	if want := []string{"debian", "debian"}; !slices.Equal(after.snd.invs, want) {
		t.Errorf("after the pause: sent %v, want %v (the same hash again)", after.snd.invs, want)
	}
}

// Events: system.boot on a new boot_id, unit.failed / unit.recovered on the
// BootID is the current boot for the sender identity of the inventory and
// the events: "" before the first slow tick, then the collector's.
func TestTelemetryLoopBootID(t *testing.T) {
	t.Parallel()
	r := newLoopRig()
	if got := r.loop.BootID(); got != "" {
		t.Fatalf("before the loop BootID = %q", got)
	}
	r.run(t, 10*time.Second)
	if got := r.loop.BootID(); got != bootA {
		t.Fatalf("BootID = %q, want %q", got, bootA)
	}
}

// change of the failed set; nothing for what was there at start.
func TestTelemetryLoopEvents(t *testing.T) {
	t.Parallel()
	r := newLoopRig()
	r.boot = "99999999-2222-3333-4444-555555555555"
	r.col.units = [][]string{{"a.service"}, {"a.service", "b.service"}, {"b.service"}}
	r.run(t, 2*time.Minute+time.Second)
	if want := []string{"system.boot", "unit.failed", "unit.recovered"}; !slices.Equal(r.buf.kinds(), want) {
		t.Fatalf("events = %v, want %v", r.buf.kinds(), want)
	}
	if p := r.buf.events[0].Params; p["previous_boot_id"] != "99999999-2222-3333-4444-555555555555" || p["boot_id"] != bootA {
		t.Errorf("system.boot params %v", p)
	}
	if r.buf.events[1].Params["unit"] != "b.service" || r.buf.events[2].Params["unit"] != "a.service" {
		t.Errorf("unit events %v", r.buf.events[1:])
	}
	if r.boot != bootA {
		t.Errorf("boot_id saved = %q", r.boot)
	}

	same := newLoopRig()
	same.boot = bootA
	same.run(t, 10*time.Second)
	if len(same.buf.events) != 0 {
		t.Errorf("restart within one boot: events %v", same.buf.kinds())
	}
}

// The detail mode shortens only the idle pause: a Retry-After, a backoff,
// the reject pause and the portal's next_after are kept.
func TestTelemetryLoopDetailKeepsServerPauses(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		res  telemetry.Result
	}{
		{"retry_after_503", telemetry.Result{Outcome: telemetry.OutcomeRetry, HTTPStatus: 503, Wait: 300 * time.Second}},
		{"retry_after_429", telemetry.Result{Outcome: telemetry.OutcomeRetry, HTTPStatus: 429, Wait: 300 * time.Second}},
		{"backoff", telemetry.Result{Outcome: telemetry.OutcomeRetry, Wait: 300 * time.Second}},
		{"reject_pause", telemetry.Result{Outcome: telemetry.OutcomeRejected, HTTPStatus: 422, Wait: telemetry.RejectPause}},
		{"next_after", telemetry.Result{Outcome: telemetry.OutcomeSent, HTTPStatus: 202, Wait: 300 * time.Second}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newLoopRig()
			r.loop.Rand = func(int64) int64 { return 0 }
			// The detail mode is on for the whole run.
			r.snd.cfg.DetailUntil = telT0.Add(2 * time.Hour)
			r.snd.script = []telemetry.Result{tc.res}
			r.run(t, 299*time.Second)
			if got := offsets(r.snd.steps); !slices.Equal(got, []time.Duration{0}) {
				t.Errorf("steps at %v, want only the first one before the pause of 300 s", got)
			}
		})
	}
}

// The detail mode that comes on while the loop waits out the idle minute
// sends within DetailSend.
func TestTelemetryLoopDetailShortensIdle(t *testing.T) {
	t.Parallel()
	r := newLoopRig()
	r.loop.Rand = func(int64) int64 { return 0 }
	r.snd.script = []telemetry.Result{{Outcome: telemetry.OutcomeIdle}}
	r.run(t, 30*time.Second)
	if got := offsets(r.snd.steps); len(got) != 1 {
		t.Fatalf("steps at %v, want one before the detail mode", got)
	}
	r2 := newLoopRig()
	r2.loop.Rand = func(int64) int64 { return 0 }
	// The config turns the detail mode on at 20 s (the sender reads it).
	on := telT0.Add(20 * time.Second)
	r2.loop.Sender = &detailAt{fakeSender: r2.snd, on: on}
	r2.run(t, 35*time.Second)
	got := offsets(r2.snd.steps)
	if len(got) < 2 || got[1] > 30*time.Second {
		t.Errorf("steps at %v, want a send within 10 s of the detail mode at 20 s", got)
	}
}

// detailAt switches the detail mode on at the given moment.
type detailAt struct {
	*fakeSender
	on time.Time
}

func (d *detailAt) Config() telemetry.Config {
	c := d.fakeSender.Config()
	if !d.ft.Now().Before(d.on) {
		c.DetailUntil = d.on.Add(time.Hour)
	}
	return c
}

// While the sender waits out a pause (Retry-After, reject pause), the
// inventory is not sent: the portal asked to be left alone.
func TestTelemetryLoopInventoryWaitsPause(t *testing.T) {
	t.Parallel()
	r := newLoopRig()
	r.loop.Rand = func(int64) int64 { return 0 }
	r.inv.bodies = []string{"debian", "ubuntu"}
	r.snd.script = []telemetry.Result{{Outcome: telemetry.OutcomeRetry, HTTPStatus: 503, Wait: 5 * time.Minute}}
	r.run(t, 4*time.Minute)
	// The inventory at 0 s goes before the first send; the change at 60 s
	// waits for the end of the pause.
	if want := []string{"debian"}; !slices.Equal(r.snd.invs, want) {
		t.Errorf("inventory sent %v during the pause, want %v", r.snd.invs, want)
	}
}
