// Package token is the agent's secret: /etc/stvor-agent/token with the portal
// address and the robot token (spec §5.6, §6). Format — the one of 3.x
// labels-token: "url=<address>\ntoken=<token>\n"; file 0600, owner root,
// directory 0700.
//
// The token goes only to the address from the same file, never to argv, the
// environment of children, the journal or state.json. No error of this package
// carries the content of the file.
package token

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/iamletenkov/stvor-agent/internal/atomicfile"
)

// DefaultPath is where the agent keeps the token on a machine.
const DefaultPath = "/etc/stvor-agent/token"

// maxSize bounds the read: a well-formed file is under 200 bytes.
const maxSize = 4096

// Error classes of Read. Match with errors.Is; Kind gives the short name for
// the journal and `status`.
var (
	ErrAbsent   = errors.New("файла токена нет")
	ErrBadMode  = errors.New("права файла токена шире допустимого")
	ErrBadOwner = errors.New("владелец файла токена не root")
	ErrCorrupt  = errors.New("файл токена испорчен")
)

var (
	reURL   = regexp.MustCompile(`^https://[a-z0-9.-]+(:[0-9]{1,5})?/robot-api/v1/labels$`)
	reToken = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
)

// FS is what the package needs from the file system; atomicfile.OS is the
// real one, tests substitute the owner.
type FS interface {
	ReadNoFollow(path string, limit int64) ([]byte, atomicfile.Info, error)
	Lstat(path string) (atomicfile.Info, error)
	EnsureDir(dir string, mode fs.FileMode, fixMode bool) error
	WriteAtomic(path string, data []byte, mode fs.FileMode) error
}

// Token is the content of the token file.
type Token struct {
	URL   string
	Token string
}

// String hides the token: a Token printed by mistake shows only the address.
func (t Token) String() string { return "url=" + t.URL + " token=<скрыт>" }

// GoString hides the token for %#v as well.
func (t Token) GoString() string {
	return "token.Token{URL:" + fmt.Sprintf("%q", t.URL) + ", Token:<скрыт>}"
}

// LogValue hides the token in slog.
func (t Token) LogValue() slog.Value { return slog.StringValue(t.String()) }

// Format hides the token for every fmt verb, %+v included.
func (t Token) Format(f fmt.State, verb rune) {
	if verb == 'v' && f.Flag('#') {
		_, _ = f.Write([]byte(t.GoString()))
		return
	}
	_, _ = f.Write([]byte(t.String()))
}

// ValidURL reports whether u is a portal labels address the token may be sent
// to: https, a lower-case host, an optional port, the fixed path.
func ValidURL(u string) bool { return reURL.MatchString(u) }

// ValidToken reports whether s has the form of a robot token: 43 characters
// [A-Za-z0-9_-].
func ValidToken(s string) bool { return reToken.MatchString(s) }

// Kind is the class of a Read error: absent, bad_mode, bad_owner or corrupt;
// "" for nil. An error of no known class counts as corrupt.
func Kind(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrAbsent):
		return "absent"
	case errors.Is(err, ErrBadMode):
		return "bad_mode"
	case errors.Is(err, ErrBadOwner):
		return "bad_owner"
	default:
		return "corrupt"
	}
}

// Read loads and checks the token file. The file is opened without following
// symlinks, and its mode and owner come from fstat of the same descriptor.
// Every error wraps one of ErrAbsent, ErrBadMode, ErrBadOwner, ErrCorrupt.
func Read(fsys FS, path string) (Token, error) {
	data, info, err := fsys.ReadNoFollow(path, maxSize)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return Token{}, fmt.Errorf("%s: %w", path, ErrAbsent)
	case errors.Is(err, atomicfile.ErrNotRegular):
		return Token{}, fmt.Errorf("%s: не обычный файл: %w", path, ErrBadMode)
	case errors.Is(err, atomicfile.ErrTooBig):
		return Token{}, fmt.Errorf("%s: больше %d байт: %w", path, maxSize, ErrCorrupt)
	case err != nil:
		return Token{}, fmt.Errorf("%s: не прочитан: %w: %w", path, err, ErrCorrupt)
	}
	if err := checkDir(fsys, filepath.Dir(path)); err != nil {
		return Token{}, err
	}
	if perm := info.Mode.Perm(); perm&0o077 != 0 {
		return Token{}, fmt.Errorf("%s: права %04o, нужно 0600: %w", path, perm, ErrBadMode)
	}
	if info.UID != 0 {
		return Token{}, fmt.Errorf("%s: владелец uid %d, нужен root: %w", path, info.UID, ErrBadOwner)
	}
	t, reason := parse(data)
	if reason != "" {
		return Token{}, fmt.Errorf("%s: %s: %w", path, reason, ErrCorrupt)
	}
	return t, nil
}

func checkDir(fsys FS, dir string) error {
	info, err := fsys.Lstat(dir)
	if err != nil {
		return fmt.Errorf("%s: каталог не прочитан: %w", dir, ErrBadMode)
	}
	if !info.Mode.IsDir() {
		return fmt.Errorf("%s: не каталог: %w", dir, ErrBadMode)
	}
	if perm := info.Mode.Perm(); perm&0o077 != 0 {
		return fmt.Errorf("%s: права каталога %04o, нужно 0700: %w", dir, perm, ErrBadMode)
	}
	if info.UID != 0 {
		return fmt.Errorf("%s: владелец каталога uid %d, нужен root: %w", dir, info.UID, ErrBadOwner)
	}
	return nil
}

// parse returns the token or the reason of a refusal. The reason never quotes
// the file: a wrong line may be the token itself.
func parse(data []byte) (Token, string) {
	if bytes.IndexByte(data, 0) >= 0 {
		return Token{}, "NUL в файле"
	}
	s := strings.TrimSuffix(string(data), "\n")
	if s == "" {
		return Token{}, "файл пуст"
	}
	var t Token
	var haveURL, haveToken bool
	for i, line := range strings.Split(s, "\n") {
		key, value, _ := strings.Cut(line, "=")
		switch key {
		case "url":
			if haveURL {
				return Token{}, "повтор ключа url"
			}
			haveURL, t.URL = true, value
		case "token":
			if haveToken {
				return Token{}, "повтор ключа token"
			}
			haveToken, t.Token = true, value
		default:
			return Token{}, fmt.Sprintf("лишняя строка %d", i+1)
		}
	}
	switch {
	case !haveURL:
		return Token{}, "нет строки url="
	case !haveToken:
		return Token{}, "нет строки token="
	case !ValidURL(t.URL):
		return Token{}, "url не по форме https://<хост>[:порт]/robot-api/v1/labels"
	case !ValidToken(t.Token):
		return Token{}, "token не по форме (43 знака [A-Za-z0-9_-])"
	}
	return t, ""
}

// Write checks t and replaces the token file atomically: the directory is
// created or narrowed to 0700, the file appears with 0600 at once. A refused
// t wraps ErrCorrupt and leaves the previous file as it was.
func Write(fsys FS, path string, t Token) error {
	if !ValidURL(t.URL) {
		return fmt.Errorf("url не по форме https://<хост>[:порт]/robot-api/v1/labels: %w", ErrCorrupt)
	}
	if !ValidToken(t.Token) {
		return fmt.Errorf("token не по форме (43 знака [A-Za-z0-9_-]): %w", ErrCorrupt)
	}
	dir := filepath.Dir(path)
	if err := fsys.EnsureDir(dir, 0o700, true); err != nil {
		return fmt.Errorf("каталог токена %s: %w", dir, err)
	}
	data := []byte("url=" + t.URL + "\ntoken=" + t.Token + "\n")
	if err := fsys.WriteAtomic(path, data, 0o600); err != nil {
		return fmt.Errorf("файл токена: %w", err)
	}
	return nil
}

// Exists reports whether the token file is there (the agent is enabled). It
// only lstat-s the path and never opens the file: `version --label` runs it
// for every Teleport heartbeat.
func Exists(fsys FS, path string) bool {
	_, err := fsys.Lstat(path)
	return err == nil
}
