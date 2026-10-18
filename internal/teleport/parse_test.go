package teleport

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseStatusFixtures(t *testing.T) {
	t.Parallel()
	want := map[string]Status{
		"managed-status-joined.json": {
			Managed: Labels{Env: "prod", Space: "sp-1"}, Joined: true, Active: true,
		},
		"managed-status-pending.json": {
			Managed: Labels{Env: "prod", Space: "sp-1"}, Pending: true, Joined: true, Active: true,
		},
		"managed-status-not-joined.json": {},
		"managed-status-inactive.json": {
			Managed: Labels{Env: "prod", Space: "sp-1"}, Joined: true,
		},
	}
	for name, w := range want {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			data, err := os.ReadFile(filepath.Join("testdata", name))
			if err != nil {
				t.Fatal(err)
			}
			got, err := parseOutput(data, false)
			if err != nil {
				t.Fatalf("parseOutput: %v", err)
			}
			if got.Status != w || got.Result != "" {
				t.Errorf("got %+v, want %+v", got, w)
			}
		})
	}
}

func TestParseSetFixtures(t *testing.T) {
	t.Parallel()
	want := map[string]SetResult{
		"set-managed-applied.json": {Status: Status{
			Managed: Labels{Env: "prod", Space: "sp-1"}, Joined: true, Active: true,
		}, Result: ResultApplied},
		"set-managed-unchanged.json": {Status: Status{
			Managed: Labels{Env: "prod", Space: "sp-1"}, Joined: true, Active: true,
		}, Result: ResultUnchanged},
		"set-managed-deferred.json": {Status: Status{
			Managed: Labels{Env: "prod", Space: "sp-1"},
		}, Result: ResultDeferred},
		"set-managed-unconfirmed.json": {Status: Status{
			Managed: Labels{Env: "prod", Space: "sp-1"}, Pending: true, Joined: true,
		}, Result: ResultUnconfirmed},
	}
	for name, w := range want {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			data, err := os.ReadFile(filepath.Join("testdata", name))
			if err != nil {
				t.Fatal(err)
			}
			got, err := parseOutput(data, true)
			if err != nil {
				t.Fatalf("parseOutput: %v", err)
			}
			if got != w {
				t.Errorf("got %+v, want %+v", got, w)
			}
		})
	}
}

// TestFixturesAreTheContract: every testdata/*.json is covered by the two
// tables above, so a new or renamed contract file cannot slip in untested.
func TestFixturesAreTheContract(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob(filepath.Join("testdata", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 8 {
		t.Fatalf("testdata/*.json: %d файлов, ожидалось 8: %v", len(files), files)
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		withResult := strings.HasPrefix(filepath.Base(f), "set-managed-")
		if _, err := parseOutput(data, withResult); err != nil {
			t.Errorf("%s: %v", f, err)
		}
		if !strings.HasSuffix(string(data), "}\n") || strings.Count(string(data), "\n") != 1 {
			t.Errorf("%s: ожидалась одна строка с переводом строки в конце", f)
		}
	}
}

func TestParseOutputRejects(t *testing.T) {
	t.Parallel()
	const base = `"managed":{"env":"prod","space":"sp-1"},"pending":false,"joined":true,"active":true`
	tests := []struct {
		name       string
		in         string
		withResult bool
	}{
		{"empty", ``, false},
		{"not json", `managed: yes`, false},
		{"array", `[1]`, false},
		{"schema 2", `{"schema":2,` + base + `}`, false},
		{"schema string", `{"schema":"1",` + base + `}`, false},
		{"schema 1.0", `{"schema":1.0,` + base + `}`, false},
		{"no schema", `{` + base + `}`, false},
		{"no active", `{"schema":1,"managed":{},"pending":false,"joined":true}`, false},
		{"extra key", `{"schema":1,` + base + `,"extra":1}`, false},
		{"result in status", `{"schema":1,` + base + `,"result":"applied"}`, false},
		{"no result in set", `{"schema":1,` + base + `}`, true},
		{"bad result", `{"schema":1,` + base + `,"result":"done"}`, true},
		{"result not string", `{"schema":1,` + base + `,"result":1}`, true},
		{"dup key", `{"schema":1,"schema":1,` + base + `}`, false},
		{"dup managed key", `{"schema":1,"managed":{"env":"a","env":"b"},"pending":false,"joined":true,"active":true}`, false},
		{"case key", `{"Schema":1,` + base + `}`, false},
		{"bool as string", `{"schema":1,"managed":{},"pending":"false","joined":true,"active":true}`, false},
		{"managed not object", `{"schema":1,"managed":[],"pending":false,"joined":true,"active":true}`, false},
		{"managed value number", `{"schema":1,"managed":{"env":1},"pending":false,"joined":true,"active":true}`, false},
		{"managed value upper", `{"schema":1,"managed":{"env":"Prod"},"pending":false,"joined":true,"active":true}`, false},
		{"managed value empty", `{"schema":1,"managed":{"env":""},"pending":false,"joined":true,"active":true}`, false},
		{"managed foreign key", `{"schema":1,"managed":{"env":"a","team":"x"},"pending":false,"joined":true,"active":true}`, false},
		{"managed bad key", `{"schema":1,"managed":{"Env":"a"},"pending":false,"joined":true,"active":true}`, false},
		{"two objects", `{"schema":1,` + base + `}{"schema":1,` + base + `}`, false},
		{"trailing garbage", `{"schema":1,` + base + `} x`, false},
		{"escape", "{\"schema\":1,\"managed\":{\"env\":\"pr\\u006fd\"},\"pending\":false,\"joined\":true,\"active\":true}", false},
		{"nul", "{\"schema\":1,\x00" + base + `}`, false},
		{"bad utf8", "{\"schema\":1,\xff" + base + `}`, false},
		{"null managed", `{"schema":1,"managed":null,"pending":false,"joined":true,"active":true}`, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := parseOutput([]byte(tc.in), tc.withResult)
			if !errors.Is(err, ErrBadOutput) {
				t.Fatalf("err = %v, want ErrBadOutput", err)
			}
		})
	}
}

func TestParseOutputAcceptsAnyKeyOrderAndWhitespace(t *testing.T) {
	t.Parallel()
	in := " {\n \"active\": false, \"result\": \"deferred\", \"joined\": false,\n" +
		" \"managed\": { \"space\": \"sp-1\", \"env\": \"dev.a_b\" }, \"pending\": false, \"schema\": 1 }\n"
	got, err := parseOutput([]byte(in), true)
	if err != nil {
		t.Fatal(err)
	}
	want := SetResult{Status: Status{Managed: Labels{Env: "dev.a_b", Space: "sp-1"}}, Result: ResultDeferred}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestParseActions(t *testing.T) {
	t.Parallel()
	read := func(name string) string {
		data, err := os.ReadFile(filepath.Join("testdata", "cream", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	tests := []struct {
		name string
		in   string
		want []string
		bad  bool
	}{
		{name: "sample 4.x", in: read("actions-teleport.json"), want: []string{"join", "managed-status", "set-managed"}},
		{name: "sample 3.x", in: read("actions-teleport-3x.json"), want: []string{"join", "status"}},
		{name: "actions as map", in: `{"ok":true,"result":{"actions":{"managed-status":{},"set-managed":{"x":1}}}}`,
			want: []string{"managed-status", "set-managed"}},
		{name: "result as array", in: `{"ok":true,"result":[{"name":"set-managed"},{"name":"managed-status","new":[1]}]}`,
			want: []string{"managed-status", "set-managed"}},
		{name: "extra fields", in: `{"cream":"1.2.0","command":"actions","ok":true,"future":{"a":1},` +
			`"result":{"extension":"teleport","more":true,"actions":[{"name":"managed-status","extra":null}]},"warnings":[{"code":"x"}],"error":null}`,
			want: []string{"managed-status"}},
		{name: "empty list", in: `{"ok":true,"result":{"actions":[]}}`, want: []string{}},
		{name: "not json", in: `teleport: join, status`, bad: true},
		{name: "ok false", in: `{"ok":false,"result":null,"error":{"code":"not_found.extension"}}`, bad: true},
		{name: "no ok", in: `{"result":{"actions":[]}}`, bad: true},
		{name: "no result", in: `{"ok":true}`, bad: true},
		{name: "unknown result form", in: `{"ok":true,"result":{"list":"x"}}`, bad: true},
		{name: "name not string", in: `{"ok":true,"result":{"actions":[{"name":1}]}}`, bad: true},
		{name: "item without name", in: `{"ok":true,"result":{"actions":[{"summary":"x"}]}}`, bad: true},
		{name: "two objects", in: `{"ok":true,"result":[]}{"ok":true,"result":[]}`, bad: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseActions([]byte(tc.in))
			if tc.bad {
				if err == nil {
					t.Fatalf("ожидалась ошибка, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for _, n := range tc.want {
				if !got[n] {
					t.Errorf("нет %q в %v", n, got)
				}
			}
		})
	}
}
