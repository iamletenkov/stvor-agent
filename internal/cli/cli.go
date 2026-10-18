// Package cli parses stvor-agent subcommands and maps them to exit codes.
//
// For now only `version` and `help` work; the other subcommands of spec §8 are
// recognised and answer "not implemented" with code 2 until they are built.
package cli

import (
	"fmt"
	"io"
	"runtime"

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

// Run executes one stvor-agent command and returns the process exit code.
// The version comes from internal/version (set by ldflags at build time).
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return run(args, version.Version, stdin, stdout, stderr)
}

// run is Run with the build version passed in, so tests can pin it.
// Write errors to stderr are ignored: there is nowhere left to report them.
func run(args []string, ver string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, usage)
		return exitUsage
	}
	switch cmd := args[0]; {
	case cmd == "version":
		return runVersion(args[1:], ver, stdout, stderr)
	case cmd == "help" || cmd == "-h" || cmd == "--help":
		return writeOut(stdout, usage)
	case notImplemented(cmd):
		_, _ = fmt.Fprintf(stderr, "stvor-agent: command %q is not implemented yet\n", cmd)
		return exitUsage
	default:
		_, _ = fmt.Fprintf(stderr, "stvor-agent: unknown command %q\n\n%s", cmd, usage)
		return exitUsage
	}
}

// runVersion prints the long version line or, with --short, the bare X.Y.Z.
// --label (spec §4.4) arrives with the token store.
func runVersion(args []string, ver string, stdout, stderr io.Writer) int {
	short := version.Short(ver)
	switch {
	case len(args) == 0:
		return writeOut(stdout, fmt.Sprintf("stvor-agent %s (%s %s/%s)\n",
			short, runtime.Version(), runtime.GOOS, runtime.GOARCH))
	case len(args) == 1 && args[0] == "--short":
		return writeOut(stdout, short+"\n")
	case len(args) == 1:
		_, _ = fmt.Fprintf(stderr, "stvor-agent: version: unknown flag %q\n\n%s", args[0], usage)
		return exitUsage
	default:
		_, _ = fmt.Fprintf(stderr, "stvor-agent: version: too many arguments\n\n%s", usage)
		return exitUsage
	}
}

// notImplemented reports the subcommands of spec §8 that are not built yet.
func notImplemented(cmd string) bool {
	switch cmd {
	case "run", "enable", "disable", "sync", "apply", "status":
		return true
	}
	return false
}

// writeOut writes s to stdout and maps a write failure (closed pipe, full disk)
// to exit code 1.
func writeOut(stdout io.Writer, s string) int {
	if _, err := io.WriteString(stdout, s); err != nil {
		return exitError
	}
	return exitOK
}
