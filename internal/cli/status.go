package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/iamletenkov/stvor-agent/internal/reconcile"
	"github.com/iamletenkov/stvor-agent/internal/state"
	"github.com/iamletenkov/stvor-agent/internal/teleport"
	"github.com/iamletenkov/stvor-agent/internal/token"
	"github.com/iamletenkov/stvor-agent/internal/version"
)

// statusSchema is the version of the `status --json` document (spec §7).
const statusSchema = 1

// statusDoc is `status --json`, schema 1; field order is the contract.
type statusDoc struct {
	Schema          int          `json:"schema"`
	Version         string       `json:"version"`
	Service         string       `json:"service"`
	Enabled         *bool        `json:"enabled"` // null — unknown: no access to the token file (not root)
	URL             *string      `json:"url"`
	Token           string       `json:"token"`
	Expected        *expectedDoc `json:"expected"`
	AppliedRevision *string      `json:"applied_revision"`
	InEffect        bool         `json:"in_effect"`
	Teleport        *teleportDoc `json:"teleport"`
	Restart         restartDoc   `json:"restart"`
	LastPoll        *lastPollDoc `json:"last_poll"`
	// Telemetry is the telemetry service (Stvor ADR-0034); added in 4.1.0.
	Telemetry telemetryDoc `json:"telemetry"`
}

type expectedDoc struct {
	Env      string `json:"env"`
	Space    string `json:"space"`
	Revision string `json:"revision"`
}

type labelsDoc struct {
	Env   string `json:"env"`
	Space string `json:"space"`
}

type teleportDoc struct {
	Managed labelsDoc `json:"managed"`
	Pending bool      `json:"pending"`
	Joined  bool      `json:"joined"`
	Active  bool      `json:"active"`
}

type restartDoc struct {
	Attempts    int        `json:"attempts"`
	PausedUntil *time.Time `json:"paused_until"`
}

type lastPollDoc struct {
	At         time.Time `json:"at"`
	Outcome    string    `json:"outcome"`
	HTTPStatus int       `json:"http_status"`
}

// runStatus is `stvor-agent status [--json]`: the token class (never the
// token), state.json and managed-status of teleport; no network, no lock.
// 0 — enabled, 1 — not enabled (for scripts).
func (a *App) runStatus(args []string, stdout, stderr io.Writer) int {
	asJSON := false
	switch {
	case len(args) == 0:
	case len(args) == 1 && args[0] == "--json":
		asJSON = true
	case len(args) == 1:
		_, _ = fmt.Fprintf(stderr, "stvor-agent: status: unknown flag %q\n\n%s", args[0], usage)
		return exitUsage
	default:
		_, _ = fmt.Fprintf(stderr, "stvor-agent: status: too many arguments\n\n%s", usage)
		return exitUsage
	}

	doc, problems := a.collectStatus(context.Background())
	for _, p := range problems {
		_, _ = fmt.Fprintf(stderr, "stvor-agent: status: %s\n", p)
	}
	var code int
	if asJSON {
		out, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "stvor-agent: status: %v\n", err)
			return exitError
		}
		code = writeOut(stdout, string(out)+"\n")
	} else {
		code = writeHuman(stdout, doc)
	}
	if code == exitOK && (doc.Enabled == nil || !*doc.Enabled) {
		return exitError
	}
	return code
}

// collectStatus gathers the document and the journal-style problems met on
// the way (state.json refused, teleport unknown).
func (a *App) collectStatus(ctx context.Context) (statusDoc, []string) {
	var problems []string
	doc := statusDoc{
		Schema:  statusSchema,
		Version: version.Short(a.Version),
		Service: a.Init.IsActive(ctx, Unit),
		Token:   "ok",
	}
	doc.Telemetry = a.collectTelemetry(ctx)
	tok, terr := token.Read(a.FS, a.TokenPath)
	if terr != nil {
		doc.Token = token.Kind(terr)
	} else {
		doc.URL = &tok.URL
	}
	if errors.Is(terr, token.ErrNoAccess) {
		problems = append(problems, "нужен root для полного статуса: файл токена закрыт для этого пользователя")
	} else {
		enabled := doc.Token != "absent"
		doc.Enabled = &enabled
	}

	st, err := state.Load(a.FS, a.StatePath)
	haveState := err == nil && (terr != nil || st.URL == tok.URL)
	if err != nil && !errors.Is(err, state.ErrAbsent) {
		problems = append(problems, "state.json не принят: "+err.Error())
	}

	ts, tsErr := a.Teleport.Status(ctx)
	if tsErr != nil {
		problems = append(problems, "managed-status не ответил: "+tsErr.Error())
	} else {
		doc.Teleport = &teleportDoc{
			Managed: labelsDoc{Env: ts.Managed.Env, Space: ts.Managed.Space},
			Pending: ts.Pending, Joined: ts.Joined, Active: ts.Active,
		}
	}

	if !haveState {
		return doc, problems
	}
	if e := st.Expected; e != nil {
		doc.Expected = &expectedDoc{Env: e.Env, Space: e.Space, Revision: e.Revision}
		doc.InEffect = tsErr == nil && reconcile.InEffect(ts, teleport.Labels(e.Labels))
	}
	if st.AppliedRevision != "" {
		doc.AppliedRevision = &st.AppliedRevision
	}
	if p := st.LastPoll; p != nil {
		doc.LastPoll = &lastPollDoc{At: p.At.UTC(), Outcome: p.Outcome, HTTPStatus: p.HTTPStatus}
	}
	att := st.RestartAttempts
	doc.Restart.Attempts = att.Count
	if until := att.Last.Add(reconcile.PauseFor).UTC(); att.Count >= reconcile.PauseAfter &&
		tsErr == nil && ts.Active && a.Now().Before(until) {
		doc.Restart.PausedUntil = &until
	}
	return doc, problems
}

// writeHuman prints the document as a table in Russian.
func writeHuman(stdout io.Writer, d statusDoc) int {
	yes := func(b bool) string {
		if b {
			return "да"
		}
		return "нет"
	}
	orDash := func(s *string) string {
		if s == nil {
			return "—"
		}
		return *s
	}
	enabled := "неизвестно (нужен root)"
	if d.Enabled != nil {
		enabled = yes(*d.Enabled)
	}
	expected := "—"
	if e := d.Expected; e != nil {
		expected = fmt.Sprintf("env=%s space=%s (ревизия %s)", e.Env, e.Space, e.Revision)
	}
	tp := "неизвестно"
	if t := d.Teleport; t != nil {
		tp = fmt.Sprintf("env=%s space=%s, pending: %s, joined: %s, active: %s",
			t.Managed.Env, t.Managed.Space, yes(t.Pending), yes(t.Joined), yes(t.Active))
	}
	restart := strconv.Itoa(d.Restart.Attempts) + " неподтверждённых подряд"
	if d.Restart.PausedUntil != nil {
		restart += ", пауза до " + d.Restart.PausedUntil.Format(time.RFC3339)
	}
	poll := "—"
	if p := d.LastPoll; p != nil {
		poll = p.At.Format(time.RFC3339) + " " + p.Outcome
		if p.HTTPStatus != 0 {
			poll += fmt.Sprintf(" (HTTP %d)", p.HTTPStatus)
		}
	}

	w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	for _, row := range [][2]string{
		{"версия", d.Version},
		{"служба", d.Service},
		{"включён", enabled},
		{"портал", orDash(d.URL)},
		{"токен", d.Token},
		{"ожидаемые метки", expected},
		{"применённая ревизия", orDash(d.AppliedRevision)},
		{"метки действуют", yes(d.InEffect)},
		{"teleport", tp},
		{"перезапуски", restart},
		{"последний опрос", poll},
		{"телеметрия", telemetryText(d.Telemetry)},
	} {
		_, _ = fmt.Fprintf(w, "%s:\t%s\n", row[0], row[1])
	}
	if err := w.Flush(); err != nil {
		return exitError
	}
	return exitOK
}

// telemetryText is the telemetry row of the human status.
func telemetryText(t telemetryDoc) string {
	s := "служба " + t.Service + ", токен " + t.Token
	if p := t.LastSend; p != nil {
		s += ", отправка " + p.At.Format(time.RFC3339) + " " + p.Outcome
		if p.HTTPStatus != 0 {
			s += fmt.Sprintf(" (HTTP %d)", p.HTTPStatus)
		}
	}
	if b := t.Buffer; b != nil {
		s += fmt.Sprintf(", буфер %d из %d байт", b.UsedBytes, b.QuotaBytes)
	}
	return s
}
