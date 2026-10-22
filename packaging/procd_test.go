package packaging

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The procd init scripts of OpenWrt (spec OpenWrt §4.3, R8): the counterparts
// of the two systemd units, shipped in stvor-agent_<v>_linux_<arch>.tar.gz.
// procd has no sandbox but no_new_privs; what the units say with
// ConditionPathExists=, User=, Restart=, TimeoutStopSec= the scripts say
// with their own lines, pinned here. Their behaviour under rc.common runs in
// make e2e-openwrt.
const (
	procdAgent     = "openwrt/stvor-agent"
	procdTelemetry = "openwrt/stvor-agent-telemetry"
)

func readScript(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("открыть %s: %v", path, err)
	}
	return string(data)
}

// codeLines are the lines of the script without comments and indentation.
func codeLines(script string) []string {
	var out []string
	for line := range strings.SplitSeq(script, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}

func hasLine(lines []string, want string) bool {
	for _, l := range lines {
		if l == want {
			return true
		}
	}
	return false
}

func TestProcdScriptsRequiredLines(t *testing.T) {
	t.Parallel()
	common := []string{
		"USE_PROCD=1",
		"START=99",
		"STOP=01",
		// Restart=always, RestartSec=10: no give-up threshold.
		"procd_set_param respawn 3600 10 0",
		// NoNewPrivileges=yes of both units.
		"procd_set_param no_new_privs 1",
		// The journal of OpenWrt is logd (logread): stdout and stderr go there.
		"procd_set_param stdout 1",
		"procd_set_param stderr 1",
	}
	tests := []struct {
		path string
		want []string
	}{
		{procdAgent, append([]string{
			// ConditionPathExists=/etc/stvor-agent/token
			`TOKEN=/etc/stvor-agent/token`,
			`[ -f "$TOKEN" ] || return 0`,
			`procd_set_param command "$PROG" run`,
			// TimeoutStopSec=120: the tick may wait for set-managed.
			"procd_set_param term_timeout 120",
			"CONFIG=/etc/cream/stvor-agent-openwrt/config",
		}, common...)},
		{procdTelemetry, append([]string{
			`TOKEN=/etc/stvor-agent-telemetry/token`,
			`[ -f "$TOKEN" ] || return 0`,
			`procd_set_param command "$PROG" telemetry`,
			// User=, Group= of the telemetry unit: never root.
			`procd_set_param user "$TELEMETRY_USER"`,
			`procd_set_param group "$TELEMETRY_USER"`,
			"TELEMETRY_USER=stvor-telemetry",
			"procd_set_param term_timeout 30",
			"CONFIG=/etc/cream/stvor-agent-openwrt/config",
		}, common...)},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()
			script := readScript(t, tc.path)
			if first, _, _ := strings.Cut(script, "\n"); first != "#!/bin/sh /etc/rc.common" {
				t.Errorf("первая строка %q, want #!/bin/sh /etc/rc.common", first)
			}
			lines := codeLines(script)
			for _, w := range tc.want {
				if !hasLine(lines, w) {
					t.Errorf("нет строки %q", w)
				}
			}
			if !hasLine(lines, "PROG=/usr/bin/stvor-agent") {
				t.Error("нет PROG=/usr/bin/stvor-agent")
			}
		})
	}
}

// TestProcdScriptsForbidden: the agent stays root (cream and teleport-openwrt
// write /etc and restart /etc/init.d/teleport); the settings file is never
// sourced (a shell of root over a file cream writes) — only two keys with a
// plain value are taken from it; nothing runs through eval.
func TestProcdScriptsForbidden(t *testing.T) {
	t.Parallel()
	sourced := regexp.MustCompile(`^(\.|source)\s+"?\$CONFIG`)
	for _, path := range []string{procdAgent, procdTelemetry} {
		lines := codeLines(readScript(t, path))
		for _, l := range lines {
			if sourced.MatchString(l) {
				t.Errorf("%s: файл настроек исполняется: %q", path, l)
			}
			if strings.HasPrefix(l, "eval ") {
				t.Errorf("%s: eval: %q", path, l)
			}
		}
		if path == procdAgent {
			for _, l := range lines {
				if strings.HasPrefix(l, "procd_set_param user") || strings.HasPrefix(l, "procd_set_param group") {
					t.Errorf("%s: агент — root, а не %q", path, l)
				}
			}
		}
	}
}

// TestProcdScriptsConfigKeys: only STVOR_AGENT_INTERVAL and
// STVOR_AGENT_LOG_LEVEL with a bare value reach the environment of the
// service (EnvironmentFile= of the units reads the same domain on Debian).
func TestProcdScriptsConfigKeys(t *testing.T) {
	t.Parallel()
	const filter = `sed -n -E 's/^(STVOR_AGENT_(INTERVAL|LOG_LEVEL))=([A-Za-z0-9_.:-]+)$/\1=\3/p' "$CONFIG"`
	for _, path := range []string{procdAgent, procdTelemetry} {
		if !hasLine(codeLines(readScript(t, path)), filter) {
			t.Errorf("%s: нет фильтра настроек %q", path, filter)
		}
	}
}
