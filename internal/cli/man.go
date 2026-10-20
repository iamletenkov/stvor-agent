package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/iamletenkov/stvor-agent/internal/daemon"
	"github.com/iamletenkov/stvor-agent/internal/state"
	"github.com/iamletenkov/stvor-agent/internal/token"
)

// The manual page stvor-agent(8) is rendered from the command table of --help
// (commands): synopses, summaries, flags and the root requirement come from
// there; manText adds the longer description of each command and manFlags of
// each flag. man_test.go fails when the two disagree. tools/genman writes the
// gzipped page for the .deb.

// manText is the DESCRIPTION paragraph of each command, by name.
var manText = map[string]string{
	"run": "The service loop, started by systemd as ExecStart of " + Unit + ". " +
		"Every interval it polls the robot API of the portal and applies the managed labels through " +
		"the cream extension teleport. Exits 0 on SIGTERM after the current tick.",
	"enable": "Reads the portal token from stdin (one line on a terminal), stores URL and token in " +
		token.DefaultPath + " (mode 0600), resets the state when the URL changes, enables and restarts " +
		Unit + " and runs one synchronous tick. A network failure of that tick is reported and still exits 0.",
	"disable": "Disables and stops " + Unit + ", removes the token and the state. " +
		"The managed labels stay as they are.",
	"sync": "Runs one reconcile tick now, without the retry pause. Exits 0 when the labels are in effect " +
		"or on a network or HTTP error (reported), 1 when the answer breaks the contract, " +
		"the restart is not confirmed or the token is invalid.",
	"apply": "Fallback without the portal: sets the managed labels env and space directly. " +
		"The expected labels in the state are not changed, so the next tick restores the decision of the portal. " +
		"Values match [a\\-z0\\-9][a\\-z0\\-9\\-]{0,62}.",
	"status": "Shows the token class (never the token), the state and the Teleport labels. " +
		"No network, no lock. Exits 1 when the agent is not enabled.",
	"version": "Prints the version, commit and build. Always exits 0.",
	"help":    "Prints the list of commands. \\fB\\-h\\fR and \\fB\\-\\-help\\fR are the same.",
}

// manFlags describes each flag of the command table, by "command flag".
var manFlags = map[string]string{
	"status --json":   "Print the state as a JSON document (schema 1).",
	"version --short": "Print only X.Y.Z.",
	"version --label": "Print X.Y.Z when the token file exists, otherwise nothing; " +
		"the command of the dynamic Teleport label stvor_agent (only stat of the token file, no lock, no network).",
}

// ManPage renders stvor-agent(8) for version (the .TH footer) and date
// (SOURCE_DATE_EPOCH of the build).
func ManPage(version string, date time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, ".TH STVOR\\-AGENT 8 %s \"stvor\\-agent %s\" \"System Administration\"\n",
		date.UTC().Format("2006-01-02"), roffEscape(version))
	b.WriteString(".SH NAME\nstvor\\-agent \\- Stvor portal agent: Teleport labels of this machine from the portal\n")
	b.WriteString(".SH SYNOPSIS\n")
	for i, c := range commands {
		if i > 0 {
			b.WriteString(".br\n")
		}
		b.WriteString(".B stvor\\-agent " + roffEscape(c.synopsis()) + "\n")
	}
	b.WriteString(`.SH DESCRIPTION
\fBstvor\-agent\fR represents the Stvor portal on a machine. The service
polls the robot API of the portal and keeps the managed Teleport labels
(\fBenv\fR, \fBspace\fR) equal to the decision of the portal through the
\fBteleport\fR extension of \fBcream\fR(1). The unit starts only after
\fBstvor\-agent enable\fR has stored a token.
.PP
Arguments are KEY=VALUE pairs; a repeated or unknown key is a usage error.
`)
	b.WriteString(".SH COMMANDS\n")
	for _, c := range commands {
		b.WriteString(".TP\n.B " + roffEscape(c.synopsis()) + "\n")
		b.WriteString(manText[c.Name])
		if c.Root {
			b.WriteString(" Requires root.")
		}
		b.WriteString("\n")
	}
	b.WriteString(".SH OPTIONS\n")
	for _, c := range commands {
		for _, f := range c.Flags {
			b.WriteString(".TP\n.B " + roffEscape(c.Name+" "+f) + "\n" + manFlags[c.Name+" "+f] + "\n")
		}
	}
	fmt.Fprintf(&b, `.SH EXIT STATUS
.TP
.B %d
Success.
.TP
.B %d
Error: input or output, not root, not enabled (status), contract violation (sync).
.TP
.B %d
Usage error: unknown command, flag or key, missing argument.
`, exitOK, exitError, exitUsage)
	fmt.Fprintf(&b, `.SH ENVIRONMENT
Read by the service from \fI/etc/cream/stvor\-agent/config\fR (EnvironmentFile of the unit, managed by
\fBcream set stvor\-agent\fR). Invalid values fall back to the default with a journal line.
.TP
.B %s
Poll interval in seconds, %d\(en%d, default %d.
.TP
.B %s
\fBinfo\fR (default) or \fBdebug\fR.
`, roffEscape(daemon.EnvInterval), int(daemon.MinInterval.Seconds()), int(daemon.MaxInterval.Seconds()),
		int(daemon.DefaultInterval.Seconds()), roffEscape(daemon.EnvLogLevel))
	fmt.Fprintf(&b, `.SH FILES
.TP
.I %s
Portal URL and token (url=, token=), mode 0600, directory 0700; written only by \fBenable\fR.
.TP
.I %s
State: expected and applied labels, restart attempts, last poll.
.TP
.I %s
Agent lock: the service takes it at once, commands wait for it.
.TP
.I /usr/lib/systemd/system/%s
The systemd unit (ConditionPathExists on the token file).
.TP
.I /etc/cream/stvor\-agent/config
Service settings (see ENVIRONMENT).
`, roffEscape(token.DefaultPath), roffEscape(state.DefaultPath), roffEscape(daemon.DefaultLockPath), roffEscape(Unit))
	b.WriteString(`.SH EXAMPLES
Enable the agent with a token from the portal:
.PP
.nf
.RS
echo "$TOKEN" | stvor\-agent enable URL=https://stvor.example/robot\-api/v1/labels
.RE
.fi
.PP
Show the state for a script:
.PP
.nf
.RS
stvor\-agent status \-\-json
.RE
.fi
.SH SEE ALSO
\fBcream\fR(1), \fBsystemctl\fR(1), \fBjournalctl\fR(1)
.PP
https://github.com/iamletenkov/stvor\-agent
`)
	return b.String()
}

// roffEscape makes s safe as roff text: backslashes and hyphens escaped.
func roffEscape(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\e")
	return strings.ReplaceAll(s, "-", "\\-")
}
