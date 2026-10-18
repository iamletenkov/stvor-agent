package cli

import (
	"io"
	"os"
	"syscall"
	"unsafe"
)

// isTerminal reports whether r is a terminal: an *os.File for which the
// TCGETS ioctl succeeds (the test of golang.org/x/term, without the
// dependency).
func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	var t syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), uintptr(syscall.TCGETS),
		uintptr(unsafe.Pointer(&t))) //nolint:gosec // ioctl needs the pointer
	return errno == 0
}
