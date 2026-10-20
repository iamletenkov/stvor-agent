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

// command is one subcommand as --help shows it; man.go renders stvor-agent(8)
// from the same table, so help and the man page cannot list different
// commands or flags.
type command struct {
	Name string
	// Args is the synopsis after the name: KEY=VALUE arguments and flags.
	Args string
	// Summary is the one-line help text.
	Summary string
	// Flags are the flags the command accepts (they also appear in Args).
	Flags []string
	// Root is true when the command needs uid 0.
	Root bool
}

// commands is the command table, in help order.
var commands = []command{
	{Name: "run", Summary: "service loop (ExecStart of stvor-agent.service)", Root: true},
	{Name: "enable", Args: "URL=<url>", Summary: "store the portal token from stdin and start the service", Root: true},
	{Name: "disable", Summary: "stop the service, remove the token and the state", Root: true},
	{Name: "sync", Summary: "one reconcile tick now", Root: true},
	{Name: "apply", Args: "env=<v> space=<v>", Summary: "set the managed labels without the portal", Root: true},
	{Name: "status", Args: "[--json]", Summary: "show the agent state", Flags: []string{"--json"}},
	{Name: "version", Args: "[--short|--label]", Summary: "print the version", Flags: []string{"--short", "--label"}},
	{Name: "help", Summary: "print this help"},
}

// synopsis is "name args".
func (c command) synopsis() string {
	return strings.TrimSpace(c.Name + " " + c.Args)
}

func findCommand(name string) (command, bool) {
	for _, c := range commands {
		if c.Name == name {
			return c, true
		}
	}
	return command{}, false
}

// usage is the help text of every command.
var usage = func() string {
	var b strings.Builder
	b.WriteString("usage: stvor-agent <command> [KEY=VALUE ...]\n\ncommands:\n")
	for _, c := range commands {
		fmt.Fprintf(&b, "  %-28s%s\n", c.synopsis(), c.Summary)
	}
	return b.String()
}()

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
	Version string
	// Commit is the VCS commit of the build (ldflags); "" — unknown.
	Commit    string
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
	// IsTerminal reports whether stdin is a terminal (enable then reads one
	// line, not up to EOF); nil — never.
	IsTerminal func(io.Reader) bool
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
		IsTerminal: isTerminal,
	}
}

// Run executes one stvor-agent command on the real machine and returns the
// process exit code. The version comes from internal/version (ldflags).
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	a := New(version.Version, stderr)
	a.Commit = version.Commit
	return a.Run(args, stdin, stdout, stderr)
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
	}
	// The rest of the table: commands that need root.
	if c, ok := findCommand(cmd); !ok || !c.Root {
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
		commit := a.Commit
		if commit == "" {
			commit = "unknown"
		}
		return writeOut(stdout, fmt.Sprintf("stvor-agent %s (commit %s, %s %s/%s)\n",
			short, commit, runtime.Version(), runtime.GOOS, runtime.GOARCH))
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
