package reconcile

import "testing"

// The outcome and apply strings are a contract: they go to state.json and to
// `status --json` (spec §5.5, §7), so a rename must break this test.
func TestOutcomeStrings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		got  Outcome
		want string
	}{
		{OutcomeOK, "ok"},
		{OutcomeNotModified, "not_modified"},
		{OutcomeUnauthorized, "unauthorized"},
		{OutcomeRateLimited, "rate_limited"},
		{OutcomeNetwork, "network"},
		{OutcomeRedirect, "redirect"},
		{OutcomeHTTPError, "http_error"},
		{OutcomeBadBody, "bad_body"},
		{OutcomeSkippedLocked, "skipped_locked"},
		{OutcomeDisabled, "disabled"},
	}
	for _, tc := range tests {
		if string(tc.got) != tc.want {
			t.Errorf("outcome = %q, want %q", tc.got, tc.want)
		}
	}
}

func TestApplyStrings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		got  string
		want string
	}{
		{ApplyNone, ""},
		{ApplyApplied, "applied"},
		{ApplyUnchanged, "unchanged"},
		{ApplyDeferred, "deferred"},
		{ApplyUnconfirmed, "unconfirmed"},
		{ApplyPaused, "paused"},
		{ApplyError, "error"},
	}
	for _, tc := range tests {
		if tc.got != tc.want {
			t.Errorf("apply = %q, want %q", tc.got, tc.want)
		}
	}
}
