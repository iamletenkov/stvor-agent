package initsys

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/iamletenkov/stvor-agent/internal/platform"
)

// DefaultInitDir is where procd init scripts live.
const DefaultInitDir = "/etc/init.d"

// reService is a service name an init script may have: no path, no flag.
var reService = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)

// Procd runs the procd init script /etc/init.d/<service> (OpenWrt): the unit
// name loses its .service suffix. Same hygiene as Systemd: argv as a list, no
// shell of ours, the environment exactly PATH and LANG, stdin /dev/null.
type Procd struct {
	Dir     string        // "" — DefaultInitDir
	Timeout time.Duration // zero — DefaultTimeout
	// Stderr receives the stderr of the script; nil discards it.
	Stderr io.Writer
}

var _ Manager = Procd{}

// Enable runs `<script> enable` (the rc.d link: start at boot).
func (p Procd) Enable(ctx context.Context, unit string) error {
	return p.run(ctx, unit, "enable")
}

// Restart runs `<script> restart`, or `<script> start` when `running` says
// the service is not running: rc.common restart is stop + start, and the stop
// of a service procd does not know yet (the first install) prints
// "Command failed: ubus call service delete … (Not found)". An unknown state
// keeps the plain restart; every failure of the chosen verb is reported.
func (p Procd) Restart(ctx context.Context, unit string) error {
	if p.IsActive(ctx, unit) == "inactive" {
		return p.run(ctx, unit, "start")
	}
	return p.run(ctx, unit, "restart")
}

// DisplayName implements Manager: "<service> (procd)".
func (p Procd) DisplayName(unit string) string {
	return strings.TrimSuffix(unit, ".service") + " (procd)"
}

// DisableNow runs `<script> stop`, then `<script> disable` even when the stop
// failed (rc.common has no --now).
func (p Procd) DisableNow(ctx context.Context, unit string) error {
	stopErr := p.run(ctx, unit, "stop")
	disableErr := p.run(ctx, unit, "disable")
	return errors.Join(stopErr, disableErr)
}

// Start runs `<script> start`.
func (p Procd) Start(ctx context.Context, unit string) error {
	return p.run(ctx, unit, "start")
}

// Stop runs `<script> stop`.
func (p Procd) Stop(ctx context.Context, unit string) error {
	return p.run(ctx, unit, "stop")
}

// IsActive runs `<script> running`: exit 0 — "active", another exit code —
// "inactive", no answer (no script, a bad name, a timeout) — "unknown".
func (p Procd) IsActive(ctx context.Context, unit string) string {
	err := p.run(ctx, unit, "running")
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return "active"
	case errors.As(err, &exitErr) && exitErr.ExitCode() > 0:
		return "inactive"
	default:
		return stateUnknown
	}
}

func (p Procd) run(ctx context.Context, unit, verb string) error {
	svc := strings.TrimSuffix(unit, ".service")
	if !reService.MatchString(svc) {
		return fmt.Errorf("procd: имя службы %q не годится", unit)
	}
	dir := p.Dir
	if dir == "" {
		dir = DefaultInitDir
	}
	path := filepath.Join(dir, svc)
	if _, err := run(ctx, path, p.Timeout, p.Stderr, verb); err != nil {
		return fmt.Errorf("%s %s: %w", path, verb, err)
	}
	return nil
}

// For returns the init system of the family: procd on OpenWrt, systemd
// otherwise. stderr receives what the tools print there.
func For(f platform.Family, stderr io.Writer) Manager {
	if f == platform.OpenWrt {
		return Procd{Stderr: stderr}
	}
	return Systemd{Stderr: stderr}
}
