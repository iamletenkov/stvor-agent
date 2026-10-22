// Package platform tells the families of machines the agent runs on apart —
// Debian/Ubuntu (systemd, the 4.x contract) and OpenWrt (procd) — and gives
// the paths and the name of the teleport extension of each (spec OpenWrt
// §4.3, R8, R11). The family comes from os-release, as cream decides it.
package platform

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Family is a family of machines.
type Family string

// The families. Anything that is not OpenWrt is Debian: the behaviour of 4.x.
const (
	Debian  Family = "debian"
	OpenWrt Family = "openwrt"
)

// maxOSRelease bounds the read of os-release.
const maxOSRelease = 64 << 10

// openwrtIDs are the os-release IDs of OpenWrt (LEDE is its former name).
var openwrtIDs = []string{"openwrt", "lede"}

// Detect reads /etc/os-release under root (then /usr/lib/os-release, as
// os-release(5) says) and returns OpenWrt when ID or one of ID_LIKE is
// openwrt or lede, Debian otherwise — also without a readable file.
func Detect(root string) Family {
	for _, rel := range []string{"etc/os-release", "usr/lib/os-release"} {
		data, err := readLimited(filepath.Join(root, rel))
		if err != nil {
			continue
		}
		ids := osReleaseIDs(data)
		for _, id := range ids {
			if slices.Contains(openwrtIDs, id) {
				return OpenWrt
			}
		}
		return Debian
	}
	return Debian
}

func readLimited(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, maxOSRelease))
}

// osReleaseIDs is ID followed by the words of ID_LIKE, unquoted.
func osReleaseIDs(data []byte) []string {
	var id string
	var like []string
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		key, val, ok := strings.Cut(line, "=")
		if !ok || strings.HasPrefix(line, "#") {
			continue
		}
		val = strings.Trim(val, `"'`)
		switch key {
		case "ID":
			id = val
		case "ID_LIKE":
			like = strings.Fields(val)
		}
	}
	return append([]string{id}, like...)
}

// Layout is what differs between the families on the machine.
type Layout struct {
	// StatePath is state.json of the agent.
	StatePath string
	// TelemetryStateDir is the state of the telemetry service (buffer,
	// markers).
	TelemetryStateDir string
	// MachineIDPath is the agent's own machine-id, created at the first start
	// of the service; "" — the system one (/etc/machine-id).
	MachineIDPath string
	// TeleportExtension is the cream extension the agent applies labels with.
	TeleportExtension string
}

// Layout returns the layout of the family. OpenWrt keeps state under /opt:
// /var is tmpfs there.
func (f Family) Layout() Layout {
	if f == OpenWrt {
		return Layout{
			StatePath:         "/opt/stvor-agent/state.json",
			TelemetryStateDir: "/opt/stvor-agent/telemetry",
			MachineIDPath:     "/opt/stvor-agent/machine-id",
			TeleportExtension: "teleport-openwrt",
		}
	}
	return Layout{
		StatePath:         "/var/lib/stvor-agent/state.json",
		TelemetryStateDir: "/var/lib/stvor-agent-telemetry",
		TeleportExtension: "teleport",
	}
}
