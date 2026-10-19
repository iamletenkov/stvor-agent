package daemon

import (
	"testing"
	"time"

	"github.com/iamletenkov/stvor-agent/internal/reconcile"
)

func TestNext(t *testing.T) {
	t.Parallel()
	const m = time.Minute
	tests := []struct {
		name       string
		interval   time.Duration
		n          int
		outcome    reconcile.Outcome
		wantN      int
		wantSleep  time.Duration
		wantJitter bool
	}{
		{"200 resets", m, 5, reconcile.OutcomeOK, 0, m, true},
		{"304 resets", m, 2, reconcile.OutcomeNotModified, 0, m, true},
		{"first network failure", m, 0, reconcile.OutcomeNetwork, 1, 2 * m, true},
		{"second failure", m, 1, reconcile.OutcomeHTTPError, 2, 4 * m, true},
		{"third failure", m, 2, reconcile.OutcomeRateLimited, 3, 8 * m, true},
		{"exponent capped at 3", m, 7, reconcile.OutcomeBadBody, 8, 8 * m, true},
		{"redirect counts", m, 0, reconcile.OutcomeRedirect, 1, 2 * m, true},
		{"600 s cap", 5 * m, 0, reconcile.OutcomeNetwork, 1, 10 * m, true},
		{"600 s cap at max interval", 10 * m, 3, reconcile.OutcomeNetwork, 4, 10 * m, true},
		{"unknown outcome is a failure", m, 0, reconcile.Outcome("other"), 1, 2 * m, true},
		{"401 sleeps 600 s flat", m, 2, reconcile.OutcomeUnauthorized, 2, 10 * m, false},
		{"disabled keeps n", m, 2, reconcile.OutcomeDisabled, 2, m, true},
		{"skipped_locked keeps n", m, 2, reconcile.OutcomeSkippedLocked, 2, m, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			n, d, j := Next(tc.interval, tc.n, tc.outcome)
			if n != tc.wantN || d != tc.wantSleep || j != tc.wantJitter {
				t.Fatalf("Next(%v, %d, %q) = %d, %v, %v; want %d, %v, %v",
					tc.interval, tc.n, tc.outcome, n, d, j, tc.wantN, tc.wantSleep, tc.wantJitter)
			}
		})
	}
}

func TestJitter(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		pick func(n int64) int64
		want time.Duration
	}{
		{"low edge", func(int64) int64 { return 0 }, 0},
		{"high edge", func(n int64) int64 { return n - 1 }, MaxJitter - 1},
		{"middle", func(n int64) int64 { return n / 2 }, MaxJitter / 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var gotN int64
			got := jitter(func(n int64) int64 { gotN = n; return tc.pick(n) })
			if gotN != int64(MaxJitter) {
				t.Fatalf("random source asked for [0, %d), want [0, %d)", gotN, int64(MaxJitter))
			}
			if got != tc.want {
				t.Fatalf("jitter = %v, want %v", got, tc.want)
			}
		})
	}
	// The default source stays in [0, 20 s).
	for range 1000 {
		if d := jitter(nil); d < 0 || d >= MaxJitter {
			t.Fatalf("default jitter %v outside [0, %v)", d, MaxJitter)
		}
	}
}
