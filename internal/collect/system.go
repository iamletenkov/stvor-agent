package collect

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"syscall"
	"time"
)

// Defaults of the systemctl adapter.
const (
	DefaultSystemctl        = "/usr/bin/systemctl"
	DefaultSystemctlTimeout = 5 * time.Second
	// maxFailedUnits bounds the list kept for the portal snapshot; the
	// metric still counts every failed unit. Both limits are the portal
	// contract v1 (failed_units_max, unit names ≤ 128).
	maxFailedUnits = 32
	maxUnitName    = 128
)

// reUnit is a systemd unit name with its type suffix (escapes like \x2d
// included); anything else in systemctl's output is not a unit.
var reUnit = regexp.MustCompile(
	`^[A-Za-z0-9:_.\\@-]+\.(service|socket|device|mount|automount|swap|target|path|timer|slice|scope)$`)

// System collects uptime, boot_id, clock synchronisation, failed systemd
// units and the reboot-required flag (the "slow" group).
func (c Collector) System(ctx context.Context, s *Snapshot) error {
	return errors.Join(c.uptime(s), c.bootID(s), c.clock(s), c.failedUnits(ctx, s), c.rebootRequired(s))
}

func (c Collector) uptime(s *Snapshot) error {
	data, absent, err := c.readFile("proc/uptime")
	if absent || err != nil {
		return err
	}
	f, _ := nextField(data)
	up, ok := parseNonNegFloat(f)
	if !ok {
		return fmt.Errorf("/proc/uptime: %w", errMalformed)
	}
	s.add(MetricUptime, Gauge, up)
	return nil
}

// bootID reads the kernel's boot UUID, validated and lower-cased: it goes to
// the portal as is.
func (c Collector) bootID(s *Snapshot) error {
	data, absent, err := c.readFile("proc/sys/kernel/random/boot_id")
	if absent || err != nil {
		return err
	}
	id, _ := nextField(data)
	if !isUUID(id) {
		return fmt.Errorf("/proc/sys/kernel/random/boot_id: %w", errMalformed)
	}
	s.BootID = string(bytes.ToLower(id))
	return nil
}

func isUUID(b []byte) bool {
	if len(b) != 36 {
		return false
	}
	for i, c := range b {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !isHex(c) {
				return false
			}
		}
	}
	return true
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func (c Collector) clock(s *Snapshot) error {
	if c.Clock == nil {
		return nil
	}
	st, err := c.Clock()
	if errors.Is(err, ErrAbsent) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("состояние часов: %w", err)
	}
	s.addCap(CapClock)
	s.ClockKnown, s.ClockSynced = true, st.Synced
	if st.Synced {
		s.add(MetricClockSynced, Gauge, 1)
		s.add(MetricClockOffset, Gauge, st.Offset.Seconds())
	} else {
		s.add(MetricClockSynced, Gauge, 0)
	}
	return nil
}

func (c Collector) failedUnits(ctx context.Context, s *Snapshot) error {
	if c.FailedUnits == nil {
		return nil
	}
	names, err := c.FailedUnits(ctx)
	if errors.Is(err, ErrAbsent) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("упавшие юниты: %w", err)
	}
	if names == nil {
		names = []string{}
	}
	s.addCap(CapSystemd)
	s.FailedUnits = names[:min(len(names), maxFailedUnits):min(len(names), maxFailedUnits)]
	s.add(MetricUnitsFailed, Gauge, float64(len(names)))
	return nil
}

// rebootRequired reports the Debian/Ubuntu marker /var/run/reboot-required:
// a flag file whose absence means "not required".
func (c Collector) rebootRequired(s *Snapshot) error {
	_, absent, err := c.readFile("var/run/reboot-required")
	if err != nil {
		return err
	}
	v := 1.0
	if absent {
		v = 0
	}
	s.add(MetricRebootRequired, Gauge, v)
	return nil
}

// Adjtimex reads the kernel clock state with adjtimex(2) in read-only mode
// (modes 0: no privileges needed).
//
// The offset is the kernel PLL's remaining offset (timex.offset). chrony
// disciplines the clock itself and leaves the kernel PLL idle, so under
// chrony system.clock.offset is ~0 whatever the real error is; only
// ntpd/systemd-timesyncd in PLL mode fill it. The synced flag is reliable
// with all of them.
//
// For the unit file (a later task): ProtectClock=yes and a seccomp filter
// without @clock (SystemCallFilter=~@clock) deny adjtimex(2) even in
// read-only mode — EPERM, and the clock capability disappears. Keep
// adjtimex allowed when hardening stvor-agent.service.
func Adjtimex() (ClockState, error) {
	var tx syscall.Timex
	state, err := syscall.Adjtimex(&tx)
	if err != nil {
		return ClockState{}, fmt.Errorf("adjtimex: %w", err)
	}
	return clockFromTimex(state, tx.Status, int64(tx.Offset)), nil //nolint:unconvert // Offset is not int64 on every GOARCH
}

// clockFromTimex: the clock is synchronised unless the kernel says
// TIME_ERROR or STA_UNSYNC; the offset is in µs, or ns with STA_NANO.
func clockFromTimex(state int, status int32, offset int64) ClockState {
	const (
		timeError = 5
		staUnsync = 0x0040
		staNano   = 0x2000
	)
	unit := time.Microsecond
	if status&staNano != 0 {
		unit = time.Nanosecond
	}
	st := ClockState{Synced: state != timeError && status&staUnsync == 0}
	if status&staUnsync == 0 {
		st.Offset = time.Duration(offset) * unit
	}
	return st
}

// Systemctl lists failed units with `systemctl list-units --state=failed`:
// argv as a list, no shell, a minimal environment, bounded by Timeout.
type Systemctl struct {
	Path    string        // "" — DefaultSystemctl
	Timeout time.Duration // zero — DefaultSystemctlTimeout
}

// FailedUnits returns the names of failed units, an empty list when none failed, ErrAbsent when systemctl is not installed.
func (sc Systemctl) FailedUnits(ctx context.Context) ([]string, error) {
	path := sc.Path
	if path == "" {
		path = DefaultSystemctl
	}
	timeout := sc.Timeout
	if timeout <= 0 {
		timeout = DefaultSystemctlTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "list-units", "--state=failed", "--plain", "--no-legend", "--no-pager")
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "SYSTEMD_COLORS=0"}
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, syscall.ENOENT) {
		return nil, ErrAbsent
	}
	if err != nil {
		return nil, fmt.Errorf("systemctl list-units: %w", err)
	}
	return parseFailedUnits(out), nil
}

// parseFailedUnits takes the first field of each line of `--plain
// --no-legend` output (a leading "●" is skipped) and keeps valid unit names;
// no output — an empty, non-nil list.
func parseFailedUnits(out []byte) []string {
	names := []string{}
	for line := range bytes.Lines(out) {
		f, rest := nextField(line)
		if string(f) == "●" || string(f) == "*" {
			f, _ = nextField(rest)
		}
		if len(f) == 0 || len(f) > maxUnitName || !reUnit.Match(f) {
			continue
		}
		names = append(names, string(f))
	}
	return names
}
