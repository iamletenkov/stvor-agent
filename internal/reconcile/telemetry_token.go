package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/iamletenkov/stvor-agent/internal/atomicfile"
	"github.com/iamletenkov/stvor-agent/internal/portal"
	"github.com/iamletenkov/stvor-agent/internal/token"
)

// The telemetry token on a machine (Stvor plan R4). The root service `run`
// writes it for the unprivileged telemetry service: no LoadCredential= —
// Jetson L4T R32 is Ubuntu 18.04 with systemd 237 (< 247).
const (
	// TelemetryTokenPath holds "url=<base>\ntoken=<token>\n"; file 0640 and
	// directory 0750, both root:TelemetryGroup.
	TelemetryTokenPath = "/etc/stvor-agent-telemetry/token"
	// TelemetryRejectedPath is the marker the telemetry service writes when
	// the portal answers 401: the next tick of `run` asks for a new token.
	TelemetryRejectedPath = "/var/lib/stvor-agent-telemetry/token-rejected"
	// TelemetrySourcePath is the sha256 of the labels token the telemetry
	// token was issued for (root only): a new labels token (`enable` after a
	// reissue on the portal) revokes the telemetry token, so it is asked anew.
	TelemetrySourcePath = "/var/lib/stvor-agent/telemetry-token.src"
	// TelemetryGroup is the group of the telemetry service (sysusers.d).
	TelemetryGroup = "stvor-telemetry"
	// TelemetryUnit is the unit started once the token is there.
	TelemetryUnit = "stvor-agent-telemetry.service"
)

// Retry pauses of the token request: a portal without the endpoint (404) or
// a failing one is asked again at most once per TelemetryRetryCeil.
const (
	TelemetryRetryFirst = time.Minute
	TelemetryRetryCeil  = time.Hour
)

// TelemetryPortal asks POST /robot-api/v1/telemetry-token; *portal.Client
// implements it.
type TelemetryPortal interface {
	TelemetryToken(ctx context.Context, r portal.Request) (portal.TokenResponse, error)
}

// TelemetryFS is what the token step needs from the file system;
// atomicfile.OS plus os.Remove is the real one.
type TelemetryFS interface {
	ReadNoFollow(path string, limit int64) ([]byte, atomicfile.Info, error)
	Lstat(path string) (atomicfile.Info, error)
	EnsureDir(dir string, mode fs.FileMode, fixMode bool) error
	WriteAtomic(path string, data []byte, mode fs.FileMode) error
	WriteAtomicOwned(path string, data []byte, mode fs.FileMode, uid, gid int) error
	Chown(path string, uid, gid int) error
	Remove(path string) error
}

// TelemetryStep is the tick step that keeps the telemetry token.
type TelemetryStep interface {
	Ensure(ctx context.Context, tok token.Token)
}

// TelemetryToken keeps the token of the telemetry service. It is used by one
// goroutine (the tick, under the agent lock).
type TelemetryToken struct {
	Portal TelemetryPortal
	FS     TelemetryFS
	// LookupGroup gives the gid of TelemetryGroup (os/user in the service).
	LookupGroup func(name string) (int, error)
	// Start starts the telemetry unit after a new token; nil — not started.
	Start func(ctx context.Context) error
	// Paths; "" — the defaults above.
	TokenPath, RejectedPath, SourcePath string
	Now                                 func() time.Time
	Log                                 *slog.Logger

	failures int
	next     time.Time
}

// Ensure asks the portal for a telemetry token when the token file is
// missing or unusable, the telemetry service marked the token rejected, or
// the labels token changed — and writes it. Failures are journal lines and a
// pause (TelemetryRetryFirst doubling to TelemetryRetryCeil); the labels tick
// goes on. Neither token reaches the journal.
func (p *TelemetryToken) Ensure(ctx context.Context, tok token.Token) {
	log := p.log()
	reason := p.reason(tok)
	if reason == "" {
		return
	}
	now := p.now()
	if now.Before(p.next) {
		log.Debug("токен телеметрии: повтор позже", "event", "telemetry_token", "reason", reason, "next_in", p.next.Sub(now))
		return
	}
	if err := p.fetch(ctx, tok, reason); err != nil {
		p.failures++
		p.next = now.Add(min(TelemetryRetryFirst<<min(p.failures-1, 10), TelemetryRetryCeil))
		log.Warn("токен телеметрии не получен — служба телеметрии ждёт", "event", "telemetry_token",
			"reason", reason, "err", err, "next_in", p.next.Sub(now))
		return
	}
	p.failures, p.next = 0, time.Time{}
}

// reason is why a token is needed: absent, rejected, rotated; "" — not needed.
func (p *TelemetryToken) reason(tok token.Token) string {
	data, _, err := p.FS.ReadNoFollow(p.tokenPath(), 4<<10)
	if err != nil || !validTelemetryFile(data) {
		return "absent"
	}
	if _, err := p.FS.Lstat(p.rejectedPath()); err == nil {
		return "rejected"
	}
	src, _, err := p.FS.ReadNoFollow(p.sourcePath(), 256)
	if err != nil || strings.TrimSpace(string(src)) != fingerprint(tok.Token) {
		return "rotated"
	}
	return ""
}

func (p *TelemetryToken) fetch(ctx context.Context, tok token.Token, reason string) error {
	base, ok := portal.RobotAPIBase(tok.URL)
	if !ok {
		return errors.New("адрес меток не https://<хост>[:порт]/robot-api/v1/labels")
	}
	// The group first: a token the portal issued and nobody can read would
	// still revoke the previous one.
	gid, err := p.LookupGroup(TelemetryGroup)
	if err != nil {
		return fmt.Errorf("группа %s: %w", TelemetryGroup, err)
	}
	resp, err := p.Portal.TelemetryToken(ctx, portal.Request{URL: tok.URL, Token: tok.Token})
	if err != nil {
		return err
	}
	if resp.Outcome != portal.OutcomeOK {
		if resp.Err != nil {
			return fmt.Errorf("портал: %s (HTTP %d): %w", resp.Outcome, resp.HTTPStatus, resp.Err)
		}
		return fmt.Errorf("портал: %s (HTTP %d)", resp.Outcome, resp.HTTPStatus)
	}
	path := p.tokenPath()
	dir := filepath.Dir(path)
	if err := p.FS.EnsureDir(dir, 0o750, true); err != nil {
		return fmt.Errorf("каталог %s: %w", dir, err)
	}
	if err := p.FS.Chown(dir, 0, gid); err != nil {
		return fmt.Errorf("каталог %s: %w", dir, err)
	}
	data := []byte("url=" + base + "\ntoken=" + string(resp.Token) + "\n")
	if err := p.FS.WriteAtomicOwned(path, data, 0o640, 0, gid); err != nil {
		return fmt.Errorf("файл токена телеметрии: %w", err)
	}
	if err := p.FS.Remove(p.rejectedPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		p.log().Warn("маркер отказа токена телеметрии не удалён", "event", "telemetry_token", "err", err)
	}
	// The fingerprint goes last: a crash before it only asks for one more
	// token; its failure pauses the requests like any other.
	src := p.sourcePath()
	err = p.FS.EnsureDir(filepath.Dir(src), 0o700, false)
	if err == nil {
		err = p.FS.WriteAtomic(src, []byte(fingerprint(tok.Token)+"\n"), 0o600)
	}
	if err != nil {
		return fmt.Errorf("токен записан, но отпечаток токена меток нет: %w", err)
	}
	p.log().Info("токен телеметрии получен и записан", "event", "telemetry_token", "reason", reason, "path", path)
	if p.Start != nil {
		if err := p.Start(ctx); err != nil {
			p.log().Warn("служба телеметрии не запущена", "event", "telemetry_token", "unit", TelemetryUnit, "err", err)
		}
	}
	return nil
}

// validTelemetryFile tells whether the token file has both lines; the token
// itself is checked by the telemetry service.
func validTelemetryFile(data []byte) bool {
	var url, tok bool
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		k, v, _ := strings.Cut(line, "=")
		switch {
		case k == "url" && v != "":
			url = true
		case k == "token" && v != "":
			tok = true
		}
	}
	return url && tok
}

func fingerprint(labelsToken string) string {
	sum := sha256.Sum256([]byte(labelsToken))
	return hex.EncodeToString(sum[:])
}

func (p *TelemetryToken) tokenPath() string    { return or(p.TokenPath, TelemetryTokenPath) }
func (p *TelemetryToken) rejectedPath() string { return or(p.RejectedPath, TelemetryRejectedPath) }
func (p *TelemetryToken) sourcePath() string   { return or(p.SourcePath, TelemetrySourcePath) }

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func (p *TelemetryToken) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *TelemetryToken) log() *slog.Logger {
	if p.Log != nil {
		return p.Log
	}
	return slog.New(slog.DiscardHandler)
}
