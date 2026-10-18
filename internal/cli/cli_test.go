package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestRunDispatch(t *testing.T) {
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
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			e.app.Version = tc.version
			code, stdout, stderr := e.run("", tc.args...)
			if code != tc.wantCode {
				t.Errorf("exit code = %d, want %d", code, tc.wantCode)
			}
			switch {
			case tc.wantExact && stdout != tc.wantStdout:
				t.Errorf("stdout = %q, want %q", stdout, tc.wantStdout)
			case !strings.Contains(stdout, tc.wantStdout):
				t.Errorf("stdout = %q, want it to contain %q", stdout, tc.wantStdout)
			case tc.wantStdout == "" && stdout != "":
				t.Errorf("stdout = %q, want empty", stdout)
			}
			if !strings.Contains(stderr, tc.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr, tc.wantStderr)
			}
			if tc.wantStderr == "" && stderr != "" {
				t.Errorf("stderr = %q, want empty", stderr)
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
	e := newEnv(t)
	var stderr bytes.Buffer
	if code := e.app.Run([]string{"version", "--short"}, strings.NewReader(""), failingWriter{}, &stderr); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
}

// Usage errors are 2 for every command, before anything is touched.
func TestUsageErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		args       []string
		wantStderr string
	}{
		{"run takes no args", []string{"run", "x"}, "run"},
		{"enable without URL", []string{"enable"}, "URL"},
		{"enable repeated URL", []string{"enable", "URL=" + testURL, "URL=" + testURL}, "повтор"},
		{"enable unknown key", []string{"enable", "URL=" + testURL, "token=x"}, `"token"`},
		{"enable not KEY=VALUE", []string{"enable", testURL}, "KEY=VALUE"},
		{"enable lower-case url key", []string{"enable", "url=" + testURL}, `"url"`},
		{"disable takes no args", []string{"disable", "--now"}, "disable"},
		{"sync takes no args", []string{"sync", "now"}, "sync"},
		{"apply without space", []string{"apply", "env=prod"}, "space"},
		{"apply repeated env", []string{"apply", "env=prod", "env=dev", "space=sp-1"}, "повтор"},
		{"apply unknown key", []string{"apply", "env=prod", "space=sp-1", "cluster=x"}, `"cluster"`},
		{"apply space out of alphabet", []string{"apply", "env=prod", "space=SP_1"}, "space"},
		{"apply env empty", []string{"apply", "env=", "space=sp-1"}, "env"},
		{"apply env starts with dash", []string{"apply", "env=-prod", "space=sp-1"}, "env"},
		{"apply env too long", []string{"apply", "env=" + strings.Repeat("a", 64), "space=sp-1"}, "env"},
		{"status unknown flag", []string{"status", "--yaml"}, `"--yaml"`},
		{"status extra arg", []string{"status", "--json", "x"}, "status"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			code, stdout, stderr := e.run(testToken+"\n", tc.args...)
			if code != 2 {
				t.Errorf("exit code = %d, want 2 (stderr %q)", code, stderr)
			}
			if !strings.Contains(stderr, tc.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr, tc.wantStderr)
			}
			if exists(e.app.TokenPath) {
				t.Error("a usage error wrote the token file")
			}
			if len(e.sys.Calls()) != 0 || len(e.tp.SetCalls()) != 0 || e.portal.calls() != 0 {
				t.Errorf("a usage error touched the machine: systemctl %q, set-managed %v, portal %d",
					e.sys.Calls(), e.tp.SetCalls(), e.portal.calls())
			}
			e.assertNoToken(stdout, stderr)
		})
	}
}

// Everything except version, help and status needs root (spec §8).
func TestRootRequired(t *testing.T) {
	t.Parallel()
	tests := []struct {
		args     []string
		wantCode int
	}{
		{[]string{"run"}, 1},
		{[]string{"enable", "URL=" + testURL}, 1},
		{[]string{"disable"}, 1},
		{[]string{"sync"}, 1},
		{[]string{"apply", "env=prod", "space=sp-1"}, 1},
		{[]string{"status", "--json"}, 0},
		{[]string{"version", "--label"}, 0},
		{[]string{"version"}, 0},
		{[]string{"help"}, 0},
	}
	for _, tc := range tests {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			e.uid = 1000
			e.writeToken()
			code, _, stderr := e.run(testToken+"\n", tc.args...)
			if code != tc.wantCode {
				t.Errorf("exit code = %d, want %d (stderr %q)", code, tc.wantCode, stderr)
			}
			if tc.wantCode == 1 {
				if !strings.Contains(stderr, "root") {
					t.Errorf("stderr = %q, want the reason (root)", stderr)
				}
				if len(e.sys.Calls()) != 0 || len(e.tp.SetCalls()) != 0 || e.portal.calls() != 0 {
					t.Error("a non-root call touched the machine")
				}
			}
		})
	}
}
