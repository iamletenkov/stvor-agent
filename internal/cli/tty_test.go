package cli

import (
	"os"
	"strings"
	"testing"
)

func TestIsTerminal(t *testing.T) {
	t.Parallel()
	if isTerminal(strings.NewReader("x")) {
		t.Error("strings.Reader is a terminal")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close() //nolint:errcheck // test pipe
	defer w.Close() //nolint:errcheck // test pipe
	if isTerminal(r) {
		t.Error("a pipe is a terminal")
	}
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck // read-only
	if isTerminal(f) {
		t.Error("/dev/null is a terminal")
	}
}
