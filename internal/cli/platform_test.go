package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/iamletenkov/stvor-agent/internal/initsys"
	"github.com/iamletenkov/stvor-agent/internal/platform"
	"github.com/iamletenkov/stvor-agent/internal/reconcile"
	"github.com/iamletenkov/stvor-agent/internal/state"
	"github.com/iamletenkov/stvor-agent/internal/teleport"
	"github.com/iamletenkov/stvor-agent/internal/token"
)

// TestNewForFamily: the App of a machine takes its init system, paths and
// extension name from the family (plan Task 3); Debian is exactly the App of
// 4.x — the same constants, systemctl, `teleport`.
func TestNewForFamily(t *testing.T) {
	t.Parallel()
	deb := newFor(platform.Debian, "4.1.0", io.Discard)
	if _, ok := deb.Init.(initsys.Systemd); !ok {
		t.Errorf("Debian: Init = %T, want initsys.Systemd", deb.Init)
	}
	if deb.StatePath != state.DefaultPath || deb.TelemetryStateDir != TelemetryStateDir || deb.MachineIDPath != "" {
		t.Errorf("Debian paths: state %q, telemetry %q, machine-id %q",
			deb.StatePath, deb.TelemetryStateDir, deb.MachineIDPath)
	}
	if tp, ok := deb.Teleport.(*teleport.CreamTeleport); !ok || tp.Extension != "teleport" {
		t.Errorf("Debian: Teleport = %#v, want the teleport extension", deb.Teleport)
	}

	owr := newFor(platform.OpenWrt, "4.1.0", io.Discard)
	if _, ok := owr.Init.(initsys.Procd); !ok {
		t.Errorf("OpenWrt: Init = %T, want initsys.Procd", owr.Init)
	}
	if owr.StatePath != "/opt/stvor-agent/state.json" || owr.TelemetryStateDir != "/opt/stvor-agent/telemetry" ||
		owr.MachineIDPath != "/opt/stvor-agent/machine-id" {
		t.Errorf("OpenWrt paths: state %q, telemetry %q, machine-id %q",
			owr.StatePath, owr.TelemetryStateDir, owr.MachineIDPath)
	}
	if tp, ok := owr.Teleport.(*teleport.CreamTeleport); !ok || tp.Extension != "teleport-openwrt" {
		t.Errorf("OpenWrt: Teleport = %#v, want the teleport-openwrt extension", owr.Teleport)
	}
	// Tokens, the lock and the runtime directory stay where they are (spec §4.3).
	for _, a := range []*App{deb, owr} {
		if a.TokenPath != token.DefaultPath || a.TelemetryTokenPath != reconcile.TelemetryTokenPath ||
			a.Lock.Path != "/run/stvor-agent.lock" || a.TelemetryRunDir != TelemetryRunDir {
			t.Errorf("tokens/lock moved: %q %q %q %q", a.TokenPath, a.TelemetryTokenPath, a.Lock.Path, a.TelemetryRunDir)
		}
	}
}

var reMachineID = regexp.MustCompile(`^[0-9a-f]{32}\n$`)

// TestEnsureMachineID: on OpenWrt (no /etc/machine-id) `run` creates its own
// id once — 128 random bits as 32 hex, readable by the telemetry group — and
// never rewrites it (clones made from an image without it differ). Without
// MachineIDPath (Debian) nothing is written.
func TestEnsureMachineID(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.withTelemetryPortal()
	path := filepath.Join(e.root, "opt/stvor-agent/machine-id")
	e.app.MachineIDPath = path
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := e.app.ensureMachineID(); err != nil {
		t.Fatalf("ensureMachineID: %v", err)
	}
	first, err := os.ReadFile(path)
	if err != nil || !reMachineID.Match(first) {
		t.Fatalf("machine-id = %q, %v", first, err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v, want 0640", st.Mode().Perm())
	}
	if got := e.fs.owner(path); got != [2]int{0, 990} {
		t.Errorf("owner = %v, want root:stvor-telemetry", got)
	}
	if err := e.app.ensureMachineID(); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(path); !bytes.Equal(again, first) {
		t.Errorf("machine-id rewritten: %q → %q", first, again)
	}

	// Another machine gets another id.
	other := newEnv(t)
	other.withTelemetryPortal()
	otherPath := filepath.Join(other.root, "machine-id")
	other.app.MachineIDPath = otherPath
	if err := other.app.ensureMachineID(); err != nil {
		t.Fatal(err)
	}
	if second, _ := os.ReadFile(otherPath); bytes.Equal(second, first) {
		t.Error("two machines got the same id")
	}

	deb := newEnv(t)
	if err := deb.app.ensureMachineID(); err != nil {
		t.Errorf("Debian: %v", err)
	}
}

// TestEnsureMachineIDWithoutGroup: no stvor-telemetry group (the telemetry
// service is not installed) — the id is root's only, 0600.
func TestEnsureMachineIDWithoutGroup(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.app.LookupGroup = func(string) (int, error) { return 0, errors.New("unknown group") }
	path := filepath.Join(e.root, "machine-id")
	e.app.MachineIDPath = path
	if err := e.app.ensureMachineID(); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("stat = %v, %v; want 0600", st, err)
	}
	if got := e.fs.owner(path); got != [2]int{} {
		t.Errorf("owner recorded %v, want none", got)
	}
}

// TestRunCreatesMachineID: the service creates the id at start; a failure is
// logged and the service still runs.
func TestRunCreatesMachineID(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		dir    string // parent of machine-id under root
		mkdir  bool
		wantID bool
	}{
		{"created", "opt/stvor-agent", true, true},
		{"no directory", "missing/stvor-agent", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			e.withTelemetryPortal()
			path := filepath.Join(e.root, tc.dir, "machine-id")
			if tc.mkdir {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			e.app.MachineIDPath = path
			e.writeToken()
			e.sendOnNotify(syscall.SIGTERM)
			done := make(chan string, 1)
			go func() {
				code, _, errOut := e.run("", "run")
				if code != exitOK {
					t.Errorf("run code = %d", code)
				}
				done <- errOut
			}()
			var stderr string
			select {
			case stderr = <-done:
			case <-time.After(30 * time.Second):
				t.Fatal("run did not stop")
			}
			_, err := os.Stat(path)
			if tc.wantID != (err == nil) {
				t.Errorf("machine-id present = %t, want %t", err == nil, tc.wantID)
			}
			if !tc.wantID && !strings.Contains(stderr, "machine-id") {
				t.Errorf("no warning about machine-id in %q", stderr)
			}
		})
	}
}

// TestTelemetryMachineIDOwn: with MachineIDPath the hash is of that file only
// (OpenWrt); without it — of the system machine-id under root (Debian).
func TestTelemetryMachineIDOwn(t *testing.T) {
	t.Parallel()
	const id = "fedcba9876543210fedcba9876543210"
	sum := sha256.Sum256([]byte(id))
	path := filepath.Join(t.TempDir(), "machine-id")
	a := &App{MachineIDPath: path}
	if got := a.telemetryMachineIDHash(); got != "" {
		t.Errorf("before the file: %q", got)
	}
	if err := os.WriteFile(path, []byte(id+"\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if got := a.telemetryMachineIDHash(); got != hex.EncodeToString(sum[:]) {
		t.Errorf("hash = %q", got)
	}
	if got, want := (&App{}).telemetryMachineIDHash(), machineIDHashUnder("/"); got != want {
		t.Errorf("Debian: %q, want the hash of the system machine-id %q", got, want)
	}
}

// TestEnsureMachineIDGroupLater: an id made before the stvor-telemetry group
// existed (0600) goes to the group at the next start, its content kept.
func TestEnsureMachineIDGroupLater(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	path := filepath.Join(e.root, "machine-id")
	e.app.MachineIDPath = path
	if err := e.app.ensureMachineID(); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(path)
	e.withTelemetryPortal()
	if err := e.app.ensureMachineID(); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(path); !bytes.Equal(again, first) {
		t.Errorf("content changed: %q → %q", first, again)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v, want 0640", st.Mode().Perm())
	}
	if got := e.fs.owner(path); got != [2]int{0, 990} {
		t.Errorf("owner = %v, want root:stvor-telemetry", got)
	}
}

// TestMachineIDMetaLate: the telemetry service started before `run` made the
// id picks it up on a later batch, then keeps it.
func TestMachineIDMetaLate(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "machine-id")
	mid := (&App{MachineIDPath: path}).machineIDMeta()
	if got := mid(); got != "" {
		t.Fatalf("before the file: %q", got)
	}
	if err := os.WriteFile(path, []byte("00112233445566778899aabbccddeeff\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	h := mid()
	if len(h) != 64 {
		t.Fatalf("after the file: %q", h)
	}
	if err := os.WriteFile(path, []byte("ffeeddccbbaa99887766554433221100\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if got := mid(); got != h {
		t.Errorf("hash changed after it was known: %q → %q", h, got)
	}
}
