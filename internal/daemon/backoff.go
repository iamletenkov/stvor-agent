package daemon

import (
	"math/rand/v2"
	"time"

	"github.com/iamletenkov/stvor-agent/internal/reconcile"
)

// Sleep bounds of the loop (spec §5.2).
const (
	// MaxJitter is the spread added to every sleep and the bound of the
	// first one: U[0, 20 s), as RandomizedDelaySec=20s of the old timer.
	MaxJitter = 20 * time.Second
	// MaxSleep caps the backoff and is the flat sleep after 401.
	MaxSleep = 600 * time.Second
	// maxExp caps the exponent of the backoff: interval·2^min(n, 3).
	maxExp = 3
)

// Next returns the failure counter after a tick with outcome o (n failures in
// a row before it) and the sleep before the next tick; jitter tells whether
// the caller adds U[0, MaxJitter) on top.
//
//   - 200/304 reset n: interval;
//   - 401: n kept, MaxSleep flat — the token was revoked, only `enable`
//     (a service restart) speeds it up;
//   - disabled (no token) and skipped_locked (no poll was made): n kept,
//     interval;
//   - anything else (network, TLS, 3xx, 429, 5xx, bad body): n+1,
//     min(interval·2^min(n+1, 3), MaxSleep).
func Next(interval time.Duration, n int, o reconcile.Outcome) (next int, sleep time.Duration, jitter bool) {
	switch o {
	case reconcile.OutcomeOK, reconcile.OutcomeNotModified:
		return 0, interval, true
	case reconcile.OutcomeUnauthorized:
		return n, MaxSleep, false
	case reconcile.OutcomeDisabled, reconcile.OutcomeSkippedLocked:
		return n, interval, true
	}
	n++
	return n, min(interval<<min(n, maxExp), MaxSleep), true
}

// jitter draws U[0, MaxJitter) from int64n (the [0, n) source); nil — math/rand/v2.
func jitter(int64n func(n int64) int64) time.Duration {
	if int64n == nil {
		int64n = rand.Int64N
	}
	return time.Duration(int64n(int64(MaxJitter)))
}
