package initsys

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// With Path empty, systemctl is looked up in the root search path (not the
// fixed /usr/bin/systemctl: images without usrmerge have only /bin/systemctl).
func TestSystemdLooksUpSystemctl(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "args")
	tool := filepath.Join(dir, "systemctl")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + out + "\necho active\n"
	if err := os.WriteFile(tool, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	var asked []string
	old := findTool
	t.Cleanup(func() { findTool = old })
	findTool = func(name string) (string, error) {
		asked = append(asked, name)
		return tool, nil
	}

	if got := (Systemd{}).IsActive(context.Background(), "x.service"); got != "active" {
		t.Errorf("IsActive = %q, want active", got)
	}
	if err := (Systemd{}).Enable(context.Background(), "x.service"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(out); string(b) != "enable\nx.service\n" {
		t.Errorf("argv = %q", b)
	}
	if len(asked) != 2 || asked[0] != "systemctl" {
		t.Errorf("looked up %q, want systemctl", asked)
	}

	findTool = func(string) (string, error) { return "", &exec.Error{Name: "systemctl", Err: exec.ErrNotFound} }
	err := (Systemd{}).Restart(context.Background(), "x.service")
	if !errors.Is(err, exec.ErrNotFound) || !strings.Contains(err.Error(), "systemctl restart x.service") {
		t.Errorf("Restart without systemctl: %v", err)
	}
	if got := (Systemd{}).IsActive(context.Background(), "x.service"); got != stateUnknown {
		t.Errorf("IsActive without systemctl = %q", got)
	}

	// An explicit Path is used as is, the lookup is not consulted.
	findTool = func(string) (string, error) { t.Error("lookup with explicit Path"); return "", nil }
	if err := (Systemd{Path: tool}).Start(context.Background(), "y.service"); err != nil {
		t.Fatal(err)
	}
}
