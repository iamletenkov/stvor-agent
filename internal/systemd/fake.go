package systemd

import (
	"context"
	"sync"
)

// Fake is an in-memory Systemctl for tests. It records every call as
// "<verb> <unit>" and answers from its fields. It is safe for concurrent use.
type Fake struct {
	mu sync.Mutex

	EnableErr, RestartErr, DisableErr error
	// Active is what IsActive reports; "" — "inactive".
	Active string

	calls []string
}

var _ Systemctl = (*Fake)(nil)

// Enable implements Systemctl.
func (f *Fake) Enable(_ context.Context, unit string) error {
	return f.record("enable "+unit, f.EnableErr)
}

// Restart implements Systemctl.
func (f *Fake) Restart(_ context.Context, unit string) error {
	return f.record("restart "+unit, f.RestartErr)
}

// DisableNow implements Systemctl.
func (f *Fake) DisableNow(_ context.Context, unit string) error {
	return f.record("disable --now "+unit, f.DisableErr)
}

// IsActive implements Systemctl.
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
