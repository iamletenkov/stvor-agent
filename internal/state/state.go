// Package state is /var/lib/stvor-agent/state.json: what the agent remembers
// between ticks (spec §5.5). The token is never stored here.
//
// Loading is strict: an unknown schema, an unknown, repeated or case-folded
// key, trailing data or a value off its pattern mean "no state" — a file
// edited by hand is not state. Writing is atomic (atomicfile).
package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"regexp"
	"time"

	"github.com/iamletenkov/stvor-agent/internal/atomicfile"
	"github.com/iamletenkov/stvor-agent/internal/portal"
	"github.com/iamletenkov/stvor-agent/internal/token"
)

// DefaultPath is where the agent keeps its state on a machine.
const DefaultPath = "/var/lib/stvor-agent/state.json"

// Schema is the version of the file this package reads and writes.
const Schema = 1

// maxSize bounds the read: a well-formed file is under 1 KiB.
const maxSize = 64 << 10

// Errors of Load and Save. Match with errors.Is.
var (
	ErrAbsent  = errors.New("state.json нет")
	ErrInvalid = errors.New("state.json не по схеме 1")
)

var (
	reValue    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`) // env, space (spec §5.4)
	reRevision = regexp.MustCompile(`^[0-9a-f]{16}$`)
	// Outcome strings are owned by reconcile (which imports this package);
	// here only their shape is checked.
	reOutcome = regexp.MustCompile(`^[a-z][a-z_]{0,31}$`)
	reKey     = regexp.MustCompile(`^[a-z_]{1,32}$`)
)

// FS is what the package needs from the file system; atomicfile.OS is the
// real one.
type FS interface {
	ReadNoFollow(path string, limit int64) ([]byte, atomicfile.Info, error)
	EnsureDir(dir string, mode fs.FileMode, fixMode bool) error
	WriteAtomic(path string, data []byte, mode fs.FileMode) error
}

// State is the content of state.json.
type State struct {
	// URL is the portal address the state belongs to; a token for another
	// address makes the whole state meaningless (spec §5.3).
	URL string
	// Expected is the last 200 answer of the portal; nil — none yet. The
	// ETag is `"<Expected.Revision>"`.
	Expected *portal.Expected
	// AppliedRevision is the revision whose labels were seen in effect; ""
	// — none. Reported to the portal with X-Applied-Revision.
	AppliedRevision string
	// RestartAttempts counts unconfirmed set-managed restarts in a row.
	RestartAttempts Attempts
	// LastPoll is the result of the last portal poll; nil — none yet.
	// Written by the tick.
	LastPoll *Poll
	// Failures is the number of failed ticks in a row. Written by the tick.
	Failures int
}

// Attempts are unconfirmed restarts in a row and the time of the last one.
type Attempts struct {
	Count int
	Last  time.Time // zero when Count is 0 and nothing was ever tried
}

// Poll is the last portal poll.
type Poll struct {
	At         time.Time
	Outcome    string // a reconcile.Outcome
	HTTPStatus int    // 0 when no response was received
}

// New is the empty state for a portal address.
func New(url string) State { return State{URL: url} }

// wire is the JSON form; field names and order are the contract of spec §5.5.
type wire struct {
	Schema          *int          `json:"schema"`
	URL             string        `json:"url"`
	Expected        *wireExpected `json:"expected,omitempty"`
	AppliedRevision string        `json:"applied_revision,omitempty"`
	RestartAttempts wireAttempts  `json:"restart_attempts"`
	LastPoll        *wirePoll     `json:"last_poll,omitempty"`
	Failures        int           `json:"failures"`
}

type wireExpected struct {
	Env      string `json:"env"`
	Space    string `json:"space"`
	Revision string `json:"revision"`
}

type wireAttempts struct {
	Count int       `json:"count"`
	Last  time.Time `json:"last,omitzero"`
}

type wirePoll struct {
	At         time.Time `json:"at"`
	Outcome    string    `json:"outcome"`
	HTTPStatus int       `json:"http_status"`
}

// Load reads state.json. On any error the returned State is zero, which is
// "no state": ErrAbsent — there is no file (nothing to log); ErrInvalid — the
// file is there but refused, the error tells why (one journal line). The
// error never quotes the file.
func Load(fsys FS, path string) (State, error) {
	data, _, err := fsys.ReadNoFollow(path, maxSize)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return State{}, fmt.Errorf("%s: %w", path, ErrAbsent)
	case err != nil:
		return State{}, fmt.Errorf("%s: %w: %w", path, err, ErrInvalid)
	}
	st, reason := decode(data)
	if reason != "" {
		return State{}, fmt.Errorf("%s: %s: %w", path, reason, ErrInvalid)
	}
	return st, nil
}

// Save validates st and replaces state.json atomically with mode 0600. The
// directory is created 0700 when absent (systemd StateDirectory= normally
// makes it). A refused st wraps ErrInvalid and leaves the file as it was.
func Save(fsys FS, path string, st State) error {
	if reason := validate(st); reason != "" {
		return fmt.Errorf("%s: %s: %w", path, reason, ErrInvalid)
	}
	schema := Schema
	w := wire{
		Schema:          &schema,
		URL:             st.URL,
		AppliedRevision: st.AppliedRevision,
		RestartAttempts: wireAttempts{Count: st.RestartAttempts.Count, Last: st.RestartAttempts.Last.UTC()},
		Failures:        st.Failures,
	}
	if e := st.Expected; e != nil {
		w.Expected = &wireExpected{Env: e.Env, Space: e.Space, Revision: e.Revision}
	}
	if p := st.LastPoll; p != nil {
		w.LastPoll = &wirePoll{At: p.At.UTC(), Outcome: p.Outcome, HTTPStatus: p.HTTPStatus}
	}
	data, err := json.Marshal(w)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	if err := fsys.EnsureDir(dir, 0o700, false); err != nil {
		return fmt.Errorf("каталог состояния %s: %w", dir, err)
	}
	return fsys.WriteAtomic(path, data, 0o600)
}

func decode(data []byte) (State, string) {
	if reason := checkKeys(data); reason != "" {
		return State{}, reason
	}
	// Keys are now exact and unique, so struct decoding cannot be fooled by
	// case folding or "last one wins".
	var w wire
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&w); err != nil {
		return State{}, "разбор: " + err.Error()
	}
	if w.Schema == nil || *w.Schema != Schema {
		return State{}, "схема не 1"
	}
	st := State{
		URL:             w.URL,
		AppliedRevision: w.AppliedRevision,
		RestartAttempts: Attempts{Count: w.RestartAttempts.Count, Last: w.RestartAttempts.Last},
		Failures:        w.Failures,
	}
	if e := w.Expected; e != nil {
		st.Expected = &portal.Expected{Labels: portal.Labels{Env: e.Env, Space: e.Space}, Revision: e.Revision}
	}
	if p := w.LastPoll; p != nil {
		st.LastPoll = &Poll{At: p.At, Outcome: p.Outcome, HTTPStatus: p.HTTPStatus}
	}
	if reason := validate(st); reason != "" {
		return State{}, reason
	}
	return st, ""
}

func validate(st State) string {
	switch {
	case !token.ValidURL(st.URL):
		return "url не по форме"
	case st.Expected != nil && !reValue.MatchString(st.Expected.Env):
		return "expected.env не по форме"
	case st.Expected != nil && !reValue.MatchString(st.Expected.Space):
		return "expected.space не по форме"
	case st.Expected != nil && !reRevision.MatchString(st.Expected.Revision):
		return "expected.revision не по форме"
	case st.AppliedRevision != "" && !reRevision.MatchString(st.AppliedRevision):
		return "applied_revision не по форме"
	case st.RestartAttempts.Count < 0:
		return "restart_attempts.count < 0"
	case st.Failures < 0:
		return "failures < 0"
	case st.LastPoll != nil && !reOutcome.MatchString(st.LastPoll.Outcome):
		return "last_poll.outcome не по форме"
	case st.LastPoll != nil && st.LastPoll.HTTPStatus != 0 &&
		(st.LastPoll.HTTPStatus < 100 || st.LastPoll.HTTPStatus > 599):
		return "last_poll.http_status вне 100–599"
	}
	return ""
}

// checkKeys walks the JSON tokens: one top-level object, every key lower-case
// [a-z_] and unique within its object, nothing after the object.
func checkKeys(data []byte) string {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return "не JSON"
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return "не объект"
	}
	if reason := walkObject(dec); reason != "" {
		return reason
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return "данные после объекта"
	}
	return ""
}

// walkObject consumes an object whose '{' is already read.
func walkObject(dec *json.Decoder) string {
	seen := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return "не JSON"
		}
		key, ok := tok.(string)
		if !ok || !reKey.MatchString(key) {
			return "ключ не по форме"
		}
		if seen[key] {
			return "повтор ключа " + key
		}
		seen[key] = true
		if reason := walkValue(dec); reason != "" {
			return reason
		}
	}
	if _, err := dec.Token(); err != nil { // '}'
		return "не JSON"
	}
	return ""
}

func walkValue(dec *json.Decoder) string {
	tok, err := dec.Token()
	if err != nil {
		return "не JSON"
	}
	d, ok := tok.(json.Delim)
	if !ok {
		return ""
	}
	switch d {
	case '{':
		return walkObject(dec)
	case '[':
		for dec.More() {
			if reason := walkValue(dec); reason != "" {
				return reason
			}
		}
		if _, err := dec.Token(); err != nil { // ']'
			return "не JSON"
		}
		return ""
	}
	return "не JSON"
}
