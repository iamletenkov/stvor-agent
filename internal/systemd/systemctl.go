// Package systemd is the agent's view of systemctl: enable, restart and
// disable --now of its own unit, start/stop of the telemetry unit, and is-active
// for `status` (spec §8).
package systemd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// Defaults of the exec adapter.
const (
	DefaultPath = "/usr/bin/systemctl"
	// DefaultTimeout bounds one systemctl call: a restart waits for the
	// running tick of the service (TimeoutStopSec=120) plus the start.
	DefaultTimeout = 150 * time.Second
	// stateUnknown is what IsActive reports when systemctl gave no answer.
	stateUnknown = "unknown"
)

// reState is the shape of an `is-active` answer (active, inactive, failed,
// activating, …).
var reState = regexp.MustCompile(`^[a-z][a-z-]{0,31}$`)

// Systemctl is what the CLI needs from systemd.
type Systemctl interface {
	Enable(ctx context.Context, unit string) error
	Restart(ctx context.Context, unit string) error
	DisableNow(ctx context.Context, unit string) error
	// Start and Stop run `systemctl start|stop <unit>` (the telemetry
	// service: started by `run` once its token is there, stopped by `disable`).
	Start(ctx context.Context, unit string) error
	Stop(ctx context.Context, unit string) error
	// IsActive is the answer of `systemctl is-active` ("active",
	// "inactive", "failed", …) or "unknown" when there is none.
	IsActive(ctx context.Context, unit string) string
}

// Exec runs systemctl at Path ("" — DefaultPath): argv as a list, no shell,
// the environment exactly PATH and LANG (nothing of the agent's own
// environment reaches the child), stdin /dev/null.
type Exec struct {
	Path    string
	Timeout time.Duration // zero — DefaultTimeout
	// Stderr receives the stderr of systemctl; nil discards it.
	Stderr io.Writer
}

var _ Systemctl = Exec{}

// Enable runs `systemctl enable <unit>`.
func (e Exec) Enable(ctx context.Context, unit string) error {
	_, err := e.run(ctx, "enable", unit)
	return err
}

// Restart runs `systemctl restart <unit>`.
func (e Exec) Restart(ctx context.Context, unit string) error {
	_, err := e.run(ctx, "restart", unit)
	return err
}

// DisableNow runs `systemctl disable --now <unit>`.
func (e Exec) DisableNow(ctx context.Context, unit string) error {
	_, err := e.run(ctx, "disable", "--now", unit)
	return err
}

// Start runs `systemctl start <unit>`.
func (e Exec) Start(ctx context.Context, unit string) error {
	_, err := e.run(ctx, "start", unit)
	return err
}

// Stop runs `systemctl stop <unit>`.
func (e Exec) Stop(ctx context.Context, unit string) error {
	_, err := e.run(ctx, "stop", unit)
	return err
}

// IsActive runs `systemctl is-active <unit>`; its exit code (3 for an
// inactive unit) is not an error, the printed state is the answer.
func (e Exec) IsActive(ctx context.Context, unit string) string {
	out, _ := e.run(ctx, "is-active", unit)
	if s := strings.TrimSpace(out); reState.MatchString(s) {
		return s
	}
	return stateUnknown
}

func (e Exec) run(ctx context.Context, args ...string) (string, error) {
	path := e.Path
	if path == "" {
		path = DefaultPath
	}
	timeout := e.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C.UTF-8"}
	cmd.Dir = "/"
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = e.Stderr
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("systemctl %s: %w", strings.Join(args, " "), err)
	}
	return out.String(), nil
}
