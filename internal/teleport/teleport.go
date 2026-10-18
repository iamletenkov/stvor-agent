// Package teleport is the agent's view of the cream extension teleport: the
// managed labels it reports and the action that sets them (spec §5.7).
//
// The interface lives here so that reconcile, daemon and cli are written against
// it; the exec adapter over `cream run teleport …` and a fake for tests sit next
// to it.
package teleport

import (
	"context"
	"errors"
)

// Labels is the full set of managed labels the agent owns in teleport. Only
// these two keys ever reach the extension (spec §6). It has the same shape as
// portal.Labels, so reconcile converts with teleport.Labels(p) and the adapter
// does not depend on the portal client.
type Labels struct {
	Env   string
	Space string
}

// Status is the answer of `cream run teleport managed-status` (schema 1).
type Status struct {
	Managed Labels
	Pending bool // a set-managed restart is not confirmed yet
	Joined  bool // the node is registered in the Teleport cluster
	Active  bool // teleport.service is active
}

// Result values of `set-managed` (spec §5.7).
const (
	ResultApplied     = "applied"     // labels written, restart confirmed (exit 0)
	ResultUnchanged   = "unchanged"   // labels already in place (exit 0)
	ResultDeferred    = "deferred"    // node not joined yet, labels wait for join (exit 0)
	ResultUnconfirmed = "unconfirmed" // restart not confirmed, pending stays (exit 1)
)

// SetResult is the answer of `cream run teleport set-managed`: the status after
// the action plus its result (one of the Result* constants).
type SetResult struct {
	Status
	Result string
}

// Teleport is what the agent needs from the teleport extension.
type Teleport interface {
	// Status runs managed-status. Any error means "state unknown": the tick
	// must not touch anything.
	Status(ctx context.Context) (Status, error)
	// SetManaged replaces the managed labels with exactly l.
	SetManaged(ctx context.Context, l Labels) (SetResult, error)
	// CheckActions verifies once at start that teleport offers managed-status
	// and set-managed: nil — ok; ErrActionsMissing — the list was read and an
	// action is absent (ticks run without applying); any other error — the
	// list could not be read (log it and keep applying; spec §5.7, ruling 7).
	CheckActions(ctx context.Context) error
}

// Error classes of the cream exit codes (spec cream §11.3). Match with errors.Is.
// The agent counts them as a failed tick without counting a restart attempt.
var (
	// ErrLockBusy — exit 75: the teleport domain lock was busy longer than
	// --lock-timeout (for example, a join is running).
	ErrLockBusy = errors.New("teleport: cream lock busy")
	// ErrProtocol — exit 76: the action broke the cream protocol.
	ErrProtocol = errors.New("teleport: action broke the cream protocol")
	// ErrCream — any other exit in 64–79: cream failed before the action ran.
	ErrCream = errors.New("teleport: cream failed")
	// ErrActionsMissing — CheckActions read the action list and managed-status
	// or set-managed is not there.
	ErrActionsMissing = errors.New("teleport: required actions missing")
)
