package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/iamletenkov/stvor-agent/internal/buffer"
	"github.com/iamletenkov/stvor-agent/internal/collect"
	"github.com/iamletenkov/stvor-agent/internal/daemon"
	"github.com/iamletenkov/stvor-agent/internal/inventory"
	"github.com/iamletenkov/stvor-agent/internal/reconcile"
	"github.com/iamletenkov/stvor-agent/internal/telemetry"
	"github.com/iamletenkov/stvor-agent/internal/version"
)

// Paths of the telemetry service (Stvor plan R4, Task 18): its state (the
// buffer, the token-rejected marker, the last boot_id) under StateDirectory=
// of its unit, the status for `status --json` on tmpfs (no eMMC wear).
const (
	TelemetryStateDir = "/var/lib/stvor-agent-telemetry"
	TelemetryRunDir   = "/run/stvor-agent-telemetry"

	telemetryStatusFile = "status.json"
	telemetryBootFile   = "boot-id"
	telemetryBufferDir  = "buffer"
	maxTelemetryStatus  = 16 << 10
)

// runTelemetry is `stvor-agent telemetry`, the ExecStart of
// stvor-agent-telemetry.service: never root, the loop until SIGTERM/SIGINT,
// then the buffer goes to disk and the exit is 0.
func (a *App) runTelemetry(stderr io.Writer) int {
	if a.Getuid() == 0 {
		_, _ = fmt.Fprintln(stderr, "stvor-agent: telemetry: не запускается от root — это служба "+
			reconcile.TelemetryUnit+" (User=stvor-telemetry)")
		return exitError
	}
	cfg, problems := daemon.LoadConfig(a.Getenv)
	log := newLogger(stderr, cfg.LogLevel)
	for _, p := range problems {
		log.Warn(p, "event", "config")
	}
	f := a.telemetryFiles()
	buf, err := buffer.Open(buffer.Config{Dir: filepath.Join(a.TelemetryStateDir, telemetryBufferDir),
		Now: a.Now, MonoMS: bootMS})
	if err != nil {
		log.Error("буфер телеметрии не открыт", "event", "start", "err", err)
		return exitError
	}
	mid := machineIDHashUnder("/")
	loop := &daemon.TelemetryLoop{
		Collector: machineCollector{c: collect.New(false)},
		Inventory: inventory.New(),
		Buffer:    buf,
		Now:       a.Now,
		MonoMS:    bootMS,
		LastBoot:  f.lastBoot,
		SaveBoot:  f.saveBoot,
		Report:    func(r daemon.TelemetryReport) { f.writeStatus(r, log) },
		Log:       log,
	}
	sender, err := telemetry.NewSender(telemetry.Options{
		Client: telemetry.ClientConfig{Version: a.Version},
		Tokens: f,
		Queue:  buf,
		Meta: func() telemetry.Meta {
			caps, failed := loop.Meta()
			return telemetry.Meta{AgentVersion: version.Short(a.Version), MachineIDHash: mid,
				BootID: loop.BootID(), Capabilities: caps, FailedUnits: failed}
		},
		Log:    log,
		Now:    a.Now,
		MonoMS: bootMS,
	})
	if err != nil {
		_ = buf.Close()
		log.Error("клиент телеметрии не создан", "event", "start", "err", err)
		return exitError
	}
	loop.Sender = sender

	sigs := make(chan os.Signal, 2)
	a.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	defer a.StopNotify(sigs)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-sigs:
			cancel()
		case <-ctx.Done():
		}
	}()
	log.Info("служба телеметрии запущена", "event", "start", "version", version.Short(a.Version))
	if err := loop.Run(ctx); err != nil {
		return exitError
	}
	return exitOK
}

// machineCollector puts per_core of the portal config into the collector.
type machineCollector struct{ c collect.Collector }

func (m machineCollector) Fast(ctx context.Context, perCore bool, s *collect.Snapshot) error {
	c := m.c
	c.PerCore = perCore
	return c.CollectFast(ctx, s)
}

func (m machineCollector) Slow(ctx context.Context, s *collect.Snapshot) error {
	return m.c.CollectSlow(ctx, s)
}

// telemetryFiles are the files of the telemetry service; it is the
// telemetry.TokenSource over the token file `run` writes.
type telemetryFiles struct {
	fs                            FS
	token, rejected, boot, status string
}

func (a *App) telemetryFiles() telemetryFiles {
	return telemetryFiles{
		fs:       a.FS,
		token:    a.TelemetryTokenPath,
		rejected: filepath.Join(a.TelemetryStateDir, filepath.Base(reconcile.TelemetryRejectedPath)),
		boot:     filepath.Join(a.TelemetryStateDir, telemetryBootFile),
		status:   filepath.Join(a.TelemetryRunDir, telemetryStatusFile),
	}
}

// Credentials implements telemetry.TokenSource: the file is re-read on every
// call, so a token `run` rotated is used at once.
func (f telemetryFiles) Credentials() (telemetry.Credentials, error) {
	data, _, err := f.fs.ReadNoFollow(f.token, 4<<10)
	if err != nil {
		return telemetry.Credentials{}, fmt.Errorf("файл токена телеметрии: %w", err)
	}
	return telemetry.ParseCredentials(data)
}

// Rejected implements telemetry.TokenSource: the marker for `run`.
func (f telemetryFiles) Rejected() error {
	return f.fs.WriteAtomic(f.rejected, []byte("rejected\n"), 0o600)
}

func (f telemetryFiles) lastBoot() string {
	data, _, err := f.fs.ReadNoFollow(f.boot, 64)
	if err != nil {
		return ""
	}
	return string(bytes.TrimSpace(data))
}

func (f telemetryFiles) saveBoot(id string) error {
	return f.fs.WriteAtomic(f.boot, []byte(id+"\n"), 0o600)
}

// telemetryStatus is the status file of the telemetry service, read by
// `status --json` (its "telemetry" block).
type telemetryStatus struct {
	At          *time.Time      `json:"at"`
	Outcome     string          `json:"outcome"`
	HTTPStatus  int             `json:"http_status"`
	ConfigHash  string          `json:"config_hash"`
	DetailUntil *time.Time      `json:"detail_until"`
	Buffer      telemetryBufDoc `json:"buffer"`
}

type telemetryBufDoc struct {
	UsedBytes  int64      `json:"used_bytes"`
	QuotaBytes int64      `json:"quota_bytes"`
	OldestAt   *time.Time `json:"oldest_at"`
	Segments   int        `json:"segments"`
	Events     int        `json:"events"`
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

// writeStatus replaces the status file; a failure is a debug line only.
func (f telemetryFiles) writeStatus(r daemon.TelemetryReport, log *slog.Logger) {
	st := telemetryStatus{
		At: timePtr(r.At), Outcome: string(r.Outcome), HTTPStatus: r.HTTPStatus,
		ConfigHash: r.ConfigHash, DetailUntil: timePtr(r.DetailUntil),
		Buffer: telemetryBufDoc{UsedBytes: r.Buffer.UsedBytes, QuotaBytes: r.Buffer.QuotaBytes,
			OldestAt: timePtr(r.Buffer.OldestAt), Segments: r.Buffer.Segments, Events: r.Buffer.Events},
	}
	data, err := json.Marshal(st)
	if err == nil {
		err = f.fs.WriteAtomic(f.status, append(data, '\n'), 0o644)
	}
	if err != nil {
		log.Debug("файл состояния телеметрии не записан", "event", "status", "err", err)
	}
}

// bootMS is CLOCK_BOOTTIME in ms from /proc/uptime (it counts suspend too);
// 0 when it is not readable.
func bootMS() int64 {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	f, _, _ := bytes.Cut(bytes.TrimSpace(data), []byte(" "))
	sec, err := strconv.ParseFloat(string(f), 64)
	if err != nil || sec < 0 {
		return 0
	}
	return int64(sec * 1000)
}

// machineIDHashUnder is the machine-id hash of the system under root:
// /etc/machine-id, then /var/lib/dbus/machine-id (images without systemd);
// "" without either — the batches then go without machine_id_hash.
func machineIDHashUnder(root string) string {
	for _, p := range []string{"etc/machine-id", "var/lib/dbus/machine-id"} {
		if h := machineIDHash(filepath.Join(root, p)); h != "" {
			return h
		}
	}
	return ""
}

// machineIDHash is sha256 hex of the machine-id (the clone detector of the
// portal); "" without the file.
func machineIDHash(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	id := bytes.TrimSpace(data)
	if len(id) == 0 {
		return ""
	}
	sum := sha256.Sum256(id)
	return hex.EncodeToString(sum[:])
}

// lookupGroupID is the gid of a group from the group database (pure Go:
// /etc/group in a static binary).
func lookupGroupID(name string) (int, error) {
	g, err := user.LookupGroup(name)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(g.Gid)
}

// telemetryDoc is the "telemetry" block of `status --json` (schema 1,
// additive).
type telemetryDoc struct {
	Service     string           `json:"service"`
	Token       string           `json:"token"`
	LastSend    *lastSendDoc     `json:"last_send"`
	Buffer      *telemetryBufDoc `json:"buffer"`
	DetailUntil *time.Time       `json:"detail_until"`
}

type lastSendDoc struct {
	At         time.Time `json:"at"`
	Outcome    string    `json:"outcome"`
	HTTPStatus int       `json:"http_status"`
}

// collectTelemetry fills the block: the unit, the token class (never the
// token) and the status file of the service.
func (a *App) collectTelemetry(ctx context.Context) telemetryDoc {
	f := a.telemetryFiles()
	doc := telemetryDoc{Service: a.Systemctl.IsActive(ctx, reconcile.TelemetryUnit), Token: "ok"}
	switch _, err := f.Credentials(); {
	case errors.Is(err, fs.ErrNotExist):
		doc.Token = "absent"
	case errors.Is(err, fs.ErrPermission):
		doc.Token = "no_access"
	case err != nil:
		doc.Token = "corrupt"
	default:
		if _, err := a.FS.Lstat(f.rejected); err == nil {
			doc.Token = "rejected"
		}
	}
	data, _, err := a.FS.ReadNoFollow(f.status, maxTelemetryStatus)
	if err != nil {
		return doc
	}
	var st telemetryStatus
	if json.Unmarshal(data, &st) != nil {
		return doc
	}
	doc.Buffer = &st.Buffer
	doc.DetailUntil = st.DetailUntil
	if st.At != nil && st.Outcome != "" {
		doc.LastSend = &lastSendDoc{At: st.At.UTC(), Outcome: st.Outcome, HTTPStatus: st.HTTPStatus}
	}
	return doc
}
