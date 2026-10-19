// Package packaging holds the files shipped in the .deb; its tests check them
// against the spec without systemd (systemd-analyze verify runs in Task 20).
package packaging

import (
	"bufio"
	"os"
	"strings"
	"testing"
)

const unitFile = "stvor-agent.service"

// unit is section -> key -> values in file order.
type unit map[string]map[string][]string

func parseUnit(t *testing.T, path string) unit {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("открыть юнит: %v", err)
	}
	defer func() { _ = f.Close() }()

	u := unit{}
	section := ""
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";"):
			continue
		case strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]"):
			section = line[1 : len(line)-1]
			if u[section] == nil {
				u[section] = map[string][]string{}
			}
		default:
			if strings.HasSuffix(line, "\\") {
				t.Fatalf("строка %d: перенос строк не поддерживается тестом", n)
			}
			k, v, ok := strings.Cut(line, "=")
			if !ok || section == "" {
				t.Fatalf("строка %d: не «ключ=значение» в секции: %q", n, line)
			}
			k = strings.TrimSpace(k)
			u[section][k] = append(u[section][k], strings.TrimSpace(v))
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("чтение юнита: %v", err)
	}
	return u
}

func TestUnitRequiredKeys(t *testing.T) {
	t.Parallel()
	u := parseUnit(t, unitFile)

	tests := []struct {
		section, key, want string
	}{
		// spec §9.1
		{"Unit", "ConditionPathExists", "/etc/stvor-agent/token"},
		{"Unit", "After", "network-online.target"},
		{"Unit", "Wants", "network-online.target"},
		{"Service", "Type", "exec"},
		{"Service", "ExecStart", "/usr/bin/stvor-agent run"},
		{"Service", "Restart", "always"},
		{"Service", "RestartSec", "10"},
		{"Service", "KillMode", "mixed"},
		{"Service", "TimeoutStopSec", "120"},
		{"Service", "EnvironmentFile", "-/etc/cream/stvor-agent/config"},
		{"Service", "StateDirectory", "stvor-agent"},
		{"Service", "StateDirectoryMode", "0700"},
		{"Service", "SyslogIdentifier", "stvor-agent"},
		{"Install", "WantedBy", "multi-user.target"},
		// spec §6 hardening
		{"Service", "NoNewPrivileges", "yes"},
		{"Service", "PrivateTmp", "yes"},
		{"Service", "ProtectHome", "yes"},
		{"Service", "ProtectKernelTunables", "yes"},
		{"Service", "ProtectKernelModules", "yes"},
		{"Service", "ProtectKernelLogs", "yes"},
		{"Service", "ProtectControlGroups", "yes"},
		{"Service", "ProtectClock", "yes"},
		{"Service", "ProtectHostname", "yes"},
		{"Service", "RestrictAddressFamilies", "AF_UNIX AF_INET AF_INET6"},
		{"Service", "RestrictNamespaces", "yes"},
		{"Service", "RestrictRealtime", "yes"},
		{"Service", "RestrictSUIDSGID", "yes"},
		{"Service", "LockPersonality", "yes"},
		{"Service", "MemoryDenyWriteExecute", "yes"},
		{"Service", "SystemCallArchitectures", "native"},
		{"Service", "UMask", "0077"},
	}
	for _, tc := range tests {
		t.Run(tc.section+"/"+tc.key, func(t *testing.T) {
			t.Parallel()
			got := u[tc.section][tc.key]
			if len(got) != 1 || got[0] != tc.want {
				t.Errorf("[%s] %s = %q, want ровно одно %q", tc.section, tc.key, got, tc.want)
			}
		})
	}
}

// Children (cream, the teleport extension) inherit the sandbox and write
// /etc/cream/teleport, /etc/teleport.yaml, /var/lib/cream/ext/teleport; the
// service runs as root (spec §6).
func TestUnitForbiddenKeys(t *testing.T) {
	t.Parallel()
	u := parseUnit(t, unitFile)

	forbidden := []string{
		"ProtectSystem", "ReadWritePaths", "ReadOnlyPaths", "InaccessiblePaths",
		"User", "Group", "DynamicUser", "PrivateDevices", "PrivateUsers", "PrivateNetwork",
		"CapabilityBoundingSet", "SystemCallFilter", "NotifyAccess", "WatchdogSec",
	}
	for _, key := range forbidden {
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			for section, keys := range u {
				if v, ok := keys[key]; ok {
					t.Errorf("[%s] %s=%q запрещён спекой §6/§9.1", section, key, v)
				}
			}
		})
	}
}

func TestUnitSectionsOnly(t *testing.T) {
	t.Parallel()
	u := parseUnit(t, unitFile)
	for section := range u {
		switch section {
		case "Unit", "Service", "Install":
		default:
			t.Errorf("лишняя секция [%s]", section)
		}
	}
	for _, s := range []string{"Unit", "Service", "Install"} {
		if u[s] == nil {
			t.Errorf("нет секции [%s]", s)
		}
	}
}
