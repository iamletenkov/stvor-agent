package portal

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"unicode/utf8"
)

// MaxBody is the largest 200 body the agent accepts (spec §5.4).
const MaxBody = 4096

// ErrBadBody means the 200 body is not by the contract. Its text names the reason
// only, never the content of the body (spec §7: no bodies in the journal).
var ErrBadBody = errors.New("ответ портала не по контракту /robot-api/v1/labels")

var (
	reValue    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	reRevision = regexp.MustCompile(`^[0-9a-f]{16}$`)
)

// ParseBody strictly parses the 200 body of GET /robot-api/v1/labels:
//
//	{"labels":{"env":"<v>","space":"<v>"},"revision":"<16 hex>"}
//
// Keys exactly in this order, each once, no extra keys, nothing after the
// object; whitespace between tokens is allowed. The body must be valid UTF-8
// with no NUL and no escapes (a legitimate body has none: keys are fixed and
// values come from the DNS label alphabet). encoding/json.Unmarshal is not
// used: it takes the last of repeated keys and matches keys case-insensitively.
func ParseBody(body []byte) (Expected, error) {
	switch {
	case len(body) > MaxBody:
		return Expected{}, bad("тело длиннее %d байт", MaxBody)
	case bytes.IndexByte(body, 0) >= 0:
		// A NUL once glued two halves into a valid value.
		return Expected{}, bad("NUL в теле")
	case !utf8.Valid(body):
		return Expected{}, bad("тело не UTF-8")
	case bytes.IndexByte(body, '\\') >= 0:
		return Expected{}, bad("escape-последовательность в теле")
	}
	p := tokens{dec: json.NewDecoder(bytes.NewReader(body))}
	p.delim('{')
	p.key("labels")
	p.delim('{')
	p.key("env")
	env := p.str("env")
	p.key("space")
	space := p.str("space")
	p.delim('}')
	p.key("revision")
	rev := p.str("revision")
	p.delim('}')
	p.end()
	if p.err != nil {
		return Expected{}, p.err
	}
	switch {
	case !reValue.MatchString(env):
		return Expected{}, bad("env вне алфавита")
	case !reValue.MatchString(space):
		return Expected{}, bad("space вне алфавита")
	case !reRevision.MatchString(rev):
		return Expected{}, bad("revision не 16 hex")
	}
	return Expected{Labels: Labels{Env: env, Space: space}, Revision: rev}, nil
}

func bad(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrBadBody, fmt.Sprintf(format, args...))
}

// tokens walks the expected token sequence; the first mismatch sticks in err
// and turns the rest into no-ops. Decoder errors are not wrapped: their text
// may quote bytes of the body.
type tokens struct {
	dec *json.Decoder
	err error
}

func (p *tokens) next(what string) (json.Token, bool) {
	if p.err != nil {
		return nil, false
	}
	tok, err := p.dec.Token()
	if err != nil {
		p.err = bad("ожидался %s: синтаксис JSON", what)
		return nil, false
	}
	return tok, true
}

func (p *tokens) delim(d json.Delim) {
	tok, ok := p.next(string(d))
	if ok && tok != d {
		p.err = bad("ожидался %s", string(d))
	}
}

func (p *tokens) key(name string) {
	tok, ok := p.next("ключ " + name)
	if !ok {
		return
	}
	if s, isStr := tok.(string); !isStr || s != name {
		p.err = bad("ожидался ключ %s", name)
	}
}

func (p *tokens) str(name string) string {
	tok, ok := p.next("значение " + name)
	if !ok {
		return ""
	}
	s, isStr := tok.(string)
	if !isStr {
		p.err = bad("значение %s не строка", name)
		return ""
	}
	return s
}

// end requires nothing but whitespace after the object.
func (p *tokens) end() {
	if p.err != nil {
		return
	}
	if _, err := p.dec.Token(); !errors.Is(err, io.EOF) {
		p.err = bad("данные после объекта")
	}
}
