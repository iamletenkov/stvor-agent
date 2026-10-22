package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/iamletenkov/stvor-agent/internal/daemon"
	"github.com/iamletenkov/stvor-agent/internal/reconcile"
	"github.com/iamletenkov/stvor-agent/internal/state"
	"github.com/iamletenkov/stvor-agent/internal/teleport"
	"github.com/iamletenkov/stvor-agent/internal/token"
	"github.com/iamletenkov/stvor-agent/internal/version"
)

// maxSecret bounds what `enable` reads from stdin.
const maxSecret = 4096

// runService is `stvor-agent run`, the ExecStart of the service: the loop
// until SIGTERM/SIGINT, then exit 0. Signals go into a buffered channel, not
// signal.NotifyContext: a stop must not cut the running tick (spec §5.8).
func (a *App) runService(stderr io.Writer) int {
	cfg, problems := daemon.LoadConfig(a.Getenv)
	log := newLogger(stderr, cfg.LogLevel)
	for _, p := range problems {
		log.Warn(p, "event", "config")
	}
	sigs := make(chan os.Signal, 4)
	a.Notify(sigs, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGINT)
	defer a.StopNotify(sigs)

	log.Info("служба запущена", "event", "start", "version", version.Short(a.Version),
		"interval", cfg.Interval)
	if err := a.ensureMachineID(); err != nil {
		log.Warn("machine-id агента не создан", "event", "start", "path", a.MachineIDPath, "err", err)
	}
	d := &daemon.Daemon{Ticker: a.ticker(log), Lock: a.Lock, Interval: cfg.Interval, Log: log}
	d.Run(context.Background(), sigs)
	return exitOK
}

// runEnable is `stvor-agent enable URL=<url>`: the token from stdin goes to
// the token file (atomically, 0600), a state of another portal is dropped,
// the service is enabled and restarted, then one sync runs at once. The token
// is never printed, put into argv or the environment.
func (a *App) runEnable(url string, stdin io.Reader, stdout, stderr io.Writer) int {
	if !token.ValidURL(url) {
		_, _ = fmt.Fprintf(stderr, "stvor-agent: enable: URL=%q не по форме https://<хост>[:порт]/robot-api/v1/labels\n", url)
		return exitError
	}
	secret, err := readSecret(stdin, a.IsTerminal != nil && a.IsTerminal(stdin))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "stvor-agent: enable: токен со stdin: %v\n", err)
		return exitError
	}

	ctx := context.Background()
	unlock, err := a.Lock.Lock(ctx, daemon.CLILockWait)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "stvor-agent: enable: %v\n", err)
		return exitError
	}
	reset, resetErr := a.resetStateFor(url)
	err = token.Write(a.FS, a.TokenPath, token.Token{URL: url, Token: secret})
	unlock()
	if resetErr != nil {
		_, _ = fmt.Fprintf(stderr, "stvor-agent: enable: state.json не сброшен: %v\n", resetErr)
		return exitError
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "stvor-agent: enable: %v\n", err)
		return exitError
	}
	if reset {
		_, _ = fmt.Fprintln(stdout, "адрес портала другой — прежнее состояние сброшено")
	}

	for _, step := range []func(context.Context, string) error{a.Init.Enable, a.Init.Restart} {
		if err := step(ctx, Unit); err != nil {
			_, _ = fmt.Fprintf(stderr, "stvor-agent: enable: токен записан, но служба не запущена: %v\n", err)
			return exitError
		}
	}
	_, _ = fmt.Fprintf(stdout, "токен записан в %s, служба %s включена\n", a.TokenPath, Unit)
	return a.runSync(stdout, stderr)
}

// readSecret reads the token from stdin: one line, spaces around it trimmed,
// 43 chars [A-Za-z0-9_-]. From a terminal (tty) it stops at the first newline
// — Enter is enough; from a pipe or file it reads to EOF and refuses a second
// line. Errors never quote the input.
func readSecret(stdin io.Reader, tty bool) (string, error) {
	var data []byte
	var err error
	if tty {
		data, err = readLine(stdin, maxSecret+1)
	} else {
		data, err = io.ReadAll(io.LimitReader(stdin, maxSecret+1))
	}
	if err != nil {
		return "", fmt.Errorf("не прочитан: %w", err)
	}
	if len(data) > maxSecret {
		return "", fmt.Errorf("больше %d байт — нужна одна строка из 43 знаков", maxSecret)
	}
	s := strings.TrimSuffix(string(data), "\n")
	if strings.Contains(s, "\n") {
		return "", errors.New("нужна одна строка")
	}
	s = strings.TrimSpace(s)
	switch {
	case s == "":
		return "", errors.New("stdin пуст")
	case !token.ValidToken(s):
		return "", errors.New("токен не по форме (43 знака [A-Za-z0-9_-])")
	}
	return s, nil
}

// readLine reads up to and including the first newline, at most limit bytes,
// one byte at a time: nothing past the line is consumed.
func readLine(r io.Reader, limit int) ([]byte, error) {
	var line []byte
	b := make([]byte, 1)
	for len(line) < limit {
		n, err := r.Read(b)
		if n == 1 {
			line = append(line, b[0])
			if b[0] == '\n' {
				return line, nil
			}
		}
		if errors.Is(err, io.EOF) {
			return line, nil
		}
		if err != nil {
			return nil, err
		}
	}
	return line, nil
}

// resetStateFor removes state.json when it belongs to another portal address
// or is refused anyway; true — it was removed.
func (a *App) resetStateFor(url string) (bool, error) {
	st, err := state.Load(a.FS, a.StatePath)
	switch {
	case errors.Is(err, state.ErrAbsent):
		return false, nil
	case err == nil && st.URL == url:
		return false, nil
	}
	if err := a.FS.Remove(a.StatePath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	return true, nil
}

// runDisable is `stvor-agent disable`: stop and disable the service, stop
// the telemetry service, remove the token, state.json and the telemetry token. Teleport is not called: the managed env/space
// stay (the node does not move silently). The files go even when systemctl
// fails — the code is 1 then.
func (a *App) runDisable(stdout, stderr io.Writer) int {
	ctx := context.Background()
	code := exitOK
	if err := a.Init.DisableNow(ctx, Unit); err != nil {
		_, _ = fmt.Fprintf(stderr, "stvor-agent: disable: служба не остановлена: %v\n", err)
		code = exitError
	}
	if err := a.Init.Stop(ctx, reconcile.TelemetryUnit); err != nil {
		_, _ = fmt.Fprintf(stderr, "stvor-agent: disable: служба телеметрии не остановлена: %v\n", err)
		code = exitError
	}
	unlock, err := a.Lock.Lock(ctx, daemon.CLILockWait)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "stvor-agent: disable: %v\n", err)
		return exitError
	}
	defer unlock()
	// The telemetry token and its fingerprint go too: the next enable asks
	// for a new one (the unit stays enabled and waits for the token).
	telSource := filepath.Join(filepath.Dir(a.StatePath), filepath.Base(reconcile.TelemetrySourcePath))
	for _, path := range []string{a.TokenPath, a.StatePath, a.TelemetryTokenPath, telSource} {
		if path == "" {
			continue
		}
		if err := a.FS.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			_, _ = fmt.Fprintf(stderr, "stvor-agent: disable: %v\n", err)
			code = exitError
		}
	}
	if code == exitOK {
		_, _ = fmt.Fprintln(stdout, "агент выключен: токены и состояние удалены, метки Teleport остаются как есть")
	}
	return code
}

// runSync is `stvor-agent sync`: one manual tick now (no 3×/15 min pause).
// 0 — the labels are in effect, or the portal failed by network/HTTP (the
// service retries); 1 — a body not by the contract, a restart not confirmed,
// a broken token, teleport state unknown.
func (a *App) runSync(stdout, stderr io.Writer) int {
	res, err := daemon.Sync(context.Background(), a.Lock, a.ticker(newLogger(stderr, slog.LevelInfo)))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "stvor-agent: sync: %v\n", err)
		return exitError
	}
	fail := func(format string, args ...any) int {
		_, _ = fmt.Fprintf(stderr, "stvor-agent: sync: "+format+"\n", args...)
		return exitError
	}

	switch res.Outcome {
	case reconcile.OutcomeOK, reconcile.OutcomeNotModified:
	case reconcile.OutcomeDisabled:
		if res.TokenErr != nil {
			return fail("файл токена не годен (%s) — выполните stvor-agent enable заново", token.Kind(res.TokenErr))
		}
		return fail("агент не включён — stvor-agent enable URL=<адрес>")
	case reconcile.OutcomeBadBody:
		return fail("ответ портала не по контракту — метки не тронуты")
	default:
		line := "портал: " + string(res.Outcome)
		if res.HTTPStatus != 0 {
			line += fmt.Sprintf(" (HTTP %d)", res.HTTPStatus)
		}
		_, _ = fmt.Fprintln(stdout, line+" — служба повторит на своём тике")
		return exitOK
	}

	switch res.Apply {
	case reconcile.ApplyUnconfirmed:
		return fail("агент Teleport не подтвердил перезапуск с новыми метками")
	case reconcile.ApplyError:
		return fail("метки не действуют: ошибка teleport (см. строку журнала выше)")
	case reconcile.ApplyPaused:
		return fail("перезапуск агента Teleport на паузе")
	case reconcile.ApplyDeferred:
		_, _ = fmt.Fprintln(stdout, "узел ещё не зарегистрирован в Teleport — метки применятся после join")
		return exitOK
	}

	// The tick's end state, seen under the lock: no second managed-status.
	switch {
	case res.Expected == nil:
		return fail("портал ответил 304, а запомненных меток нет — повторите sync")
	case res.StatusErr != nil:
		return fail("метки не действуют: состояние teleport неизвестно: %v", res.StatusErr)
	case !res.InEffect:
		ts := res.Teleport
		return fail("метки не действуют: в teleport %s, pending=%t joined=%t active=%t",
			labelsText(ts.Managed), ts.Pending, ts.Joined, ts.Active)
	}
	w := teleport.Labels(res.Expected.Labels)
	_, _ = fmt.Fprintf(stdout, "метки действуют: %s (ревизия %s)\n", labelsText(w), res.Expected.Revision)
	return exitOK
}

// runApply is `stvor-agent apply env=<v> space=<v>`: the manual fallback of
// the robot card — set-managed without the portal and without the pause;
// state.json (the portal's decision) is not touched.
func (a *App) runApply(l teleport.Labels, stdout, stderr io.Writer) int {
	ctx := context.Background()
	unlock, err := a.Lock.Lock(ctx, daemon.CLILockWait)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "stvor-agent: apply: %v\n", err)
		return exitError
	}
	defer unlock()
	r, err := a.Teleport.SetManaged(ctx, l)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "stvor-agent: apply: set-managed не выполнен: %v\n", err)
		return exitError
	}
	switch r.Result {
	case teleport.ResultApplied, teleport.ResultUnchanged:
		_, _ = fmt.Fprintf(stdout, "метки записаны в teleport: %s (%s); следующий тик службы вернёт решение портала\n",
			labelsText(l), r.Result)
		return exitOK
	case teleport.ResultDeferred:
		_, _ = fmt.Fprintf(stdout, "узел не зарегистрирован — метки %s применятся после join\n", labelsText(l))
		return exitOK
	default:
		_, _ = fmt.Fprintf(stderr, "stvor-agent: apply: агент Teleport не подтвердил перезапуск с метками %s\n", labelsText(l))
		return exitError
	}
}

func labelsText(l teleport.Labels) string { return "env=" + l.Env + " space=" + l.Space }
