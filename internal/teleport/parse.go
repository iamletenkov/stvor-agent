package teleport

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"unicode/utf8"
)

// MaxOutput is the largest stdout the agent reads from cream (spec §5.7).
const MaxOutput = 64 << 10

// maxManaged is the limit of TELEPORT_MANAGED_LABELS in T (spec §10.3).
const maxManaged = 16

// ErrBadOutput means stdout of the action is not the JSON of schema 1. Its text
// names the reason only, never the bytes of the output.
var ErrBadOutput = errors.New("teleport: вывод действия не по контракту схемы 1")

// Key and value alphabets of TELEPORT_MANAGED_LABELS in T (spec §10.3).
var (
	reKey   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
	reValue = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)
)

// parseOutput strictly parses the object of managed-status (withResult=false)
// or set-managed (withResult=true), schema 1 (spec §5.7):
//
//	{"schema":1,"managed":{"env":"<v>","space":"<v>"},"pending":B,"joined":B,"active":B[,"result":"<r>"]}
//
// Keys in any order, each exactly once, no others; nothing after the object.
// managed holds only env and space (the agent's keys, spec §6), each optional
// (an empty managed is a node nobody has labelled yet). The output is UTF-8
// with no NUL and no escapes: keys are fixed and values come from the T
// alphabet. encoding/json.Unmarshal is not used: it takes the last of repeated
// keys and matches keys case-insensitively.
func parseOutput(data []byte, withResult bool) (SetResult, error) {
	switch {
	case bytes.IndexByte(data, 0) >= 0:
		return SetResult{}, bad("NUL в выводе")
	case !utf8.Valid(data):
		return SetResult{}, bad("вывод не UTF-8")
	case bytes.IndexByte(data, '\\') >= 0:
		return SetResult{}, bad("escape-последовательность в выводе")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	p := tokens{dec: dec}
	var r SetResult
	seen := map[string]bool{}
	p.delim('{')
	for p.more() {
		k := p.str("ключ")
		if p.err != nil {
			break
		}
		if seen[k] {
			p.fail("повтор ключа %s", k)
			break
		}
		seen[k] = true
		switch k {
		case "schema":
			tok, ok := p.next("schema")
			if n, isNum := tok.(json.Number); ok && (!isNum || n.String() != "1") {
				p.fail("schema не 1")
			}
		case "managed":
			r.Managed = p.managed()
		case "pending":
			r.Pending = p.boolean(k)
		case "joined":
			r.Joined = p.boolean(k)
		case "active":
			r.Active = p.boolean(k)
		case "result":
			if !withResult {
				p.fail("ключ result в ответе managed-status")
				break
			}
			r.Result = p.str("result")
			switch r.Result {
			case ResultApplied, ResultUnchanged, ResultDeferred, ResultUnconfirmed:
			default:
				p.fail("неизвестный result")
			}
		default:
			p.fail("посторонний ключ")
		}
	}
	p.delim('}')
	p.end()
	if p.err != nil {
		return SetResult{}, p.err
	}
	required := []string{"schema", "managed", "pending", "joined", "active"}
	if withResult {
		required = append(required, "result")
	}
	for _, k := range required {
		if !seen[k] {
			return SetResult{}, bad("нет ключа %s", k)
		}
	}
	return r, nil
}

func bad(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrBadOutput, fmt.Sprintf(format, args...))
}

// tokens walks the token stream; the first mismatch sticks in err and turns
// the rest into no-ops. Decoder errors are not wrapped: their text may quote
// bytes of the output.
type tokens struct {
	dec *json.Decoder
	err error
}

func (p *tokens) fail(format string, args ...any) {
	if p.err == nil {
		p.err = bad(format, args...)
	}
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

func (p *tokens) more() bool { return p.err == nil && p.dec.More() }

func (p *tokens) delim(d json.Delim) {
	tok, ok := p.next(string(d))
	if ok && tok != d {
		p.fail("ожидался %s", string(d))
	}
}

func (p *tokens) str(what string) string {
	tok, ok := p.next(what)
	if !ok {
		return ""
	}
	s, isStr := tok.(string)
	if !isStr {
		p.fail("%s не строка", what)
	}
	return s
}

func (p *tokens) boolean(what string) bool {
	tok, ok := p.next(what)
	if !ok {
		return false
	}
	b, isBool := tok.(bool)
	if !isBool {
		p.fail("%s не true/false", what)
	}
	return b
}

// managed parses the managed object: T keys and values, each key once, at most
// maxManaged, and only env and space among them.
func (p *tokens) managed() Labels {
	var l Labels
	seen := map[string]bool{}
	p.delim('{')
	for p.more() {
		k := p.str("ключ managed")
		v := p.str("значение managed")
		switch {
		case p.err != nil:
		case !reKey.MatchString(k):
			p.fail("ключ managed вне алфавита")
		case seen[k]:
			p.fail("повтор ключа managed")
		case len(seen) >= maxManaged:
			p.fail("в managed больше %d меток", maxManaged)
		case !reValue.MatchString(v):
			p.fail("значение managed вне алфавита")
		}
		seen[k] = true
		switch k {
		case "env":
			l.Env = v
		case "space":
			l.Space = v
		default:
			// The agent is the only manager and sets exactly env and space; any
			// other managed key is a state it cannot express, so it is unknown.
			p.fail("посторонняя управляемая метка")
		}
	}
	p.delim('}')
	return l
}

// end requires nothing but whitespace after the object.
func (p *tokens) end() {
	if p.err != nil {
		return
	}
	if _, err := p.dec.Token(); !errors.Is(err, io.EOF) {
		p.fail("данные после объекта")
	}
}

// jsonObject reports whether data is exactly one JSON object (exit code 2 of
// an action promises one; its content is not part of the contract).
func jsonObject(data []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(data))
	var v map[string]json.RawMessage
	if err := dec.Decode(&v); err != nil || v == nil {
		return false
	}
	_, err := dec.Token()
	return errors.Is(err, io.EOF)
}

// parseActions reads the names of actions from `cream actions <ext> --json`.
// The envelope is cream §11.4; the form of result is fixed by the golden files
// of cream T15 (expected form — testdata/cream/actions-teleport.json:
// result.actions is a list of objects with "name"). Until then the reader is
// tolerant: unknown fields anywhere are ignored, and result.actions may also be
// an object keyed by name, or result itself the list. Any doubt is an error —
// CheckActions turns it into "unknown", never into "missing".
func parseActions(data []byte) (map[string]bool, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	var env struct {
		OK     *bool           `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	if err := dec.Decode(&env); err != nil {
		return nil, errors.New("вывод cream actions не JSON")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("данные после конверта cream actions")
	}
	if env.OK == nil || !*env.OK {
		return nil, errors.New("конверт cream actions без ok=true")
	}
	if len(env.Result) == 0 || string(env.Result) == "null" {
		return nil, errors.New("в конверте cream actions нет result")
	}
	list := env.Result
	var obj struct {
		Actions json.RawMessage `json:"actions"`
	}
	if json.Unmarshal(env.Result, &obj) == nil {
		if len(obj.Actions) == 0 {
			return nil, errors.New("в result cream actions нет actions")
		}
		list = obj.Actions
	}
	names := map[string]bool{}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(list, &items); err == nil {
		for _, it := range items {
			var name string
			if err := json.Unmarshal(it["name"], &name); err != nil || name == "" {
				return nil, errors.New("действие без имени в cream actions")
			}
			names[name] = true
		}
		return names, nil
	}
	var byName map[string]json.RawMessage
	if err := json.Unmarshal(list, &byName); err == nil && byName != nil {
		for name := range byName {
			names[name] = true
		}
		return names, nil
	}
	return nil, errors.New("неизвестная форма result cream actions")
}
