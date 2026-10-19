package state

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamletenkov/stvor-agent/internal/atomicfile"
	"github.com/iamletenkov/stvor-agent/internal/portal"
)

const testURL = "https://stvor.example/robot-api/v1/labels"

// specExample is the state.json of spec §5.5, compacted.
const specExample = `{"schema":1,"url":"https://stvor.example/robot-api/v1/labels",` +
	`"expected":{"env":"prod","space":"sp-1","revision":"0123456789abcdef"},` +
	`"applied_revision":"0123456789abcdef",` +
	`"restart_attempts":{"count":0,"last":"2026-10-18T10:00:00Z"},` +
	`"last_poll":{"at":"2026-10-18T10:00:00Z","outcome":"not_modified","http_status":304},` +
	`"failures":0}` + "\n"

func full() State {
	at := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	return State{
		URL: testURL,
		Expected: &portal.Expected{
			Labels:   portal.Labels{Env: "prod", Space: "sp-1"},
			Revision: "0123456789abcdef",
		},
		AppliedRevision: "0123456789abcdef",
		RestartAttempts: Attempts{Count: 0, Last: at},
		LastPoll:        &Poll{At: at, Outcome: "not_modified", HTTPStatus: 304},
		Failures:        0,
	}
}

func path(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "stvor-agent", "state.json")
}

func TestSaveWritesSpecFormat(t *testing.T) {
	t.Parallel()
	p := path(t)
	if err := Save(atomicfile.OS{}, p, full()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != specExample {
		t.Errorf("state.json =\n%s\nwant\n%s", raw, specExample)
	}
	st, _ := os.Lstat(p)
	if st.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %o, want 600", st.Mode().Perm())
	}
	dst, _ := os.Lstat(filepath.Dir(p))
	if dst.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %o, want 700", dst.Mode().Perm())
	}
}

func TestNewSavesMinimal(t *testing.T) {
	t.Parallel()
	p := path(t)
	if err := Save(atomicfile.OS{}, p, New(testURL)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, _ := os.ReadFile(p)
	want := `{"schema":1,"url":"` + testURL + `","restart_attempts":{"count":0},"failures":0}` + "\n"
	if string(raw) != want {
		t.Errorf("state.json = %s, want %s", raw, want)
	}
	got, err := Load(atomicfile.OS{}, p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.URL != testURL || got.Expected != nil || got.LastPoll != nil || got.AppliedRevision != "" {
		t.Errorf("Load(New) = %+v", got)
	}
}

func TestRoundTrip(t *testing.T) {
	t.Parallel()
	p := path(t)
	want := full()
	want.RestartAttempts = Attempts{Count: 2, Last: time.Date(2026, 10, 18, 11, 30, 5, 0, time.UTC)}
	want.Failures = 4
	want.LastPoll = &Poll{At: time.Date(2026, 10, 18, 11, 31, 0, 0, time.UTC), Outcome: "network"}
	if err := Save(atomicfile.OS{}, p, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(atomicfile.OS{}, p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.URL != want.URL || *got.Expected != *want.Expected || got.AppliedRevision != want.AppliedRevision ||
		got.RestartAttempts.Count != 2 || !got.RestartAttempts.Last.Equal(want.RestartAttempts.Last) ||
		got.Failures != 4 || got.LastPoll.Outcome != "network" || got.LastPoll.HTTPStatus != 0 ||
		!got.LastPoll.At.Equal(want.LastPoll.At) {
		t.Errorf("round trip:\n got %+v\nwant %+v", got, want)
	}
}

func TestLoadAbsent(t *testing.T) {
	t.Parallel()
	got, err := Load(atomicfile.OS{}, path(t))
	if !errors.Is(err, ErrAbsent) {
		t.Fatalf("err = %v, want ErrAbsent", err)
	}
	if got.URL != "" || got.Expected != nil {
		t.Errorf("state = %+v, want zero", got)
	}
}

func TestLoadAcceptsSpecExampleWithSpaces(t *testing.T) {
	t.Parallel()
	p := path(t)
	pretty := `{"schema":1,
 "url":"https://stvor.example/robot-api/v1/labels",
 "expected":{"env":"prod","space":"sp-1","revision":"0123456789abcdef"},
 "applied_revision":"0123456789abcdef",
 "restart_attempts":{"count":0,"last":"2026-10-18T10:00:00Z"},
 "last_poll":{"at":"2026-10-18T10:00:00Z","outcome":"not_modified","http_status":304},
 "failures":0}`
	writeRaw(t, p, pretty)
	if _, err := Load(atomicfile.OS{}, p); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

func TestLoadRefusals(t *testing.T) {
	t.Parallel()
	ex := strings.TrimSuffix(specExample, "\n")
	repl := func(old, with string) string {
		if !strings.Contains(ex, old) {
			panic("test fixture: " + old)
		}
		return strings.Replace(ex, old, with, 1)
	}
	tests := []struct{ name, content string }{
		{"empty", ""},
		{"not json", "url=x"},
		{"array", "[]"},
		{"schema 2", repl(`"schema":1`, `"schema":2`)},
		{"no schema", repl(`"schema":1,`, ``)},
		{"schema string", repl(`"schema":1`, `"schema":"1"`)},
		{"extra top field", repl(`"failures":0`, `"failures":0,"etag":"x"`)},
		{"extra nested field", repl(`"revision":"0123456789abcdef"}`, `"revision":"0123456789abcdef","x":1}`)},
		{"upper-case key", repl(`"url":`, `"URL":`)},
		{"case-folded key", repl(`"url":`, `"Url":`)},
		{"repeated key", repl(`"failures":0`, `"failures":0,"failures":1`)},
		{"repeated nested key", repl(`"env":"prod"`, `"env":"prod","env":"dev"`)},
		{"trailing data", ex + `{}`},
		{"two objects", ex + "\n" + ex},
		{"http url", repl(`https://stvor`, `http://stvor`)},
		{"no url", repl(`"url":"https://stvor.example/robot-api/v1/labels",`, ``)},
		{"bad revision", repl(`"revision":"0123456789abcdef"`, `"revision":"0123456789ABCDEF"`)},
		{"short revision", repl(`"revision":"0123456789abcdef"`, `"revision":"0123"`)},
		{"bad applied revision", repl(`"applied_revision":"0123456789abcdef"`, `"applied_revision":"zz"`)},
		{"bad env", repl(`"env":"prod"`, `"env":"Prod"`)},
		{"empty space", repl(`"space":"sp-1"`, `"space":""`)},
		{"missing env", repl(`"env":"prod",`, ``)},
		{"negative failures", repl(`"failures":0`, `"failures":-1`)},
		{"fractional failures", repl(`"failures":0`, `"failures":1.5`)},
		{"negative count", repl(`"count":0`, `"count":-1`)},
		{"bad time", repl(`"last":"2026-10-18T10:00:00Z"`, `"last":"yesterday"`)},
		{"bad outcome", repl(`"outcome":"not_modified"`, `"outcome":"Not Modified"`)},
		{"empty outcome", repl(`"outcome":"not_modified"`, `"outcome":""`)},
		{"bad http status", repl(`"http_status":304`, `"http_status":42`)},
		{"NUL", repl(`"sp-1"`, "\"sp-1\x00\"")},
		{"too big", ex + strings.Repeat(" ", 70*1024)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := path(t)
			writeRaw(t, p, tc.content)
			got, err := Load(atomicfile.OS{}, p)
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
			if got.URL != "" || got.Expected != nil {
				t.Errorf("state = %+v, want zero", got)
			}
		})
	}
}

func TestLoadRefusesSymlink(t *testing.T) {
	t.Parallel()
	p := path(t)
	writeRaw(t, p+".real", specExample)
	if err := os.Symlink(p+".real", p); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(atomicfile.OS{}, p); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}

func TestSaveRefusesInvalid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		mut  func(*State)
	}{
		{"bad url", func(s *State) { s.URL = "http://x/robot-api/v1/labels" }},
		{"bad env", func(s *State) { s.Expected.Env = "a b" }},
		{"bad revision", func(s *State) { s.Expected.Revision = "x" }},
		{"bad applied", func(s *State) { s.AppliedRevision = "x" }},
		{"negative failures", func(s *State) { s.Failures = -1 }},
		{"bad outcome", func(s *State) { s.LastPoll.Outcome = "OK" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := path(t)
			if err := Save(atomicfile.OS{}, p, full()); err != nil {
				t.Fatal(err)
			}
			s := full()
			tc.mut(&s)
			if err := Save(atomicfile.OS{}, p, s); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
			if raw, _ := os.ReadFile(p); string(raw) != specExample {
				t.Errorf("previous state changed: %s", raw)
			}
		})
	}
}

func writeRaw(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
