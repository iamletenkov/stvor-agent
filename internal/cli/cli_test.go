package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		args       []string
		version    string
		wantCode   int
		wantStdout string // exact when wantExact, otherwise a substring
		wantExact  bool
		wantStderr string
	}{
		{name: "version --short prints X.Y.Z", args: []string{"version", "--short"}, version: "4.0.0", wantCode: 0, wantStdout: "4.0.0\n", wantExact: true},
		{name: "version --short drops leading v", args: []string{"version", "--short"}, version: "v4.0.0", wantCode: 0, wantStdout: "4.0.0\n", wantExact: true},
		{name: "version --short without ldflags", args: []string{"version", "--short"}, version: "dev", wantCode: 0, wantStdout: "dev\n", wantExact: true},
		{name: "version long", args: []string{"version"}, version: "4.0.0", wantCode: 0, wantStdout: "stvor-agent 4.0.0"},
		{name: "version unknown flag", args: []string{"version", "--frob"}, version: "4.0.0", wantCode: 2, wantStderr: `"--frob"`},
		{name: "version extra args", args: []string{"version", "--short", "x"}, version: "4.0.0", wantCode: 2, wantStderr: "usage"},
		{name: "help", args: []string{"help"}, version: "4.0.0", wantCode: 0, wantStdout: "usage: stvor-agent"},
		{name: "help flag", args: []string{"--help"}, version: "4.0.0", wantCode: 0, wantStdout: "usage: stvor-agent"},
		{name: "no args", args: nil, version: "4.0.0", wantCode: 2, wantStderr: "usage: stvor-agent"},
		{name: "unknown subcommand", args: []string{"frobnicate"}, version: "4.0.0", wantCode: 2, wantStderr: `unknown command "frobnicate"`},
		{name: "known but not implemented", args: []string{"sync"}, version: "4.0.0", wantCode: 2, wantStderr: `"sync" is not implemented`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			code := run(tc.args, tc.version, strings.NewReader(""), &stdout, &stderr)
			if code != tc.wantCode {
				t.Errorf("exit code = %d, want %d", code, tc.wantCode)
			}
			switch {
			case tc.wantExact && stdout.String() != tc.wantStdout:
				t.Errorf("stdout = %q, want %q", stdout.String(), tc.wantStdout)
			case !strings.Contains(stdout.String(), tc.wantStdout):
				t.Errorf("stdout = %q, want it to contain %q", stdout.String(), tc.wantStdout)
			case tc.wantStdout == "" && stdout.Len() != 0:
				t.Errorf("stdout = %q, want empty", stdout.String())
			}
			if !strings.Contains(stderr.String(), tc.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), tc.wantStderr)
			}
			if tc.wantStderr == "" && stderr.Len() != 0 {
				t.Errorf("stderr = %q, want empty", stderr.String())
			}
		})
	}
}

// Run takes the version from internal/version (ldflags); without ldflags it is "dev".
func TestRunUsesBuildVersion(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"version", "--short"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	if got := stdout.String(); got != "dev\n" {
		t.Errorf("stdout = %q, want %q", got, "dev\n")
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("closed pipe") }

func TestRunStdoutWriteError(t *testing.T) {
	t.Parallel()
	var stderr bytes.Buffer
	if code := run([]string{"version", "--short"}, "4.0.0", strings.NewReader(""), failingWriter{}, &stderr); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
}
