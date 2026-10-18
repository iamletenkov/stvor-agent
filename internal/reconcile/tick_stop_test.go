package reconcile

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/iamletenkov/stvor-agent/internal/portal"
	"github.com/iamletenkov/stvor-agent/internal/teleport"
	"github.com/iamletenkov/stvor-agent/internal/teleport/teleportfake"
)

// blockingPortal blocks Get until its ctx is done; started gets a value when
// Get begins.
type blockingPortal struct{ started chan struct{} }

func (p *blockingPortal) Get(ctx context.Context, _ portal.Request) (portal.Response, error) {
	p.started <- struct{}{}
	<-ctx.Done()
	return portal.Response{Outcome: portal.OutcomeNetwork, Err: ctx.Err()}, nil
}

// ctxTeleport records whether set-managed got a live context.
type ctxTeleport struct {
	*teleportfake.Fake
	setCtxErr chan error
}

func (c ctxTeleport) SetManaged(ctx context.Context, l teleport.Labels) (teleport.SetResult, error) {
	r, err := c.Fake.SetManaged(ctx, l)
	c.setCtxErr <- ctx.Err()
	return r, err
}

func closed() <-chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}

// A stop asked before set-managed: set-managed is not started, the portal
// answer is still remembered, the tick returns at once (spec §5.8 budget).
func TestTickStopBeforeSetManaged(t *testing.T) {
	t.Parallel()
	r := newRig()
	r.portal.answer200(labelsB, revB)

	res := r.tk.Tick(context.Background(), false, closed())

	if res.Apply != ApplyStopped || res.Outcome != OutcomeOK {
		t.Fatalf("result = %+v, want apply=%q", brief(res), ApplyStopped)
	}
	if n := len(r.tp.SetCalls()); n != 0 {
		t.Fatalf("set-managed вызван %d раз после просьбы остановиться", n)
	}
	if st := r.st(t); st.Expected == nil || st.Expected.Revision != revB || st.AppliedRevision != "" {
		t.Fatalf("state = %+v / %q: ответ портала запомнен, ревизия не отмечена", st.Expected, st.AppliedRevision)
	}
	if !strings.Contains(r.log.String(), "остановка") {
		t.Fatalf("нет строки об остановке: %s", r.log.String())
	}
}

// A stop during the portal poll cuts the poll (its context), not the tick's
// context; nothing is applied and the failure is not counted.
func TestTickStopCutsPortalPoll(t *testing.T) {
	t.Parallel()
	r := newRig()
	r.steady()
	bp := &blockingPortal{started: make(chan struct{}, 1)}
	r.tk.Portal = bp
	stop := make(chan struct{})
	done := make(chan Result, 1)
	go func() { done <- r.tk.Tick(context.Background(), false, stop) }()
	<-bp.started
	close(stop)
	var res Result
	select {
	case res = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("тик не вернулся после просьбы остановиться во время опроса портала")
	}
	if res.Outcome != OutcomeNetwork {
		t.Fatalf("outcome = %q", res.Outcome)
	}
	if n := len(r.tp.SetCalls()); n != 0 {
		t.Fatalf("set-managed вызван %d раз", n)
	}
	if st := r.st(t); st.Failures != 0 || st.LastPoll != nil {
		t.Fatalf("прерванный остановкой опрос записан как отказ: failures=%d last_poll=%+v", st.Failures, st.LastPoll)
	}
}

// A stop during set-managed waits for it: its context is not cancelled, the
// tick finishes it and returns its result.
func TestTickStopDuringSetManagedWaits(t *testing.T) {
	t.Parallel()
	r := newRig()
	r.portal.answer200(labelsB, revB)
	stop := make(chan struct{})
	inSet := make(chan struct{})
	release := make(chan struct{})
	r.tp.SetFunc = func(f *teleportfake.Fake, l teleport.Labels) (teleport.SetResult, error) {
		close(inSet)
		<-release
		f.Current.Managed = l
		return teleport.SetResult{Status: f.Current, Result: teleport.ResultApplied}, nil
	}
	ct := ctxTeleport{Fake: r.tp, setCtxErr: make(chan error, 1)}
	r.tk.Teleport = ct
	done := make(chan Result, 1)
	go func() { done <- r.tk.Tick(context.Background(), false, stop) }()
	<-inSet
	close(stop)
	select {
	case <-done:
		t.Fatal("тик вернулся, не дождавшись set-managed")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	res := <-done
	if err := <-ct.setCtxErr; err != nil {
		t.Fatalf("контекст set-managed отменён остановкой: %v", err)
	}
	if res.Apply != ApplyApplied {
		t.Fatalf("apply = %q, want %q", res.Apply, ApplyApplied)
	}
	if n := len(r.tp.SetCalls()); n != 1 {
		t.Fatalf("set-managed вызван %d раз", n)
	}
}
