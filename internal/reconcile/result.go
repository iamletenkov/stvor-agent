// Package reconcile holds one tick of the agent: fetch the desired labels from
// the portal, compare them with teleport, apply, report (spec §5.3).
package reconcile

import (
	"github.com/iamletenkov/stvor-agent/internal/portal"
	"github.com/iamletenkov/stvor-agent/internal/teleport"
)

// Outcome is how the portal poll of a tick ended. The strings are a contract:
// they are written to state.json (last_poll.outcome) and shown by
// `status --json` (spec §5.5, §7).
type Outcome string

// Poll outcomes (spec §7) plus OutcomeDisabled for "no token file".
const (
	OutcomeOK            Outcome = "ok"             // 200 with a valid body
	OutcomeNotModified   Outcome = "not_modified"   // 304
	OutcomeUnauthorized  Outcome = "unauthorized"   // 401: token revoked
	OutcomeRateLimited   Outcome = "rate_limited"   // 429
	OutcomeNetwork       Outcome = "network"        // DNS, TCP, TLS, timeout
	OutcomeRedirect      Outcome = "redirect"       // 3xx other than 304
	OutcomeHTTPError     Outcome = "http_error"     // 5xx and other codes
	OutcomeBadBody       Outcome = "bad_body"       // 200 body not by the contract
	OutcomeSkippedLocked Outcome = "skipped_locked" // another operation holds the agent lock
	OutcomeDisabled      Outcome = "disabled"       // no token file: the agent is not enabled
)

// What the tick did with teleport, in Result.Apply.
const (
	ApplyNone        = ""            // labels were in effect or not touched
	ApplyApplied     = "applied"     // set-managed: applied
	ApplyUnchanged   = "unchanged"   // set-managed: unchanged
	ApplyDeferred    = "deferred"    // set-managed: node not joined yet
	ApplyUnconfirmed = "unconfirmed" // set-managed: restart not confirmed
	ApplyPaused      = "paused"      // skipped: 3 unconfirmed in a row, pause up to 15 min
	ApplyError       = "error"       // managed-status or set-managed failed
	ApplyStopped     = "stopped"     // skipped: the service is stopping, set-managed not started
)

// Result is what one tick returns to its caller (daemon or cli).
//
// The tick does not suggest a sleep: the failure counter and the backoff live
// only in the daemon, which derives them from Outcome. The only pause the tick
// decides itself (3 unconfirmed restarts / 15 min) shows up as Apply ==
// ApplyPaused and does not feed the backoff. The tick itself writes last_poll
// and failures to state.json.
type Result struct {
	Outcome    Outcome
	HTTPStatus int    // portal status code; 0 when no response was received
	Apply      string // one of the Apply* constants
	TokenErr   error  // token file present but unusable (mode, owner, format)

	// The end state of the tick, for `sync` (no second managed-status outside
	// the lock). Expected is the portal answer the tick worked with; nil — none
	// (no token, a failed poll, 304 without remembered labels). StatusErr is
	// the managed-status error, if the last call failed. Teleport is the last
	// managed-status seen; InEffect — it runs exactly Expected.
	Expected  *portal.Expected
	Teleport  teleport.Status
	StatusErr error
	InEffect  bool
}
