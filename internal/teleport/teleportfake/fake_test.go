package teleportfake_test

import (
	"context"
	"errors"
	"testing"

	"github.com/iamletenkov/stvor-agent/internal/teleport"
	"github.com/iamletenkov/stvor-agent/internal/teleport/teleportfake"
)

func TestDefaultSetManaged(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	want := teleport.Labels{Env: "prod", Space: "sp-1"}
	tests := []struct {
		name    string
		current teleport.Status
		result  string
	}{
		{"not joined", teleport.Status{}, teleport.ResultDeferred},
		{"new labels", teleport.Status{Joined: true, Active: true}, teleport.ResultApplied},
		{"same labels", teleport.Status{Managed: want, Joined: true, Active: true}, teleport.ResultUnchanged},
		{"same labels, pending", teleport.Status{Managed: want, Pending: true, Joined: true, Active: true}, teleport.ResultApplied},
		{"same labels, inactive", teleport.Status{Managed: want, Joined: true}, teleport.ResultApplied},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := &teleportfake.Fake{Current: tc.current}
			r, err := f.SetManaged(ctx, want)
			if err != nil || r.Result != tc.result {
				t.Fatalf("got %+v, %v; want %s", r, err, tc.result)
			}
			st, err := f.Status(ctx)
			if err != nil || st.Managed != want || st.Pending {
				t.Errorf("status после set: %+v, %v", st, err)
			}
			if got := f.SetCalls(); len(got) != 1 || got[0] != want {
				t.Errorf("SetCalls = %v", got)
			}
		})
	}
}

func TestOverrides(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := &teleportfake.Fake{
		StatusErr:  teleport.ErrLockBusy,
		SetErr:     teleport.ErrProtocol,
		ActionsErr: teleport.ErrActionsMissing,
	}
	if _, err := f.Status(ctx); !errors.Is(err, teleport.ErrLockBusy) {
		t.Errorf("Status: %v", err)
	}
	if _, err := f.SetManaged(ctx, teleport.Labels{Env: "a", Space: "b"}); !errors.Is(err, teleport.ErrProtocol) {
		t.Errorf("SetManaged: %v", err)
	}
	if err := f.CheckActions(ctx); !errors.Is(err, teleport.ErrActionsMissing) {
		t.Errorf("CheckActions: %v", err)
	}
	if f.StatusCalls() != 1 || len(f.SetCalls()) != 1 || f.ActionsCalls() != 1 {
		t.Errorf("счётчики: %d %d %d", f.StatusCalls(), len(f.SetCalls()), f.ActionsCalls())
	}

	f = &teleportfake.Fake{SetFunc: func(f *teleportfake.Fake, l teleport.Labels) (teleport.SetResult, error) {
		f.Current.Managed, f.Current.Pending = l, true
		return teleport.SetResult{Status: f.Current, Result: teleport.ResultUnconfirmed}, nil
	}}
	r, err := f.SetManaged(ctx, teleport.Labels{Env: "a", Space: "b"})
	if err != nil || r.Result != teleport.ResultUnconfirmed || !r.Pending {
		t.Errorf("SetFunc: %+v, %v", r, err)
	}
}
