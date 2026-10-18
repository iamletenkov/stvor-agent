package token

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/iamletenkov/stvor-agent/internal/atomicfile"
)

const testURL = "https://stvor.example.org/robot-api/v1/labels"

// testToken is built, not written out: a literal 43-char secret-looking string
// would trip the secret scanner.
func testToken() string { return strings.Repeat("Ab3_-", 8) + "xyz" }

// rootFS is the real file system with the owner of every file replaced: the
// tests run as an ordinary user, the agent as root.
type rootFS struct {
	atomicfile.OS
	fileUID, dirUID uint32
}

func (r rootFS) ReadNoFollow(path string, limit int64) ([]byte, atomicfile.Info, error) {
	data, info, err := r.OS.ReadNoFollow(path, limit)
	info.UID = r.fileUID
	return data, info, err
}

func (r rootFS) Lstat(path string) (atomicfile.Info, error) {
	info, err := r.OS.Lstat(path)
	info.UID = r.dirUID
	return info, err
}

// setup makes <tmp>/etc/stvor-agent (0700) and returns the token path in it.
func setup(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "stvor-agent")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "token")
}

func writeRaw(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func good() string { return "url=" + testURL + "\ntoken=" + testToken() + "\n" }

func TestReadGood(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		content string
		mode    os.FileMode
	}{
		{"as written", good(), 0o600},
		{"read-only file", good(), 0o400},
		{"keys swapped", "token=" + testToken() + "\nurl=" + testURL + "\n", 0o600},
		{"no final newline", "url=" + testURL + "\ntoken=" + testToken(), 0o600},
		{"port", "url=https://stvor.example.org:8443/robot-api/v1/labels\ntoken=" + testToken() + "\n", 0o600},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := setup(t)
			writeRaw(t, path, tc.content, tc.mode)
			got, err := Read(rootFS{}, path)
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			if got.Token != testToken() {
				t.Errorf("token mismatch")
			}
			if !strings.HasPrefix(got.URL, "https://stvor.example.org") {
				t.Errorf("url = %q", got.URL)
			}
		})
	}
}

func TestReadRefusals(t *testing.T) {
	t.Parallel()
	tok := testToken()
	tests := []struct {
		name    string
		content string // "" with absent=true: no file at all
		absent  bool
		mode    os.FileMode
		dirMode os.FileMode
		fileUID uint32
		dirUID  uint32
		want    error
	}{
		{name: "absent", absent: true, want: ErrAbsent},
		{name: "file 0644", content: good(), mode: 0o644, want: ErrBadMode},
		{name: "file 0640", content: good(), mode: 0o640, want: ErrBadMode},
		{name: "file 0604", content: good(), mode: 0o604, want: ErrBadMode},
		{name: "dir 0755", content: good(), mode: 0o600, dirMode: 0o755, want: ErrBadMode},
		{name: "dir 0750", content: good(), mode: 0o600, dirMode: 0o750, want: ErrBadMode},
		{name: "file owner not root", content: good(), mode: 0o600, fileUID: 1000, want: ErrBadOwner},
		{name: "dir owner not root", content: good(), mode: 0o600, dirUID: 1000, want: ErrBadOwner},
		{name: "empty", content: "", mode: 0o600, want: ErrCorrupt},
		{name: "no url", content: "token=" + tok + "\n", mode: 0o600, want: ErrCorrupt},
		{name: "no token", content: "url=" + testURL + "\n", mode: 0o600, want: ErrCorrupt},
		{name: "extra line", content: good() + "x=y\n", mode: 0o600, want: ErrCorrupt},
		{name: "empty line", content: good() + "\n", mode: 0o600, want: ErrCorrupt},
		{name: "repeated url", content: "url=" + testURL + "\nurl=" + testURL + "\ntoken=" + tok + "\n", mode: 0o600, want: ErrCorrupt},
		{name: "repeated token", content: good() + "token=" + tok + "\n", mode: 0o600, want: ErrCorrupt},
		{name: "http url", content: "url=http://stvor.example.org/robot-api/v1/labels\ntoken=" + tok + "\n", mode: 0o600, want: ErrCorrupt},
		{name: "other path", content: "url=https://stvor.example.org/api\ntoken=" + tok + "\n", mode: 0o600, want: ErrCorrupt},
		{name: "upper-case host", content: "url=https://Stvor.example.org/robot-api/v1/labels\ntoken=" + tok + "\n", mode: 0o600, want: ErrCorrupt},
		{name: "url trailing newline in value", content: "url=" + testURL + "\r\ntoken=" + tok + "\n", mode: 0o600, want: ErrCorrupt},
		{name: "token 42", content: "url=" + testURL + "\ntoken=" + tok[:42] + "\n", mode: 0o600, want: ErrCorrupt},
		{name: "token 44", content: "url=" + testURL + "\ntoken=" + tok + "a\n", mode: 0o600, want: ErrCorrupt},
		{name: "token bad char", content: "url=" + testURL + "\ntoken=" + tok[:42] + "+\n", mode: 0o600, want: ErrCorrupt},
		{name: "token with spaces", content: "url=" + testURL + "\ntoken= " + tok + "\n", mode: 0o600, want: ErrCorrupt},
		{name: "NUL", content: "url=" + testURL + "\ntoken=" + tok + "\x00\n", mode: 0o600, want: ErrCorrupt},
		{name: "too big", content: good() + strings.Repeat("#", 5000), mode: 0o600, want: ErrCorrupt},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := setup(t)
			if tc.dirMode != 0 {
				if err := os.Chmod(filepath.Dir(path), tc.dirMode); err != nil {
					t.Fatal(err)
				}
			}
			if !tc.absent {
				writeRaw(t, path, tc.content, tc.mode)
			}
			_, err := Read(rootFS{fileUID: tc.fileUID, dirUID: tc.dirUID}, path)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			assertNoSecret(t, err)
			if Kind(err) == "" {
				t.Errorf("Kind(%v) is empty", err)
			}
		})
	}
}

func TestReadSymlink(t *testing.T) {
	t.Parallel()
	path := setup(t)
	target := filepath.Join(filepath.Dir(path), "real")
	writeRaw(t, target, good(), 0o600)
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	_, err := Read(rootFS{}, path)
	if !errors.Is(err, ErrBadMode) {
		t.Errorf("symlink: err = %v, want ErrBadMode", err)
	}
	assertNoSecret(t, err)
}

// deniedFS answers like the real file system to a user who is not root: the
// directory 0700 of root is closed, open and lstat give EACCES.
type deniedFS struct {
	rootFS
	lstatOnly bool
}

func (d deniedFS) ReadNoFollow(path string, limit int64) ([]byte, atomicfile.Info, error) {
	if d.lstatOnly {
		return d.rootFS.ReadNoFollow(path, limit)
	}
	return nil, atomicfile.Info{}, &fs.PathError{Op: "open", Path: path, Err: syscall.EACCES}
}

func (d deniedFS) Lstat(path string) (atomicfile.Info, error) {
	return atomicfile.Info{}, &fs.PathError{Op: "lstat", Path: path, Err: syscall.EACCES}
}

// No access (not root) is its own class, not a broken file.
func TestReadNoAccess(t *testing.T) {
	t.Parallel()
	for _, lstatOnly := range []bool{false, true} {
		path := setup(t)
		writeRaw(t, path, good(), 0o600)
		_, err := Read(deniedFS{lstatOnly: lstatOnly}, path)
		if !errors.Is(err, ErrNoAccess) {
			t.Fatalf("lstatOnly=%v: err = %v, want ErrNoAccess", lstatOnly, err)
		}
		if got := Kind(err); got != "no_access" {
			t.Errorf("lstatOnly=%v: Kind = %q, want no_access", lstatOnly, got)
		}
		assertNoSecret(t, err)
	}
}

func TestKind(t *testing.T) {
	t.Parallel()
	tests := []struct {
		err  error
		want string
	}{
		{nil, ""},
		{fmt.Errorf("x: %w", ErrAbsent), "absent"},
		{fmt.Errorf("x: %w", ErrBadMode), "bad_mode"},
		{fmt.Errorf("x: %w", ErrBadOwner), "bad_owner"},
		{fmt.Errorf("x: %w", ErrCorrupt), "corrupt"},
		{fmt.Errorf("x: %w", ErrNoAccess), "no_access"},
		{errors.New("other"), "corrupt"},
	}
	for _, tc := range tests {
		if got := Kind(tc.err); got != tc.want {
			t.Errorf("Kind(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

func TestWriteAndRead(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "stvor-agent")
	path := filepath.Join(dir, "token")
	want := Token{URL: testURL, Token: testToken()}
	if err := Write(rootFS{}, path, want); err != nil {
		t.Fatalf("Write: %v", err)
	}
	st, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %o, want 600", st.Mode().Perm())
	}
	dst, _ := os.Lstat(dir)
	if dst.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %o, want 700", dst.Mode().Perm())
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != good() {
		t.Errorf("file = %q, want the 3.x format url=…\\ntoken=…\\n", raw)
	}
	got, err := Read(rootFS{}, path)
	if err != nil || got != want {
		t.Fatalf("Read after Write: %v (equal %v)", err, got == want)
	}
}

// enable is the cure for a wrong directory mode: Write narrows it to 0700.
func TestWriteFixesDirMode(t *testing.T) {
	t.Parallel()
	path := setup(t)
	if err := os.Chmod(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Write(rootFS{}, path, Token{URL: testURL, Token: testToken()}); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Lstat(filepath.Dir(path)); st.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %o, want 700", st.Mode().Perm())
	}
}

func TestWriteRefusesBadInput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		tok  Token
	}{
		{"http url", Token{URL: "http://stvor.example.org/robot-api/v1/labels", Token: testToken()}},
		{"url with newline", Token{URL: testURL + "\ntoken=x", Token: testToken()}},
		{"short token", Token{URL: testURL, Token: testToken()[:42]}},
		{"token with newline", Token{URL: testURL, Token: testToken()[:42] + "\n"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := setup(t)
			writeRaw(t, path, good(), 0o600)
			err := Write(rootFS{}, path, tc.tok)
			if !errors.Is(err, ErrCorrupt) {
				t.Fatalf("err = %v, want ErrCorrupt", err)
			}
			assertNoSecret(t, err)
			if raw, _ := os.ReadFile(path); string(raw) != good() {
				t.Errorf("previous file changed")
			}
		})
	}
}

func TestExists(t *testing.T) {
	t.Parallel()
	path := setup(t)
	if Exists(rootFS{}, path) {
		t.Error("Exists before write")
	}
	// Exists does not read: even a file it cannot open counts.
	writeRaw(t, path, "garbage", 0o000)
	if !Exists(rootFS{}, path) {
		t.Error("Exists after write")
	}
}

// The token never leaks through formatting of the struct.
func TestTokenFormatting(t *testing.T) {
	t.Parallel()
	tok := Token{URL: testURL, Token: testToken()}
	for _, s := range []string{fmt.Sprint(tok), fmt.Sprintf("%v", tok), fmt.Sprintf("%+v", tok), fmt.Sprintf("%#v", tok), fmt.Sprintf("%s", tok)} { //nolint:gocritic // each verb is the point
		if strings.Contains(s, testToken()) {
			t.Errorf("formatted token leaks: %s", s)
		}
		if !strings.Contains(s, testURL) {
			t.Errorf("formatted token lost the url: %s", s)
		}
	}
	if v := tok.LogValue().String(); strings.Contains(v, testToken()) {
		t.Errorf("LogValue leaks: %s", v)
	}
}

func TestValidURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		url  string
		want bool
	}{
		{testURL, true},
		{"https://10.0.0.1:443/robot-api/v1/labels", true},
		{"https://a/robot-api/v1/labels", true},
		{"https://a:123456/robot-api/v1/labels", false},
		{"https://a/robot-api/v1/labels/", false},
		{"https://a_b/robot-api/v1/labels", false},
		{"https://user@a/robot-api/v1/labels", false},
		{"", false},
	}
	for _, tc := range tests {
		if got := ValidURL(tc.url); got != tc.want {
			t.Errorf("ValidURL(%q) = %v, want %v", tc.url, got, tc.want)
		}
	}
}

func assertNoSecret(t *testing.T, err error) {
	t.Helper()
	if err != nil && strings.Contains(err.Error(), testToken()[:20]) {
		t.Errorf("error carries the token: %v", err)
	}
}
