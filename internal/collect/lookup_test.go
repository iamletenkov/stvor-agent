package collect

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// With Path empty, systemctl is looked up in the root search path; not found
// there — ErrAbsent, as on OpenWrt.
func TestSystemctlLooksUpTool(t *testing.T) {
	dir := t.TempDir()
	tool := filepath.Join(dir, "systemctl")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\necho 'a.service loaded failed failed A'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	old := findSystemctl
	t.Cleanup(func() { findSystemctl = old })
	var asked string
	findSystemctl = func(name string) (string, error) { asked = name; return tool, nil }

	got, err := Systemctl{}.FailedUnits(context.Background())
	if err != nil || !slices.Equal(got, []string{"a.service"}) || asked != "systemctl" {
		t.Errorf("FailedUnits = %q, %v (looked up %q)", got, err, asked)
	}

	findSystemctl = func(string) (string, error) { return "", &exec.Error{Name: "systemctl", Err: exec.ErrNotFound} }
	if _, err := (Systemctl{}).FailedUnits(context.Background()); !errors.Is(err, ErrAbsent) {
		t.Errorf("err = %v, want ErrAbsent", err)
	}
}
