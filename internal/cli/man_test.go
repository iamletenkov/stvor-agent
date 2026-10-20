package cli

import (
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode"
)

// usageV4 is the help text of stvor-agent 4.0.0: the command table renders it
// byte for byte.
const usageV4 = `usage: stvor-agent <command> [KEY=VALUE ...]

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

func TestUsageFromCommandTable(t *testing.T) {
	t.Parallel()
	if usage != usageV4 {
		t.Errorf("usage =\n%s\nwant\n%s", usage, usageV4)
	}
}

// TestCommandTableMatchesDispatch: every command of the table is dispatched,
// every flag is accepted, Root matches the root check, and names outside the
// table are unknown.
func TestCommandTableMatchesDispatch(t *testing.T) {
	t.Parallel()
	// Arguments that pass parsing for each root command.
	valid := map[string][]string{"enable": {"URL=" + testURL}, "apply": {"env=prod", "space=sp-1"}}
	for _, c := range commands {
		e := newEnv(t)
		_, _, stderr := e.run("", c.Name, "bogus=1", "x")
		if strings.Contains(stderr, "unknown command") {
			t.Errorf("%s: not dispatched: %s", c.Name, stderr)
		}
		for _, f := range c.Flags {
			if !strings.Contains(c.Args, f) {
				t.Errorf("%s: flag %s not in Args %q", c.Name, f, c.Args)
			}
			e := newEnv(t)
			if code, _, stderr := e.run("", c.Name, f); code == exitUsage {
				t.Errorf("%s %s: usage error: %s", c.Name, f, stderr)
			}
		}
		if c.Root {
			e := newEnv(t)
			e.uid = 1000
			code, _, stderr := e.run("", append([]string{c.Name}, valid[c.Name]...)...)
			if code != exitError || !strings.Contains(stderr, "root") {
				t.Errorf("%s as uid 1000: code %d, stderr %q; want root refusal", c.Name, code, stderr)
			}
		}
	}
	e := newEnv(t)
	if code, _, _ := e.run("", "frob"); code != exitUsage {
		t.Errorf("unknown command: code %d", code)
	}
}

// TestManDocsMatchCommands: the English prose of stvor-agent(8) covers exactly
// the commands and flags of --help.
func TestManDocsMatchCommands(t *testing.T) {
	t.Parallel()
	var names, flags []string
	for _, c := range commands {
		names = append(names, c.Name)
		for _, f := range c.Flags {
			flags = append(flags, c.Name+" "+f)
		}
	}
	sort.Strings(names)
	sort.Strings(flags)
	if got := sortedKeys(manText); !slices.Equal(got, names) {
		t.Errorf("manText = %v, commands = %v", got, names)
	}
	if got := sortedKeys(manFlags); !slices.Equal(got, flags) {
		t.Errorf("manFlags = %v, flags = %v", got, flags)
	}
}

func TestManPage(t *testing.T) {
	t.Parallel()
	date := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	page := ManPage("4.1.0", date)
	if page != ManPage("4.1.0", date) {
		t.Error("not deterministic")
	}
	if first, _, _ := strings.Cut(page, "\n"); !strings.HasPrefix(first, `.TH STVOR\-AGENT 8 2026-10-18 "stvor\-agent 4.1.0"`) {
		t.Errorf(".TH = %q", first)
	}
	for _, c := range commands {
		if !strings.Contains(page, ".B stvor\\-agent "+roffEscape(c.synopsis())+"\n") {
			t.Errorf("no synopsis of %s", c.Name)
		}
		for _, f := range c.Flags {
			if !strings.Contains(page, ".B "+roffEscape(c.Name+" "+f)+"\n") {
				t.Errorf("no option %s %s", c.Name, f)
			}
		}
	}
	for _, s := range []string{"NAME", "SYNOPSIS", "DESCRIPTION", "COMMANDS", "OPTIONS", "EXIT STATUS",
		"ENVIRONMENT", "FILES", "EXAMPLES", "SEE ALSO"} {
		if !strings.Contains(page, ".SH "+s+"\n") {
			t.Errorf("no section %s", s)
		}
	}
	for _, s := range []string{"/etc/stvor\\-agent/token", "STVOR_AGENT_INTERVAL", "stvor\\-agent.service"} {
		if !strings.Contains(page, s) {
			t.Errorf("no %s", s)
		}
	}
	for i, r := range page {
		if r > unicode.MaxASCII {
			t.Fatalf("non-ASCII %q at %d", r, i)
		}
	}
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
