package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamletenkov/stvor-agent/internal/buffer"
	"github.com/iamletenkov/stvor-agent/internal/daemon"
	"github.com/iamletenkov/stvor-agent/internal/portal"
	"github.com/iamletenkov/stvor-agent/internal/telemetry"
)

const telToken = "telemetry_TOKEN-0123456789abcdefghijklmnop"

type fakeTelPortal struct {
	mu   sync.Mutex
	reqs []portal.Request
}

func (f *fakeTelPortal) TelemetryToken(_ context.Context, r portal.Request) (portal.TokenResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, r)
	return portal.TokenResponse{Outcome: portal.OutcomeOK, HTTPStatus: 200, Token: telToken}, nil
}

func (e *env) withTelemetryPortal() *fakeTelPortal {
	p := &fakeTelPortal{}
	e.app.TelemetryPortal = p
	e.app.LookupGroup = func(name string) (int, error) {
		if name != "stvor-telemetry" {
			e.t.Errorf("group %q", name)
		}
		return 990, nil
	}
	return p
}

// `sync` (the tick of `run`) with an accepted labels token writes the
// telemetry token for the telemetry service and starts it; the token never
// shows up in outputs or systemctl argv.
func TestTickWritesTelemetryToken(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	tp := e.withTelemetryPortal()
	e.writeToken()
	code, stdout, stderr := e.run("", "sync")
	if code != 0 {
		t.Fatalf("sync = %d, stderr %q", code, stderr)
	}
	data, err := os.ReadFile(e.app.TelemetryTokenPath)
	if err != nil {
		t.Fatalf("telemetry token file: %v", err)
	}
	if want := "url=https://stvor.example/robot-api/v1\ntoken=" + telToken + "\n"; string(data) != want {
		t.Errorf("file = %q, want %q", data, want)
	}
	if got := e.fs.owner(e.app.TelemetryTokenPath); got != [2]int{0, 990} {
		t.Errorf("owner = %v, want root:990", got)
	}
	if !slices.Contains(e.sys.Calls(), "start stvor-agent-telemetry.service") {
		t.Errorf("systemctl = %q, want start of the telemetry unit", e.sys.Calls())
	}
	if len(tp.reqs) != 1 || tp.reqs[0].Token != testToken {
		t.Errorf("telemetry-token requests %d", len(tp.reqs))
	}
	e.assertNoToken(stdout, stderr)
	if strings.Contains(stdout+stderr+strings.Join(e.sys.Calls(), " "), telToken) {
		t.Error("telemetry token leaked")
	}

	// disable removes it and stops the unit.
	if code, _, stderr := e.run("", "disable"); code != 0 {
		t.Fatalf("disable = %d, %q", code, stderr)
	}
	if exists(e.app.TelemetryTokenPath) {
		t.Error("disable left the telemetry token")
	}
}

func TestTelemetryCommandRefusals(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	if code, _, stderr := e.run("", "telemetry"); code != exitError || !strings.Contains(stderr, "root") {
		t.Errorf("as root: code %d, stderr %q; want refusal", code, stderr)
	}
	e.uid = 1000
	if code, _, _ := e.run("", "telemetry", "x"); code != exitUsage {
		t.Errorf("with an argument: code %d, want %d", code, exitUsage)
	}
	if len(e.sys.Calls()) != 0 {
		t.Error("a refused telemetry touched systemctl")
	}
}

// The token source of the service: re-read on every call, a marker on
// rejection; the status block reads the class and the status file.
func TestTelemetryFilesAndStatus(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	f := e.app.telemetryFiles()
	for _, d := range []string{filepath.Dir(f.token), e.app.TelemetryStateDir, e.app.TelemetryRunDir} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if doc := e.app.collectTelemetry(context.Background()); doc.Token != "absent" || doc.LastSend != nil {
		t.Errorf("no files: %+v", doc)
	}
	write := func(s string) {
		if err := os.WriteFile(f.token, []byte(s), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	write("url=https://stvor.example/robot-api/v1\ntoken=" + telToken + "\n")
	cr, err := f.Credentials()
	if err != nil || cr.Token != telToken || cr.BaseURL != "https://stvor.example/robot-api/v1" {
		t.Fatalf("Credentials = %+v, %v", cr.BaseURL, err)
	}
	if doc := e.app.collectTelemetry(context.Background()); doc.Token != "ok" {
		t.Errorf("token = %q, want ok", doc.Token)
	}
	if err := f.Rejected(); err != nil {
		t.Fatal(err)
	}
	if doc := e.app.collectTelemetry(context.Background()); doc.Token != "rejected" {
		t.Errorf("token = %q, want rejected", doc.Token)
	}
	write("token=" + telToken + "\n")
	if doc := e.app.collectTelemetry(context.Background()); doc.Token != "corrupt" {
		t.Errorf("token = %q, want corrupt", doc.Token)
	}

	at := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	f.writeStatus(daemon.TelemetryReport{At: at, Outcome: telemetry.OutcomeSent, HTTPStatus: 200,
		Buffer: buffer.Stats{UsedBytes: 10, QuotaBytes: 256 << 20}}, newLogger(os.Stderr, 0))
	if st, _ := os.Stat(f.status); st == nil || st.Mode().Perm() != 0o644 {
		t.Errorf("status file %v, want 0644", st)
	}
	doc := e.app.collectTelemetry(context.Background())
	if doc.LastSend == nil || !doc.LastSend.At.Equal(at) || doc.LastSend.Outcome != "sent" || doc.Buffer.UsedBytes != 10 {
		t.Errorf("status block %+v", doc)
	}
	out, _ := json.Marshal(doc)
	if strings.Contains(string(out), telToken) {
		t.Error("status shows the telemetry token")
	}

	if f.lastBoot() != "" {
		t.Error("boot id before save")
	}
	if err := f.saveBoot("abc"); err != nil || f.lastBoot() != "abc" {
		t.Errorf("boot id round trip: %q, %v", f.lastBoot(), err)
	}
}

func TestMachineIDHash(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "machine-id")
	if machineIDHash(path) != "" {
		t.Error("hash without the file")
	}
	_ = os.WriteFile(path, []byte("0123456789abcdef0123456789abcdef\n"), 0o444)
	if h := machineIDHash(path); len(h) != 64 || h != machineIDHash(path) {
		t.Errorf("hash = %q", h)
	}
}

// The machine-id hash: /etc/machine-id, then /var/lib/dbus/machine-id
// (images without systemd); an empty or missing file gives "" — the batch
// then goes without machine_id_hash.
func TestTelemetryMachineIDHash(t *testing.T) {
	t.Parallel()
	const id = "0123456789abcdef0123456789abcdef"
	sum := sha256.Sum256([]byte(id))
	want := hex.EncodeToString(sum[:])
	for _, tc := range []struct {
		name       string
		etc, dbus  *string
		wantHashed bool
	}{
		{"etc", ptr(id + "\n"), nil, true},
		{"dbus_fallback", nil, ptr(id + "\n"), true},
		{"empty_etc_falls_back", ptr("\n"), ptr(id), true},
		{"etc_wins", ptr(id), ptr("ffffffffffffffffffffffffffffffff"), true},
		{"none", nil, nil, false},
		{"both_empty", ptr(""), ptr("  \n"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			for path, data := range map[string]*string{"etc/machine-id": tc.etc, "var/lib/dbus/machine-id": tc.dbus} {
				if data == nil {
					continue
				}
				p := filepath.Join(root, path)
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(*data), 0o444); err != nil {
					t.Fatal(err)
				}
			}
			got := machineIDHashUnder(root)
			if tc.wantHashed && got != want || !tc.wantHashed && got != "" {
				t.Errorf("machineIDHashUnder = %q, want hashed %t", got, tc.wantHashed)
			}
		})
	}
}

func ptr(s string) *string { return &s }
