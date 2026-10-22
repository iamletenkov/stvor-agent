package teleport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"syscall"
	"time"
)

// Defaults of the cream adapter (spec §5.7).
const (
	DefaultCreamPath = "/usr/bin/cream"
	// DefaultExtension is the cream extension on Debian/Ubuntu; OpenWrt has
	// teleport-openwrt (spec OpenWrt R11).
	DefaultExtension = "teleport"
	// DefaultStatusTimeout bounds managed-status (lock: none, no side effects).
	DefaultStatusTimeout = 15 * time.Second
	// DefaultSetTimeout bounds set-managed: lock wait ≤ 20 s + confirmed
	// restart ≤ 30 s + margin.
	DefaultSetTimeout = 90 * time.Second
	// DefaultActionsTimeout bounds `cream actions teleport --json`.
	DefaultActionsTimeout = 15 * time.Second
	// DefaultKillGrace is the SIGTERM→SIGKILL interval on timeout. cream runs
	// the action in a process group of its own and, on SIGTERM, gives it
	// 10 s between its own SIGTERM and SIGKILL (cream internal/runner,
	// DefaultGrace); killing cream sooner orphans the action.
	DefaultKillGrace = 15 * time.Second
	// setLockTimeout is the --lock-timeout of set-managed.
	setLockTimeout = "20s"
	// pipeDelay is how long Wait waits for stdout after cream exited leaving
	// a descendant with the pipe open.
	pipeDelay = 5 * time.Second
)

// creamEnv is the whole environment of cream: nothing of the agent's own
// environment (the token among it) reaches the child.
func creamEnv() []string {
	return []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C.UTF-8"}
}

// Error classes of the adapter on top of the cream ones in teleport.go. Match
// with errors.Is.
var (
	// ErrInvalidInput — exit 2 of the action (it rejected its arguments) or
	// labels that cannot be T labels, refused before cream is started.
	ErrInvalidInput = errors.New("teleport: действие отвергло ввод")
	// ErrActionFailed — any other exit 1–63 the action does not define as a
	// result (1 of managed-status, 3–63 of both).
	ErrActionFailed = errors.New("teleport: действие завершилось ошибкой")
	// ErrTimeout — the call outlived its timeout; the process group was killed.
	// It also wraps context.DeadlineExceeded.
	ErrTimeout = errors.New("teleport: тайм-аут cream")
)

// CreamTeleport implements Teleport by running cream (spec §5.7):
//
//	cream run teleport managed-status
//	cream run --lock-timeout 20s teleport set-managed env=<v> space=<v>
//	cream actions teleport --json
//
// with Extension in place of teleport (teleport-openwrt on OpenWrt).
// argv as a list, no shell, the environment exactly creamEnv(), stdin /dev/null,
// cwd "/", its own process group. On timeout the group gets SIGTERM — cream
// then stops the action, whose group is its own — and SIGKILL after KillGrace. Both actions are
// `output: json` and are called without --json, so stdout is the action's
// object as is (cream §7.2). Zero durations take the defaults.
type CreamTeleport struct {
	Path string
	// Extension is the name of the extension in the catalog ("" —
	// DefaultExtension).
	Extension      string
	StatusTimeout  time.Duration
	SetTimeout     time.Duration
	ActionsTimeout time.Duration
	// KillGrace is the SIGTERM→SIGKILL interval on timeout (zero — DefaultKillGrace).
	KillGrace time.Duration
	// Stderr receives the stderr of cream (its messages and the action's log);
	// nil discards it.
	Stderr io.Writer
}

// NewCream returns the adapter for the cream binary at path ("" — /usr/bin/cream).
func NewCream(path string) *CreamTeleport {
	if path == "" {
		path = DefaultCreamPath
	}
	return &CreamTeleport{
		Path:           path,
		Extension:      DefaultExtension,
		StatusTimeout:  DefaultStatusTimeout,
		SetTimeout:     DefaultSetTimeout,
		ActionsTimeout: DefaultActionsTimeout,
		KillGrace:      DefaultKillGrace,
	}
}

var _ Teleport = (*CreamTeleport)(nil)

// Status runs managed-status. It follows ctx: the action has no side effects.
func (c *CreamTeleport) Status(ctx context.Context) (Status, error) {
	code, out, err := c.run(ctx, orDefault(c.StatusTimeout, DefaultStatusTimeout),
		"run", c.extension(), "managed-status")
	if err != nil {
		return Status{}, fmt.Errorf("managed-status: %w", err)
	}
	if code != 0 {
		return Status{}, fmt.Errorf("managed-status: %w", codeError(code, out))
	}
	r, err := parseOutput(out, false)
	if err != nil {
		return Status{}, fmt.Errorf("managed-status: %w", err)
	}
	return r.Status, nil
}

// SetManaged runs set-managed with exactly env and space. Once started it is
// not cut by ctx — only by its own timeout: the service waits for the child
// cream run on stop (spec §5.8), killing it mid-restart helps nobody.
// unconfirmed (exit 1) is a result, not an error.
func (c *CreamTeleport) SetManaged(ctx context.Context, l Labels) (SetResult, error) {
	if !reValue.MatchString(l.Env) || !reValue.MatchString(l.Space) {
		return SetResult{}, fmt.Errorf("set-managed: %w: env или space вне алфавита меток", ErrInvalidInput)
	}
	code, out, err := c.run(context.WithoutCancel(ctx), orDefault(c.SetTimeout, DefaultSetTimeout),
		"run", "--lock-timeout", setLockTimeout, c.extension(), "set-managed",
		"env="+l.Env, "space="+l.Space)
	if err != nil {
		return SetResult{}, fmt.Errorf("set-managed: %w", err)
	}
	if code != 0 && code != 1 {
		return SetResult{}, fmt.Errorf("set-managed: %w", codeError(code, out))
	}
	r, err := parseOutput(out, true)
	if err != nil {
		return SetResult{}, fmt.Errorf("set-managed: %w", err)
	}
	if (code == 1) != (r.Result == ResultUnconfirmed) {
		return SetResult{}, fmt.Errorf("set-managed: %w: result %s при коде %d", ErrBadOutput, r.Result, code)
	}
	return r, nil
}

// CheckActions runs `cream actions teleport --json`. It is soft (spec §5.7,
// plan ruling 7): ErrActionsMissing only when the list was read and an action
// is absent; a non-zero exit or an unreadable list is another error ("unknown":
// log it, keep applying).
func (c *CreamTeleport) CheckActions(ctx context.Context) error {
	code, out, err := c.run(ctx, orDefault(c.ActionsTimeout, DefaultActionsTimeout),
		"actions", c.extension(), "--json")
	if err != nil {
		return fmt.Errorf("cream actions: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("cream actions: код %d", code)
	}
	names, err := parseActions(out)
	if err != nil {
		return fmt.Errorf("cream actions: %w", err)
	}
	var missing []string
	for _, a := range []string{"managed-status", "set-managed"} {
		if !names[a] {
			missing = append(missing, a)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: %v", ErrActionsMissing, missing)
	}
	return nil
}

// codeError classifies an exit code other than the results (spec cream §11.3).
func codeError(code int, out []byte) error {
	switch {
	case code == 2:
		if !jsonObject(out) {
			return fmt.Errorf("%w (код 2, вывод не JSON-объект)", ErrInvalidInput)
		}
		return fmt.Errorf("%w (код 2)", ErrInvalidInput)
	case code == 75:
		return fmt.Errorf("%w (код 75)", ErrLockBusy)
	case code == 76:
		return fmt.Errorf("%w (код 76)", ErrProtocol)
	case code >= 1 && code <= 63:
		return fmt.Errorf("%w (код %d)", ErrActionFailed, code)
	default: // 64–79 and anything cream does not define
		return fmt.Errorf("%w (код %d)", ErrCream, code)
	}
}

// run starts cream with args and returns its exit code and stdout. err is set
// only when no exit code is there to classify: cream did not start, was killed
// on timeout or by a signal, or stdout outgrew MaxOutput.
func (c *CreamTeleport) run(ctx context.Context, timeout time.Duration, args ...string) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	path := c.Path
	if path == "" {
		path = DefaultCreamPath
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = creamEnv()
	cmd.Dir = "/"
	cmd.Stdin = nil // /dev/null
	stdout := &capped{max: MaxOutput}
	cmd.Stdout = stdout
	cmd.Stderr = io.Discard
	if c.Stderr != nil {
		cmd.Stderr = c.Stderr
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	grace := orDefault(c.KillGrace, DefaultKillGrace)
	var killTimer *time.Timer
	cmd.Cancel = func() error {
		// SIGTERM, not SIGKILL: the action sits in a group of its own that
		// only cream knows and kills on SIGTERM. SIGKILL of the group of cream
		// after grace is the backstop for a cream that does not stop.
		pgid := cmd.Process.Pid
		killTimer = time.AfterFunc(grace, func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) })
		return syscall.Kill(-pgid, syscall.SIGTERM)
	}
	// WaitDelay bounds both waits of Wait: cream after Cancel (it must outlast
	// grace, or Wait's own SIGKILL of cream would come first) and stdout held
	// open by a descendant after cream exited.
	cmd.WaitDelay = grace + pipeDelay

	err := cmd.Run()
	if killTimer != nil {
		killTimer.Stop()
		// Leftovers in the group of cream that outlived it.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			return 0, nil, fmt.Errorf("%w %s: %w", ErrTimeout, timeout, ctxErr)
		}
		return 0, nil, ctxErr
	}
	var exitErr *exec.ExitError
	switch {
	case errors.Is(err, exec.ErrWaitDelay):
		// cream exited, a descendant kept stdout open past waitDelay: the
		// exit code stands.
	case errors.As(err, &exitErr):
		if exitErr.ExitCode() < 0 {
			return 0, nil, fmt.Errorf("%w: cream убит сигналом", ErrCream)
		}
	case err != nil:
		return 0, nil, fmt.Errorf("%w: запуск %s: %w", ErrCream, path, err)
	}
	if stdout.over {
		return 0, nil, bad("stdout длиннее %d байт", MaxOutput)
	}
	return cmd.ProcessState.ExitCode(), stdout.buf, nil
}

func (c *CreamTeleport) extension() string {
	if c.Extension == "" {
		return DefaultExtension
	}
	return c.Extension
}

func orDefault(d, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d
}

// capped keeps the first max bytes and notes that there was more. It accepts
// every write, so the child is never blocked or broken by a full pipe.
type capped struct {
	buf  []byte
	max  int
	over bool
}

func (w *capped) Write(p []byte) (int, error) {
	if room := w.max - len(w.buf); room < len(p) {
		w.over = true
		if room > 0 {
			w.buf = append(w.buf, p[:room]...)
		}
		return len(p), nil
	}
	w.buf = append(w.buf, p...)
	return len(p), nil
}
