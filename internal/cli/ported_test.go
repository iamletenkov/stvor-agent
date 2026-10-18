package cli

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Checks carried over from tests/test-labels.sh of 3.x that the other tests of
// this package did not cover yet (docs/test-map.md).

// test-labels.sh labels-enable: refused input («URL не по шаблону (http)»,
// «чужой путь», «кавычка», «без URL», «токен не по шаблону», «пустой stdin») —
// no token file, no state.json, no systemctl, no request to the portal.
func TestEnableRefusalsTouchNothing(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		stdin string
		args  []string
	}{
		{"http URL", testToken + "\n", []string{"enable", "URL=http://stvor.example/robot-api/v1/labels"}},
		{"other path", testToken + "\n", []string{"enable", "URL=https://stvor.example/api/v1/labels"}},
		{"quote in URL", testToken + "\n", []string{"enable", `URL=https://stvor".example/robot-api/v1/labels`}},
		{"no URL", testToken + "\n", []string{"enable"}},
		{"short token", "short", []string{"enable", "URL=" + testURL}},
		{"empty stdin", "", []string{"enable", "URL=" + testURL}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			code, stdout, stderr := e.run(tc.stdin, tc.args...)
			if code == 0 {
				t.Fatalf("exit code = 0, want a refusal (stdout %q)", stdout)
			}
			if exists(e.app.TokenPath) || exists(e.app.StatePath) {
				t.Error("a refused enable left the token file or state.json")
			}
			if len(e.sys.Calls()) != 0 {
				t.Errorf("a refused enable called systemctl: %q", e.sys.Calls())
			}
			if e.portal.calls() != 0 {
				t.Errorf("a refused enable polled the portal %d times", e.portal.calls())
			}
			if e.tp.StatusCalls() != 0 || len(e.tp.SetCalls()) != 0 {
				t.Error("a refused enable called teleport")
			}
			e.assertNoToken(stdout, stderr)
		})
	}
}

// test-labels.sh labels-enable: «токена нет в config», «status: временных
// файлов запроса не осталось». After enable and its first sync the token is
// only in the token file, and no temporary file is left anywhere.
func TestEnableTokenOnlyInTokenFile(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	if code, _, stderr := e.run(testToken+"\n", "enable", "URL="+testURL); code != 0 {
		t.Fatalf("exit code = %d (stderr %q)", code, stderr)
	}
	if !exists(e.app.StatePath) {
		t.Fatal("state.json not written by the first sync")
	}
	err := filepath.WalkDir(e.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(d.Name(), ".") {
			t.Errorf("temporary file left behind: %s", path)
		}
		if d.IsDir() || path == e.app.TokenPath {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), testToken) {
			t.Errorf("token found in %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// test-labels.sh set-managed: the refused input of the manual path («чужой
// ключ app», «чужой ключ вместе с допустимым», «ключ ручки», «без аргументов»,
// «space=A"b», «пустой space», «перевод строки», «заглавные», «запятая», «ключ
// дважды»; «после отказов config не тронут», «systemctl не вызывался») on
// `stvor-agent apply`: exit 2, set-managed not called, state.json unchanged.
func TestApplyRefusesBadInput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args []string
	}{
		{"foreign key app", []string{"app=x"}},
		{"foreign key with valid ones", []string{"env=prod", "space=sp-b", "site=x"}},
		{"knob key", []string{"TELEPORT_ENV=prod", "space=sp-b"}},
		{"no args", nil},
		{"quote in space", []string{"env=prod", `space=A"b`}},
		{"empty space", []string{"env=prod", "space="}},
		{"newline in value", []string{"env=prod", "space=sp-a\nenv=x"}},
		{"upper case", []string{"env=Prod", "space=sp-b"}},
		{"comma", []string{"env=prod", "space=a,b=c"}},
		{"key twice", []string{"env=prod", "space=a", "space=b"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			e.writeToken()
			e.saveState(stateInEffect())
			before, err := os.ReadFile(e.app.StatePath)
			if err != nil {
				t.Fatal(err)
			}
			code, stdout, stderr := e.run("", append([]string{"apply"}, tc.args...)...)
			if code != 2 {
				t.Errorf("exit code = %d, want 2 (stderr %q)", code, stderr)
			}
			if len(e.tp.SetCalls()) != 0 || e.tp.StatusCalls() != 0 {
				t.Errorf("refused apply called teleport: set-managed %v", e.tp.SetCalls())
			}
			if len(e.sys.Calls()) != 0 || e.portal.calls() != 0 {
				t.Error("refused apply called systemctl or the portal")
			}
			after, err := os.ReadFile(e.app.StatePath)
			if err != nil || string(after) != string(before) {
				t.Errorf("refused apply changed state.json (%v)", err)
			}
			e.assertNoToken(stdout, stderr)
		})
	}
}
