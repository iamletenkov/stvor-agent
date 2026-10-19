// Package atomicfile holds the file primitives the agent's secret and state
// files are built on (spec §5.5, §6): an atomic replace (temp file in the same
// directory → fsync → rename → fsync of the directory) and a read that never
// follows a symlink and checks the very descriptor it reads (no Lstat+Open
// race).
//
// OS is the real implementation; token and state take it through their own
// small interfaces so tests can substitute the owner of a file.
package atomicfile

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// Errors of ReadNoFollow and EnsureDir. Match with errors.Is.
var (
	ErrNotRegular = errors.New("не обычный файл")
	ErrNotDir     = errors.New("не каталог")
	ErrTooBig     = errors.New("файл больше допустимого")
)

// Info is what the agent checks about a file: its type and permission bits
// and its owner.
type Info struct {
	Mode fs.FileMode
	UID  uint32
}

// OS is the file system of the machine.
type OS struct{}

// WriteAtomic replaces path with data. The file appears with mode at once
// (never with wider rights), and either the old or the new content survives a
// crash at any point.
func (OS) WriteAtomic(path string, data []byte, mode fs.FileMode) error {
	return write(path, data, mode, ops{rename: os.Rename, syncDir: syncDir})
}

// ReadNoFollow reads a regular file of at most limit bytes. It opens path with
// O_NOFOLLOW (a symlink is refused) and O_NONBLOCK (a FIFO does not hang the
// agent) and takes Info from fstat of the same descriptor.
func (OS) ReadNoFollow(path string, limit int64) ([]byte, Info, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, Info{}, fmt.Errorf("%s: символическая ссылка: %w", path, ErrNotRegular)
		}
		return nil, Info{}, err
	}
	defer f.Close() //nolint:errcheck // read-only descriptor
	st, err := f.Stat()
	if err != nil {
		return nil, Info{}, err
	}
	info := infoOf(st)
	if !st.Mode().IsRegular() {
		return nil, info, fmt.Errorf("%s: %w", path, ErrNotRegular)
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, info, fmt.Errorf("%s: чтение: %w", path, err)
	}
	if int64(len(data)) > limit {
		return nil, info, fmt.Errorf("%s: больше %d байт: %w", path, limit, ErrTooBig)
	}
	return data, info, nil
}

// Lstat describes path itself, not what a symlink points to.
func (OS) Lstat(path string) (Info, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return Info{}, err
	}
	return infoOf(st), nil
}

// EnsureDir creates dir with mode if it is absent (the parent must exist). An
// existing dir must be a real directory, not a symlink; fixMode also narrows
// its permission bits to mode.
func (OS) EnsureDir(dir string, mode fs.FileMode, fixMode bool) error {
	err := os.Mkdir(dir, mode)
	switch {
	case err == nil:
		// Mkdir is subject to umask; set the bits explicitly.
		return os.Chmod(dir, mode)
	case !errors.Is(err, fs.ErrExist):
		return err
	}
	st, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("%s: %w", dir, ErrNotDir)
	}
	if fixMode && st.Mode().Perm() != mode {
		return os.Chmod(dir, mode)
	}
	return nil
}

func infoOf(st fs.FileInfo) Info {
	info := Info{Mode: st.Mode()}
	if sys, ok := st.Sys().(*syscall.Stat_t); ok {
		info.UID = sys.Uid
	}
	return info
}

// ops are the steps of write a test can break.
type ops struct {
	rename   func(oldpath, newpath string) error
	syncDir  func(dir string) error
	syncFile func(f *os.File) error // nil: f.Sync
}

func write(path string, data []byte, mode fs.FileMode, o ops) (err error) {
	dir, base := filepath.Split(path)
	if dir == "" {
		dir = "."
	}
	// CreateTemp opens with O_EXCL and 0600: nobody else can read or swap it.
	f, err := os.CreateTemp(dir, "."+base+".tmp-*")
	if err != nil {
		return fmt.Errorf("%s: временный файл: %w", path, err)
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(tmp)
		}
	}()
	if err = f.Chmod(mode); err != nil {
		return fmt.Errorf("%s: права: %w", path, err)
	}
	if _, err = f.Write(data); err != nil {
		return fmt.Errorf("%s: запись: %w", path, err)
	}
	sync := o.syncFile
	if sync == nil {
		sync = (*os.File).Sync
	}
	if err = sync(f); err != nil {
		return fmt.Errorf("%s: fsync: %w", path, err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("%s: закрытие: %w", path, err)
	}
	if err = o.rename(tmp, path); err != nil {
		return fmt.Errorf("%s: rename: %w", path, err)
	}
	// After rename the temp name is gone; a failed directory fsync must not
	// remove the new file.
	if serr := o.syncDir(dir); serr != nil {
		return fmt.Errorf("%s: fsync каталога: %w", path, serr)
	}
	return nil
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	if err := d.Sync(); err != nil {
		_ = d.Close()
		return err
	}
	return d.Close()
}
