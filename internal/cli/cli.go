// Package cli parses stvor-agent subcommands and maps them to exit codes
// (spec §8). Everything external — files, systemctl, teleport, the portal,
// the clock, the uid, signals — is a field of App, so tests swap it.
package cli

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/iamletenkov/stvor-agent/internal/atomicfile"
	"github.com/iamletenkov/stvor-agent/internal/daemon"
	"github.com/iamletenkov/stvor-agent/internal/portal"
	"github.com/iamletenkov/stvor-agent/internal/reconcile"
	"github.com/iamletenkov/stvor-agent/internal/state"
	"github.com/iamletenkov/stvor-agent/internal/systemd"
	"github.com/iamletenkov/stvor-agent/internal/teleport"
	"github.com/iamletenkov/stvor-agent/internal/token"
	"github.com/iamletenkov/stvor-agent/internal/version"
)

const usage = `usage: stvor-agent <command> [KEY=VALUE ...]

commands:
  run                         service loop (ExecStart of stvor-agent.service)
  enable URL=<url>            store the portal token from stdin and start the service
  disable                     stop the service, remove the token and the state
  sync                        one reconcile tick now
  apply env=<v> space=<v>     set the managed labels without the portal
  status [--json]             show the agent state
  version [--short|--label]   print the version
  help                        print this help
`

// Exit codes shared by all subcommands.
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

// Unit is the systemd unit of the service.
const Unit = "stvor-agent.service"

// reLabel is the alphabet of env and space (spec §5.4).
var reLabel = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// FS is what the CLI needs from the file system: the token and state stores
// plus Remove for `disable` and the state reset of `enable`.
type FS interface {
	token.FS
	Remove(path string) error
}

// osFS is the real FS.
type osFS struct{ atomicfile.OS }

func (osFS) Remove(path string) error { return os.Remove(path) }

// App is one stvor-agent process with its dependencies.
type App struct {
	Version   string
	FS        FS
	TokenPath string
	StatePath string
	// Lock is the agent lock: the service takes it without waiting, the CLI
	// commands wait up to daemon.CLILockWait.
	Lock      daemon.FileLock
	Systemctl systemd.Systemctl
	Teleport  teleport.Teleport
	Portal    reconcile.Portal
	Now       func() time.Time
	Getuid    func() int
	Getenv    func(string) string
	// Notify and StopNotify are signal.Notify and signal.Stop.
	Notify     func(c chan<- os.Signal, sig ...os.Signal)
	StopNotify func(c chan<- os.Signal)
}

// New is the App of the real machine. Cream and systemctl write their stderr
// to stderr (the journal under the service).
func New(ver string, stderr io.Writer) *App {
	tp := teleport.NewCream("")
	tp.Stderr = stderr
	return &App{
		Version:    ver,
		FS:         osFS{},
		TokenPath:  token.DefaultPath,
		StatePath:  state.DefaultPath,
		Lock:       daemon.FileLock{Path: daemon.DefaultLockPath},
		Systemctl:  systemd.Exec{Stderr: stderr},
		Teleport:   tp,
		Portal:     portal.New(portal.Config{Version: ver}),
		Now:        time.Now,
		Getuid:     os.Getuid,
		Getenv:     os.Getenv,
		Notify:     signal.Notify,
		StopNotify: signal.Stop,
	}
}

// Run executes one stvor-agent command on the real machine and returns the
// process exit code. The version comes from internal/version (ldflags).
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return New(version.Version, stderr).Run(args, stdin, stdout, stderr)
}

// Run executes one command and returns its exit code. Write errors to stderr
// are ignored: there is nowhere left to report them.
func (a *App) Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, usage)
		return exitUsage
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "version":
		return a.runVersion(rest, stdout, stderr)
	case "help", "-h", "--help":
		return writeOut(stdout, usage)
	case "status":
		return a.runStatus(rest, stdout, stderr)
	case "run", "enable", "disable", "sync", "apply":
	default:
		_, _ = fmt.Fprintf(stderr, "stvor-agent: unknown command %q\n\n%s", cmd, usage)
		return exitUsage
	}

	var kv map[string]string
	var err error
	switch cmd {
	case "enable":
		kv, err = parseKV(rest, "URL")
	case "apply":
		kv, err = parseKV(rest, "env", "space")
		if err == nil {
			err = checkLabels(kv)
		}
	default:
		if len(rest) != 0 {
			err = fmt.Errorf("%s не принимает аргументов", cmd)
		}
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "stvor-agent: %s: %v\n\n%s", cmd, err, usage)
		return exitUsage
	}
	if uid := a.Getuid(); uid != 0 {
		_, _ = fmt.Fprintf(stderr, "stvor-agent: %s: нужны права root (uid %d)\n", cmd, uid)
		return exitError
	}

	switch cmd {
	case "run":
		return a.runService(stderr)
	case "enable":
		return a.runEnable(kv["URL"], stdin, stdout, stderr)
	case "disable":
		return a.runDisable(stdout, stderr)
	case "sync":
		return a.runSync(stdout, stderr)
	default: // apply
		return a.runApply(teleport.Labels{Env: kv["env"], Space: kv["space"]}, stdout, stderr)
	}
}

// parseKV parses KEY=VALUE arguments: every key of keys exactly once,
// nothing else (spec §8).
func parseKV(args []string, keys ...string) (map[string]string, error) {
	kv := make(map[string]string, len(keys))
	for _, arg := range args {
		k, v, ok := strings.Cut(arg, "=")
		if !ok {
			return nil, fmt.Errorf("аргумент %q не KEY=VALUE", arg)
		}
		if !slices.Contains(keys, k) {
			return nil, fmt.Errorf("неизвестный ключ %q (ожидаются %s)", k, strings.Join(keys, ", "))
		}
		if _, dup := kv[k]; dup {
			return nil, fmt.Errorf("повтор ключа %s", k)
		}
		kv[k] = v
	}
	for _, k := range keys {
		if _, ok := kv[k]; !ok {
			return nil, fmt.Errorf("нужен %s=<значение>", k)
		}
	}
	return kv, nil
}

// checkLabels refuses env and space outside the label alphabet.
func checkLabels(kv map[string]string) error {
	for _, k := range []string{"env", "space"} {
		if !reLabel.MatchString(kv[k]) {
			return fmt.Errorf("%s=%q вне алфавита меток [a-z0-9][a-z0-9-]{0,62}", k, kv[k])
		}
	}
	return nil
}

// runVersion prints the long version line; --short — the bare X.Y.Z;
// --label — X.Y.Z when the token file exists (the agent is enabled), else
// nothing (spec §4.4). --label stats the token file only, takes no lock and
// exits 0 always: Teleport would publish an error text as the label.
func (a *App) runVersion(args []string, stdout, stderr io.Writer) int {
	short := version.Short(a.Version)
	switch {
	case len(args) == 0:
		return writeOut(stdout, fmt.Sprintf("stvor-agent %s (%s %s/%s)\n",
			short, runtime.Version(), runtime.GOOS, runtime.GOARCH))
	case len(args) == 1 && args[0] == "--short":
		return writeOut(stdout, short+"\n")
	case len(args) == 1 && args[0] == "--label":
		if token.Exists(a.FS, a.TokenPath) {
			_ = writeOut(stdout, short+"\n")
		}
		return exitOK
	case len(args) == 1:
		_, _ = fmt.Fprintf(stderr, "stvor-agent: version: unknown flag %q\n\n%s", args[0], usage)
		return exitUsage
	default:
		_, _ = fmt.Fprintf(stderr, "stvor-agent: version: too many arguments\n\n%s", usage)
		return exitUsage
	}
}

// writeOut writes s to stdout and maps a write failure (closed pipe, full disk)
// to exit code 1.
func writeOut(stdout io.Writer, s string) int {
	if _, err := io.WriteString(stdout, s); err != nil {
		return exitError
	}
	return exitOK
}

// newLogger is the journal: one key=value line per event on w. journald
// stamps the time itself, so the handler drops it.
func newLogger(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}))
}

// ticker is the reconcile tick over the App's dependencies.
func (a *App) ticker(log *slog.Logger) *reconcile.Ticker {
	return &reconcile.Ticker{
		Tokens:   reconcile.TokenFile{FS: a.FS, Path: a.TokenPath},
		States:   reconcile.StateFile{FS: a.FS, Path: a.StatePath},
		Portal:   a.Portal,
		Teleport: a.Teleport,
		Now:      a.Now,
		Log:      log,
	}
}
