package atomicfile

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestWriteCreatesFileWithMode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		mode fs.FileMode
	}{
		{"owner only", 0o600},
		{"read only", 0o400},
		{"world readable", 0o644},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "f")
			if err := (OS{}).WriteAtomic(path, []byte("data\n"), tc.mode); err != nil {
				t.Fatalf("WriteAtomic: %v", err)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != "data\n" {
				t.Fatalf("content = %q, %v", got, err)
			}
			st, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if st.Mode().Perm() != tc.mode {
				t.Errorf("mode = %o, want %o", st.Mode().Perm(), tc.mode)
			}
			assertNoTemp(t, filepath.Dir(path))
		})
	}
}

func TestWriteReplacesExisting(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := (OS{}).WriteAtomic(path, []byte("new"), 0o600); err != nil {
		t.Fatalf("WriteAtomic: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "new" {
		t.Errorf("content = %q, want new", got)
	}
	st, _ := os.Lstat(path)
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 600", st.Mode().Perm())
	}
}

// A symlink at the target is replaced by the file itself: rename does not
// follow it, so the write never lands where the link points.
func TestWriteReplacesSymlinkNotTarget(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "f")
	if err := os.Symlink(victim, path); err != nil {
		t.Fatal(err)
	}
	if err := (OS{}).WriteAtomic(path, []byte("new"), 0o600); err != nil {
		t.Fatalf("WriteAtomic: %v", err)
	}
	if got, _ := os.ReadFile(victim); string(got) != "keep" {
		t.Errorf("symlink target changed: %q", got)
	}
	st, _ := os.Lstat(path)
	if !st.Mode().IsRegular() {
		t.Errorf("path is %v, want a regular file", st.Mode())
	}
}

// A failure between writing the temp file and rename leaves the previous file
// intact and no temp file behind.
func TestWriteFailureKeepsPrevious(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	tests := []struct {
		name string
		ops  ops
	}{
		{"rename fails", ops{rename: func(string, string) error { return boom }, syncDir: syncDir}},
		{"sync file fails", ops{rename: os.Rename, syncDir: syncDir, syncFile: func(*os.File) error { return boom }}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "f")
			if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
				t.Fatal(err)
			}
			err := write(path, []byte("new"), 0o600, tc.ops)
			if !errors.Is(err, boom) {
				t.Fatalf("err = %v, want boom", err)
			}
			if got, _ := os.ReadFile(path); string(got) != "old" {
				t.Errorf("previous file changed: %q", got)
			}
			assertNoTemp(t, dir)
		})
	}
}

// A failed fsync of the directory is reported: the rename may not survive a
// power loss, the caller must know.
func TestWriteSyncDirFailureReported(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	path := filepath.Join(t.TempDir(), "f")
	err := write(path, []byte("new"), 0o600, ops{rename: os.Rename, syncDir: func(string) error { return boom }})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
}

func TestWriteMissingDir(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "absent", "f")
	if err := (OS{}).WriteAtomic(path, []byte("x"), 0o600); err == nil {
		t.Fatal("want an error for a missing directory")
	}
}

func TestReadNoFollow(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	reg := filepath.Join(dir, "reg")
	if err := os.WriteFile(reg, []byte("hello"), 0o640); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(reg, link); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	big := filepath.Join(dir, "big")
	if err := os.WriteFile(big, []byte(strings.Repeat("x", 11)), 0o600); err != nil {
		t.Fatal(err)
	}

	data, info, err := (OS{}).ReadNoFollow(reg, 10)
	if err != nil {
		t.Fatalf("regular: %v", err)
	}
	if string(data) != "hello" || info.Mode.Perm() != 0o640 || !info.Mode.IsRegular() {
		t.Errorf("regular: data %q, mode %v", data, info.Mode)
	}
	if info.UID != uint32(os.Getuid()) {
		t.Errorf("uid = %d, want %d", info.UID, os.Getuid())
	}

	tests := []struct {
		name string
		path string
		want error
	}{
		{"absent", filepath.Join(dir, "absent"), fs.ErrNotExist},
		{"symlink", link, ErrNotRegular},
		{"fifo does not block", fifo, ErrNotRegular},
		{"too big", big, ErrTooBig},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := (OS{}).ReadNoFollow(tc.path, 10)
			if !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestLstat(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	info, err := (OS{}).Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode.IsDir() || info.Mode.Perm() != 0o700 {
		t.Errorf("mode = %v", info.Mode)
	}
	if _, err := (OS{}).Lstat(filepath.Join(dir, "absent")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("absent: err = %v", err)
	}
}

func TestEnsureDir(t *testing.T) {
	t.Parallel()
	base := t.TempDir()

	fresh := filepath.Join(base, "fresh")
	if err := (OS{}).EnsureDir(fresh, 0o700, false); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Lstat(fresh); st.Mode().Perm() != 0o700 {
		t.Errorf("fresh: mode %o", st.Mode().Perm())
	}

	wide := filepath.Join(base, "wide")
	if err := os.Mkdir(wide, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := (OS{}).EnsureDir(wide, 0o700, false); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Lstat(wide); st.Mode().Perm() != 0o755 {
		t.Errorf("wide without fix: mode %o, want untouched 755", st.Mode().Perm())
	}
	if err := (OS{}).EnsureDir(wide, 0o700, true); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Lstat(wide); st.Mode().Perm() != 0o700 {
		t.Errorf("wide with fix: mode %o, want 700", st.Mode().Perm())
	}

	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := (OS{}).EnsureDir(link, 0o700, true); !errors.Is(err, ErrNotDir) {
		t.Errorf("symlink: err = %v, want ErrNotDir", err)
	}

	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (OS{}).EnsureDir(file, 0o700, true); !errors.Is(err, ErrNotDir) {
		t.Errorf("file: err = %v, want ErrNotDir", err)
	}
}

func assertNoTemp(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

// WriteAtomicOwned sets the owner before the rename; a chown failure leaves
// the previous file and no temp file.
func TestWriteAtomicOwned(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	if err := (OS{}).WriteAtomicOwned(path, []byte("new\n"), 0o640, os.Getuid(), os.Getgid()); err != nil {
		t.Fatalf("WriteAtomicOwned: %v", err)
	}
	st, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	sys := st.Sys().(*syscall.Stat_t)
	if st.Mode().Perm() != 0o640 || int(sys.Uid) != os.Getuid() || int(sys.Gid) != os.Getgid() {
		t.Errorf("mode %o owner %d:%d", st.Mode().Perm(), sys.Uid, sys.Gid)
	}

	boom := errors.New("boom")
	err = write(path, []byte("other\n"), 0o640, ops{rename: os.Rename, syncDir: syncDir,
		chown: func(*os.File) error { return boom }})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "new\n" {
		t.Errorf("previous content lost: %q", got)
	}
	assertNoTemp(t, dir)
	if err := (OS{}).Chown(path, os.Getuid(), os.Getgid()); err != nil {
		t.Errorf("Chown: %v", err)
	}
}
