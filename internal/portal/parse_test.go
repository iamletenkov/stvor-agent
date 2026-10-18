package portal_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/iamletenkov/stvor-agent/internal/portal"
)

const (
	revA = "0123456789abcdef"
	good = `{"labels":{"env":"prod","space":"sp-1a2b3c4d"},"revision":"0123456789abcdef"}`
)

func TestParseBodyAccepts(t *testing.T) {
	t.Parallel()
	want := portal.Expected{Labels: portal.Labels{Env: "prod", Space: "sp-1a2b3c4d"}, Revision: revA}
	tests := []struct {
		name string
		body string
	}{
		{"компактный", good},
		{"пробелы между токенами", "{\n  \"labels\": {\"env\": \"prod\", \"space\": \"sp-1a2b3c4d\"},\n  \"revision\": \"0123456789abcdef\"\n}\n"},
		{"табы и CR", "\t{\r\n\"labels\"\t:\t{\"env\":\"prod\",\"space\":\"sp-1a2b3c4d\"},\"revision\":\"0123456789abcdef\"}\r\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := portal.ParseBody([]byte(tc.body))
			if err != nil {
				t.Fatalf("ParseBody: %v", err)
			}
			if got != want {
				t.Fatalf("ParseBody = %+v, want %+v", got, want)
			}
		})
	}
}

func TestParseBodyBoundaryValues(t *testing.T) {
	t.Parallel()
	max63 := "a" + strings.Repeat("-", 62)
	tests := []struct {
		name, env, space string
		ok               bool
	}{
		{"одна цифра", "0", "9", true},
		{"63 знака", max63, max63, true},
		{"64 знака", max63 + "a", "x", false},
		{"начало с дефиса", "-prod", "x", false},
		{"заглавные", "Prod", "x", false},
		{"подчёркивание", "prod", "sp_x", false},
		{"пустое", "", "x", false},
		{"пробел", "pr od", "x", false},
		{"кириллица", "прод", "x", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body := `{"labels":{"env":"` + tc.env + `","space":"` + tc.space + `"},"revision":"` + revA + `"}`
			_, err := portal.ParseBody([]byte(body))
			if tc.ok && err != nil {
				t.Fatalf("ParseBody: %v", err)
			}
			if !tc.ok && !errors.Is(err, portal.ErrBadBody) {
				t.Fatalf("ParseBody err = %v, want ErrBadBody", err)
			}
		})
	}
}

func TestParseBodyRejects(t *testing.T) {
	t.Parallel()
	lab := `"labels":{"env":"prod","space":"sp-x"}`
	rev := `"revision":"` + revA + `"`
	tests := []struct {
		name string
		body string
	}{
		{"пустое тело", ""},
		{"только пробелы", " \n "},
		{"не JSON", "not json"},
		{"null", "null"},
		{"массив", `[` + good + `]`},
		{"строка", `"x"`},
		{"лишний ключ в labels", `{"labels":{"env":"prod","space":"sp-x","app":"y"},` + rev + `}`},
		{"лишний ключ верхнего уровня", `{` + lab + `,` + rev + `,"x":"y"}`},
		{"лишний ключ в начале", `{"x":"y",` + lab + `,` + rev + `}`},
		{"повтор space", `{"labels":{"env":"prod","space":"sp-x","space":"sp-y"},` + rev + `}`},
		{"повтор env", `{"labels":{"env":"prod","env":"dev","space":"sp-x"},` + rev + `}`},
		{"повтор revision", `{` + lab + `,` + rev + `,` + rev + `}`},
		{"повтор labels", `{` + lab + `,` + lab + `,` + rev + `}`},
		{"иной порядок в labels", `{"labels":{"space":"sp-x","env":"prod"},` + rev + `}`},
		{"иной порядок верхнего уровня", `{` + rev + `,` + lab + `}`},
		{"ключ ENV", `{"labels":{"ENV":"prod","space":"sp-x"},` + rev + `}`},
		{"ключ Labels", `{"Labels":{"env":"prod","space":"sp-x"},` + rev + `}`},
		{"ключ через escape", `{"labels":{"\u0065nv":"prod","space":"sp-x"},` + rev + `}`},
		{"значение через escape", `{"labels":{"env":"pr\u006fd","space":"sp-x"},` + rev + `}`},
		{"нет space", `{"labels":{"env":"prod"},` + rev + `}`},
		{"нет revision", `{` + lab + `}`},
		{"нет labels", `{` + rev + `}`},
		{"пустой объект", `{}`},
		{"вложенный лишний объект", `{"labels":{"env":{"x":"prod"},"space":"sp-x"},` + rev + `}`},
		{"labels — строка", `{"labels":"env=prod",` + rev + `}`},
		{"значение — число", `{"labels":{"env":1,"space":"sp-x"},` + rev + `}`},
		{"значение — null", `{"labels":{"env":null,"space":"sp-x"},` + rev + `}`},
		{"revision — число", `{` + lab + `,"revision":123}`},
		{"хвост — второй объект", good + good},
		{"хвост — мусор", good + "x"},
		{"хвост — запятая", good + ","},
		{"незакрытый объект", strings.TrimSuffix(good, "}")},
		{"NUL в значении", `{"labels":{"env":"prod` + "\x00" + `","space":"sp-x"},` + rev + `}`},
		{"NUL в хвосте", good + "\x00"},
		{"NUL между токенами", "\x00" + good},
		{"NUL через escape", `{"labels":{"env":"prod\u0000","space":"sp-x"},` + rev + `}`},
		{"не UTF-8", `{"labels":{"env":"prod","space":"sp-x"},` + rev + `}` + "\xff"},
		{"не UTF-8 в значении", `{"labels":{"env":"pr` + "\xc3\x28" + `d","space":"sp-x"},` + rev + `}`},
		{"BOM", "\xef\xbb\xbf" + good},
		{"revision не hex", `{` + lab + `,"revision":"xyz"}`},
		{"revision 15 знаков", `{` + lab + `,"revision":"0123456789abcde"}`},
		{"revision 17 знаков", `{` + lab + `,"revision":"0123456789abcdef0"}`},
		{"revision заглавные", `{` + lab + `,"revision":"0123456789ABCDEF"}`},
		{"длиннее 4096", good + strings.Repeat(" ", 4097-len(good))},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := portal.ParseBody([]byte(tc.body))
			if !errors.Is(err, portal.ErrBadBody) {
				t.Fatalf("ParseBody = %+v, %v; want ErrBadBody", got, err)
			}
			if got != (portal.Expected{}) {
				t.Fatalf("ParseBody on error returned %+v, want zero", got)
			}
		})
	}
}

// The body of the answer must never reach the journal: the error text names
// the reason only, not the values the portal sent.
func TestParseBodyErrorHasNoBody(t *testing.T) {
	t.Parallel()
	secret := "leakmarker"
	bodies := []string{
		`{"labels":{"env":"` + secret + `_","space":"sp-x"},"revision":"` + revA + `"}`,
		`{"labels":{"` + secret + `":"prod","space":"sp-x"},"revision":"` + revA + `"}`,
		secret,
		good + secret,
	}
	for _, b := range bodies {
		_, err := portal.ParseBody([]byte(b))
		if err == nil {
			t.Fatalf("ParseBody(%q) = nil error", b)
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error %q contains body content", err)
		}
	}
}

func TestParseBodyExactly4096(t *testing.T) {
	t.Parallel()
	body := good + strings.Repeat(" ", 4096-len(good))
	if _, err := portal.ParseBody([]byte(body)); err != nil {
		t.Fatalf("ParseBody(4096 bytes): %v", err)
	}
}
