package cli

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"sync"

	"github.com/iamletenkov/stvor-agent/internal/reconcile"
)

// ensureMachineID creates the agent's own machine-id at MachineIDPath when it
// is absent (spec OpenWrt §4.3): 128 random bits as 32 hex, the format of
// machine-id(5). It is never rewritten, so a clone of an image made before the
// first start gets an id of its own. The telemetry service (its group) reads
// it: 0640 root:stvor-telemetry, or 0600 root without that group. Without
// MachineIDPath (Debian: /etc/machine-id) it does nothing.
func (a *App) ensureMachineID() error {
	if a.MachineIDPath == "" {
		return nil
	}
	gid, group := a.telemetryGID()
	info, err := a.FS.Lstat(a.MachineIDPath)
	switch {
	case err == nil:
		// Created before the group existed: hand it to the group now, the
		// content stays.
		if !group || info.Mode.Perm() != 0o600 {
			return nil
		}
		data, _, err := a.FS.ReadNoFollow(a.MachineIDPath, maxMachineID)
		if err != nil {
			return fmt.Errorf("machine-id: %w", err)
		}
		return a.FS.WriteAtomicOwned(a.MachineIDPath, data, 0o640, 0, gid)
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return fmt.Errorf("machine-id: %w", err)
	}
	data := []byte(hex.EncodeToString(id[:]) + "\n")
	if group {
		return a.FS.WriteAtomicOwned(a.MachineIDPath, data, 0o640, 0, gid)
	}
	return a.FS.WriteAtomic(a.MachineIDPath, data, 0o600)
}

// maxMachineID bounds the read of an existing machine-id.
const maxMachineID = 4096

// telemetryGID is the gid of the telemetry group; false — no such group.
func (a *App) telemetryGID() (int, bool) {
	if a.LookupGroup == nil {
		return 0, false
	}
	gid, err := a.LookupGroup(reconcile.TelemetryGroup)
	return gid, err == nil
}

// telemetryMachineIDHash is the machine_id_hash of the batches: of the
// agent's own id with MachineIDPath, of the system machine-id otherwise.
func (a *App) telemetryMachineIDHash() string {
	if a.MachineIDPath != "" {
		return machineIDHash(a.MachineIDPath)
	}
	return machineIDHashUnder("/")
}

// machineIDMeta returns the machine_id_hash for each batch. The system id is
// read once (Debian, as before); the agent's own id is read again while it is
// absent — the telemetry service may start before `run` has created it.
func (a *App) machineIDMeta() func() string {
	h := a.telemetryMachineIDHash()
	if a.MachineIDPath == "" {
		return func() string { return h }
	}
	var mu sync.Mutex
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		if h == "" {
			h = a.telemetryMachineIDHash()
		}
		return h
	}
}
