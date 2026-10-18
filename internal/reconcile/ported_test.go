package reconcile

import (
	"strings"
	"testing"
	"time"
)

// Checks carried over from tests/test-labels.sh of 3.x that the other tests of
// this package did not cover yet (docs/test-map.md).

// test-labels.sh «пауза: попытка в журнале» (attempt 3) and «пауза: попытка 4
// в журнале»: every unconfirmed restart logs its number; the attempt after the
// 15-minute pause is the fourth.
func TestTickLogsAttemptNumber(t *testing.T) {
	t.Parallel()
	r := newRig()
	r.steady()
	r.tp.SetFunc = unconfirmable
	r.portal.answer200(labelsC, revC)
	at := func(sec int) { r.clock.now = t0.Add(time.Duration(sec) * time.Second) }

	for i, sec := range []int{0, 60, 120} {
		at(sec)
		r.tick(t, false)
		r.portal.answer304()
		if want := "attempt=" + string(rune('1'+i)); !strings.Contains(r.log.String(), want) {
			t.Fatalf("tick %d: journal lacks %q: %s", i+1, want, r.log.String())
		}
	}
	at(180)
	if res := r.tick(t, false); res.Apply != ApplyPaused {
		t.Fatalf("fourth tick apply = %q, want paused", res.Apply)
	}
	if strings.Contains(r.log.String(), "attempt=") {
		t.Fatalf("a skipped tick logged an attempt: %s", r.log.String())
	}
	at(1020)
	if res := r.tick(t, false); res.Apply != ApplyUnconfirmed {
		t.Fatalf("after the pause apply = %q", res.Apply)
	}
	if !strings.Contains(r.log.String(), "attempt=4") {
		t.Fatalf("journal lacks attempt=4: %s", r.log.String())
	}
}
