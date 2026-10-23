package syspath

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func mkfile(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), mode); err != nil {
		t.Fatal(err)
	}
}

func TestFind(t *testing.T) {
	t.Parallel()
	usrSbin, usrBin, sbin, bin := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	dirs := []string{usrSbin, usrBin, sbin, bin}

	// systemctl only in /bin — Ubuntu 20.04 on Jetson without usrmerge.
	mkfile(t, filepath.Join(bin, "systemctl"), 0o755)
	// tool in /usr/bin and /bin — the first directory wins.
	mkfile(t, filepath.Join(usrBin, "both"), 0o755)
	mkfile(t, filepath.Join(bin, "both"), 0o755)
	// not executable in /usr/sbin, executable in /sbin.
	mkfile(t, filepath.Join(usrSbin, "noexec"), 0o644)
	mkfile(t, filepath.Join(sbin, "noexec"), 0o755)
	// a directory with the tool's name is skipped.
	if err := os.Mkdir(filepath.Join(usrSbin, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	mkfile(t, filepath.Join(sbin, "dir"), 0o755)
	// a symlink to an executable counts (/bin/systemctl → /lib/systemd/systemctl).
	if err := os.Symlink(filepath.Join(bin, "systemctl"), filepath.Join(sbin, "link")); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ name, want string }{
		{"systemctl", filepath.Join(bin, "systemctl")},
		{"both", filepath.Join(usrBin, "both")},
		{"noexec", filepath.Join(sbin, "noexec")},
		{"dir", filepath.Join(sbin, "dir")},
		{"link", filepath.Join(sbin, "link")},
	} {
		got, err := find(tc.name, dirs)
		if err != nil || got != tc.want {
			t.Errorf("find(%q) = %q, %v; want %q", tc.name, got, err, tc.want)
		}
	}
	for _, name := range []string{"absent", "", "../bin/systemctl", "/bin/sh", "sub/x"} {
		if got, err := find(name, dirs); !errors.Is(err, exec.ErrNotFound) {
			t.Errorf("find(%q) = %q, %v; want ErrNotFound", name, got, err)
		}
	}
}

// The caller's PATH is never consulted: a tool planted in a directory of
// $PATH outside Dirs is not found.
func TestFindIgnoresEnvPath(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, filepath.Join(dir, "stvor-planted-tool"), 0o755)
	t.Setenv("PATH", dir)
	if got, err := Find("stvor-planted-tool"); !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("Find = %q, %v; want ErrNotFound", got, err)
	}
}

func TestDirs(t *testing.T) {
	t.Parallel()
	if Dirs != "/usr/sbin:/usr/bin:/sbin:/bin" {
		t.Errorf("Dirs = %q", Dirs)
	}
	if got, err := Find("sh"); err != nil || (got != "/usr/bin/sh" && got != "/bin/sh") {
		t.Errorf("Find(sh) = %q, %v", got, err)
	}
}
