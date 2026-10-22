package platform_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/iamletenkov/stvor-agent/internal/platform"
)

// osRelease writes content as <root>/<rel> and returns root.
func osRelease(t *testing.T, rel, content string) string {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// TestDetect: the family comes from ID and ID_LIKE of os-release under root
// (as cream decides it); anything that is not OpenWrt is Debian, the family
// of the existing behaviour.
func TestDetect(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, rel, content string
		want               platform.Family
	}{
		{"debian 12", "etc/os-release", "PRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\nID=debian\nVERSION_ID=\"12\"\n", platform.Debian},
		{"ubuntu", "etc/os-release", "NAME=\"Ubuntu\"\nID=ubuntu\nID_LIKE=debian\n", platform.Debian},
		{"openwrt 24", "etc/os-release", "NAME=\"OpenWrt\"\nVERSION=\"24.10.2\"\nID=\"openwrt\"\nID_LIKE=\"lede openwrt\"\n", platform.OpenWrt},
		{"openwrt unquoted", "etc/os-release", "ID=openwrt\n", platform.OpenWrt},
		{"lede", "etc/os-release", "ID='lede'\n", platform.OpenWrt},
		{"derivative of openwrt", "etc/os-release", "ID=\"routerx\"\nID_LIKE=\"lede openwrt\"\n", platform.OpenWrt},
		{"usr/lib fallback", "usr/lib/os-release", "ID=\"openwrt\"\n", platform.OpenWrt},
		{"comment and blank", "etc/os-release", "# ID=openwrt\n\nID=debian\n", platform.Debian},
		{"openwrt in NAME only", "etc/os-release", "NAME=openwrt\nID=debian\n", platform.Debian},
		{"no os-release", "etc/hostname", "robot\n", platform.Debian},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := osRelease(t, tc.rel, tc.content)
			if got := platform.Detect(root); got != tc.want {
				t.Errorf("Detect = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestDetectPrefersEtc: /etc/os-release wins over /usr/lib/os-release.
func TestDetectPrefersEtc(t *testing.T) {
	t.Parallel()
	root := osRelease(t, "etc/os-release", "ID=debian\n")
	if err := os.MkdirAll(filepath.Join(root, "usr/lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "usr/lib/os-release"), []byte("ID=openwrt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := platform.Detect(root); got != platform.Debian {
		t.Errorf("Detect = %q, want debian", got)
	}
}

// TestLayout: paths and the name of the teleport extension by family (plan
// Task 3, spec §4.3, R11). Debian keeps the 4.x contract as is.
func TestLayout(t *testing.T) {
	t.Parallel()
	tests := []struct {
		family platform.Family
		want   platform.Layout
	}{
		{platform.Debian, platform.Layout{
			StatePath:         "/var/lib/stvor-agent/state.json",
			TelemetryStateDir: "/var/lib/stvor-agent-telemetry",
			MachineIDPath:     "",
			TeleportExtension: "teleport",
		}},
		{platform.OpenWrt, platform.Layout{
			StatePath:         "/opt/stvor-agent/state.json",
			TelemetryStateDir: "/opt/stvor-agent/telemetry",
			MachineIDPath:     "/opt/stvor-agent/machine-id",
			TeleportExtension: "teleport-openwrt",
		}},
		{platform.Family("unknown"), platform.Layout{
			StatePath:         "/var/lib/stvor-agent/state.json",
			TelemetryStateDir: "/var/lib/stvor-agent-telemetry",
			TeleportExtension: "teleport",
		}},
	}
	for _, tc := range tests {
		if got := tc.family.Layout(); got != tc.want {
			t.Errorf("%q.Layout() = %+v, want %+v", tc.family, got, tc.want)
		}
	}
}
