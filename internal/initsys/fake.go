package initsys

import (
	"context"
	"sync"
)

// Fake is an in-memory Manager (systemd or procd) for tests. It records every call as
// "<verb> <unit>" and answers from its fields. It is safe for concurrent use.
type Fake struct {
	mu sync.Mutex

	EnableErr, RestartErr, DisableErr, StartErr, StopErr error
	// Active is what IsActive reports; "" — "inactive".
	Active string

	calls []string
}

var _ Manager = (*Fake)(nil)

// Enable implements Manager.
func (f *Fake) Enable(_ context.Context, unit string) error {
	return f.record("enable "+unit, f.EnableErr)
}

// Restart implements Manager.
func (f *Fake) Restart(_ context.Context, unit string) error {
	return f.record("restart "+unit, f.RestartErr)
}

// DisableNow implements Manager.
func (f *Fake) DisableNow(_ context.Context, unit string) error {
	return f.record("disable --now "+unit, f.DisableErr)
}

// Start implements Manager.
func (f *Fake) Start(_ context.Context, unit string) error {
	return f.record("start "+unit, f.StartErr)
}

// Stop implements Manager.
func (f *Fake) Stop(_ context.Context, unit string) error {
	return f.record("stop "+unit, f.StopErr)
}

// IsActive implements Manager.
func (f *Fake) IsActive(_ context.Context, unit string) string {
	_ = f.record("is-active "+unit, nil)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Active == "" {
		return "inactive"
	}
	return f.Active
}

// Calls returns the recorded calls in order.
func (f *Fake) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *Fake) record(call string, err error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
	return err
}
