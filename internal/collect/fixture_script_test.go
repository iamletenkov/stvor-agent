package collect

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// runCaptureScript runs tools/collect-fixture.sh with ROOT=root into a new
// directory and returns it.
func runCaptureScript(t *testing.T, root string) string {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not found")
	}
	out := filepath.Join(t.TempDir(), "fixture")
	cmd := exec.Command("bash", "../../tools/collect-fixture.sh", out)
	cmd.Env = append(os.Environ(), "ROOT="+root)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("collect-fixture.sh: %v\n%s", err, b)
	}
	return out
}

func pointSet(t *testing.T, root string) []string {
	t.Helper()
	c := Collector{Root: os.DirFS(root)}
	c.Clock, c.FailedUnits = clockOK(time.Millisecond), units("a.service")
	c.Statfs = func(string) (FSStat, error) { return FSStat{Size: 10, Free: 5, Avail: 4, Files: 3, FilesFree: 1}, nil }
	var s Snapshot
	if err := c.Collect(context.Background(), &s); err != nil {
		t.Fatalf("Collect %s: %v", root, err)
	}
	out := make([]string, 0, len(s.Points))
	for _, p := range s.Points {
		out = append(out, fmt.Sprintf("%s=%g", key(p), p.Value))
	}
	slices.Sort(out)
	return append(out, strings.Join(s.Capabilities, ","))
}

// TestCollectFixtureScript: a capture by tools/collect-fixture.sh of a
// fixture root gives the collector the same points as the root itself — the
// script copies every file the collector reads.
func TestCollectFixtureScript(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"jetson-nano-r32", "pve-vm", "debian12-amd64", "pve-robot-debian13"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			src := filepath.Join("testdata", name)
			got, want := pointSet(t, runCaptureScript(t, src)), pointSet(t, src)
			if !slices.Equal(got, want) {
				for _, w := range want {
					if !slices.Contains(got, w) {
						t.Errorf("missing in the capture: %s", w)
					}
				}
				for _, g := range got {
					if !slices.Contains(want, g) {
						t.Errorf("extra in the capture: %s", g)
					}
				}
			}
		})
	}
}

// TestCollectFixtureScriptMasks: user names (uid ≥ 1000 in etc/passwd) leave
// mountinfo, the boot_id is a placeholder, nothing that names the machine is
// copied.
func TestCollectFixtureScriptMasks(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	files := map[string]string{
		"etc/passwd":                      "root:x:0:0::/root:/bin/sh\nalice:x:1000:1000::/home/alice:/bin/sh\n",
		"etc/hostname":                    "robot-secret-name\n",
		"proc/sys/kernel/random/boot_id":  "11111111-2222-3333-4444-555555555555\n",
		"proc/self/mountinfo":             "1 0 8:1 / / rw - ext4 /dev/sda1 rw\n2 1 8:2 / /home/alice/data rw - ext4 /dev/sda2 rw\n3 1 8:3 / /media/alice rw - vfat /dev/sdb1 rw\n",
		"sys/class/net/eth0/address":      "bc:24:11:12:34:56\n",
		"sys/class/net/eth0/mtu":          "1500\n",
		"sys/class/dmi/id/product_serial": "SERIAL-1\n",
	}
	for p, body := range files {
		if err := os.MkdirAll(filepath.Join(root, filepath.Dir(p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, p), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out := runCaptureScript(t, root)
	mi, err := os.ReadFile(filepath.Join(out, "proc/self/mountinfo"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(mi), "alice") || !strings.Contains(string(mi), "/home/user/data") ||
		!strings.Contains(string(mi), "/media/user ") {
		t.Errorf("mountinfo not masked:\n%s", mi)
	}
	boot, _ := os.ReadFile(filepath.Join(out, "proc/sys/kernel/random/boot_id"))
	if strings.Contains(string(boot), "1111") {
		t.Errorf("real boot_id copied: %s", boot)
	}
	for _, p := range []string{"etc/hostname", "etc/passwd", "sys/class/net/eth0/address", "sys/class/dmi/id/product_serial"} {
		if _, err := os.Stat(filepath.Join(out, p)); err == nil {
			t.Errorf("%s copied", p)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(out, "sys/class/net/eth0/mtu")); string(b) != "1500\n" {
		t.Errorf("mtu not copied: %q", b)
	}
}
