// Package teleportfake is an in-memory teleport.Teleport for tests of reconcile,
// daemon and cli. It is safe for concurrent use.
package teleportfake

import (
	"context"
	"sync"

	"github.com/iamletenkov/stvor-agent/internal/teleport"
)

// Fake answers from its fields. By default SetManaged behaves like a joined,
// active node: labels equal to Current.Managed give "unchanged", others are
// written and give "applied"; when Current.Joined is false they are written and
// give "deferred". SetFunc, SetErr, StatusErr and ActionsErr override that.
type Fake struct {
	mu sync.Mutex

	// Current is what Status reports; the default SetManaged updates it.
	Current teleport.Status
	// StatusErr, when set, is returned by Status.
	StatusErr error
	// SetErr, when set, is returned by SetManaged (nothing is written).
	SetErr error
	// SetFunc, when set, replaces the default SetManaged; it runs under the
	// fake's lock and may change f.Current directly.
	SetFunc func(f *Fake, l teleport.Labels) (teleport.SetResult, error)
	// ActionsErr is returned by CheckActions (nil — the actions are there).
	ActionsErr error

	statusCalls  int
	setCalls     []teleport.Labels
	actionsCalls int
}

var _ teleport.Teleport = (*Fake)(nil)

// Status implements teleport.Teleport.
func (f *Fake) Status(context.Context) (teleport.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statusCalls++
	if f.StatusErr != nil {
		return teleport.Status{}, f.StatusErr
	}
	return f.Current, nil
}

// SetManaged implements teleport.Teleport.
func (f *Fake) SetManaged(_ context.Context, l teleport.Labels) (teleport.SetResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setCalls = append(f.setCalls, l)
	switch {
	case f.SetFunc != nil:
		return f.SetFunc(f, l)
	case f.SetErr != nil:
		return teleport.SetResult{}, f.SetErr
	}
	result := teleport.ResultApplied
	switch {
	case !f.Current.Joined:
		result = teleport.ResultDeferred
	case f.Current.Managed == l && !f.Current.Pending && f.Current.Active:
		result = teleport.ResultUnchanged
	}
	f.Current.Managed = l
	if f.Current.Joined {
		f.Current.Pending = false
		f.Current.Active = true
	}
	return teleport.SetResult{Status: f.Current, Result: result}, nil
}

// CheckActions implements teleport.Teleport.
func (f *Fake) CheckActions(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.actionsCalls++
	return f.ActionsErr
}

// StatusCalls is the number of Status calls so far.
func (f *Fake) StatusCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.statusCalls
}

// SetCalls returns the labels of every SetManaged call so far, in order.
func (f *Fake) SetCalls() []teleport.Labels {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]teleport.Labels(nil), f.setCalls...)
}

// ActionsCalls is the number of CheckActions calls so far.
func (f *Fake) ActionsCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.actionsCalls
}
