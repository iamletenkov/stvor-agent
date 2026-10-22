package inventory

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strconv"
	"strings"
	"syscall"
)

// reader reads the sources under root and collects the errors of sources
// that exist but cannot be read. A missing file or one only root may read is
// not an error: the field stays empty.
type reader struct {
	root fs.FS
	errs []error
}

func (r *reader) fail(err error) { r.errs = append(r.errs, err) }

// quiet reports errors that only mean "this source is not here for us".
func quiet(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) || errors.Is(err, fs.ErrInvalid)
}

// file reads name; ok is false when it is absent or unreadable.
func (r *reader) file(name string) (data []byte, ok bool) {
	data, err := fs.ReadFile(r.root, name)
	if err != nil {
		if !quiet(err) && !isSysfsReadError(err) {
			r.fail(fmt.Errorf("чтение /%s: %w", name, err))
		}
		return nil, false
	}
	return data, true
}

// isSysfsReadError: some sysfs attributes fail with EIO or EINVAL when the
// device does not implement them; that is "absent", not a failure.
func isSysfsReadError(err error) bool {
	return errors.Is(err, syscall.EIO) || errors.Is(err, syscall.EINVAL) ||
		errors.Is(err, syscall.ENODEV) || errors.Is(err, syscall.ENXIO) || errors.Is(err, syscall.EOPNOTSUPP)
}

// line is the cleaned content of a one-line attribute ("" when absent).
func (r *reader) line(name string) string {
	data, ok := r.file(name)
	if !ok {
		return ""
	}
	return clean(strings.TrimRight(string(data), "\x00\n"))
}

// int64 reads a decimal attribute.
func (r *reader) int64(name string) (int64, bool) {
	s := r.line(name)
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseInt(s, 10, 64)
	return v, err == nil
}

// exists reports whether name exists (following links).
func (r *reader) exists(name string) bool {
	_, err := fs.Stat(r.root, name)
	return err == nil
}

// dir lists name; nil when absent.
func (r *reader) dir(name string) []fs.DirEntry {
	ents, err := fs.ReadDir(r.root, name)
	if err != nil {
		if !quiet(err) {
			r.fail(fmt.Errorf("каталог /%s: %w", name, err))
		}
		return nil
	}
	return ents
}

// link is the target of the symbolic link name ("" when it is not a link).
func (r *reader) link(name string) string {
	t, err := fs.ReadLink(r.root, name)
	if err != nil {
		return ""
	}
	return t
}

// linkBase is the last element of a link target: the driver name of
// ".../drivers/r8169".
func (r *reader) linkBase(name string) string {
	t := r.link(name)
	if t == "" {
		return ""
	}
	return clean(path.Base(t))
}

// natCompare orders strings with embedded numbers naturally: sda2 < sda10,
// 1-1.2 < 1-1.10, usb2 < usb10.
func natCompare(a, b string) int {
	for a != "" && b != "" {
		da, db := isDigit(a[0]), isDigit(b[0])
		if da && db {
			na, ra := splitDigits(a)
			nb, rb := splitDigits(b)
			ta, tb := strings.TrimLeft(na, "0"), strings.TrimLeft(nb, "0")
			if len(ta) != len(tb) {
				return cmpInt(len(ta), len(tb))
			}
			if ta != tb {
				return strings.Compare(ta, tb)
			}
			a, b = ra, rb
			continue
		}
		if a[0] != b[0] {
			return cmpInt(int(a[0]), int(b[0]))
		}
		a, b = a[1:], b[1:]
	}
	return cmpInt(len(a), len(b))
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func splitDigits(s string) (digits, rest string) {
	i := 0
	for i < len(s) && isDigit(s[i]) {
		i++
	}
	return s[:i], s[i:]
}

func sortBy[T any](s []T, key func(T) string) {
	slices.SortStableFunc(s, func(a, b T) int { return natCompare(key(a), key(b)) })
}

func sortStrings(s []string) { slices.SortFunc(s, natCompare) }
