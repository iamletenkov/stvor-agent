// Package packaging holds the files shipped in the .deb; its tests check them
// against the spec without systemd (systemd-analyze verify runs in Task 20).
//
// The telemetry unit's budget is a controller ruling: MemoryMax=64M and
// CPUQuota=10% are hard caps with GOMEMLIMIT=40MiB, ProtectHome=read-only; the
// spec budget (< 30 MB RSS, < 1 % CPU) is verified by measurement (Stvor plan Task 21).
package packaging

import (
	"bufio"
	"os"
	"slices"
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

const telemetryUnitFile = "stvor-agent-telemetry.service"

// The telemetry unit (Stvor plan Task 18, ruling R4): unprivileged, no
// capabilities, read-only system, budget limits, systemd 237 compatible.
func TestTelemetryUnitRequiredKeys(t *testing.T) {
	t.Parallel()
	u := parseUnit(t, telemetryUnitFile)
	tests := []struct {
		section, key, want string
	}{
		{"Unit", "ConditionPathExists", "/etc/stvor-agent-telemetry/token"},
		{"Unit", "After", "network-online.target"},
		{"Unit", "Wants", "network-online.target"},
		{"Service", "Type", "simple"}, // exec needs systemd 240
		{"Service", "ExecStart", "/usr/bin/stvor-agent telemetry"},
		{"Service", "Restart", "always"},
		{"Service", "User", "stvor-telemetry"},
		{"Service", "Group", "stvor-telemetry"},
		{"Service", "EnvironmentFile", "-/etc/cream/stvor-agent/config"},
		{"Service", "StateDirectory", "stvor-agent-telemetry"},
		{"Service", "StateDirectoryMode", "0700"},
		{"Service", "RuntimeDirectory", "stvor-agent-telemetry"},
		{"Service", "CapabilityBoundingSet", ""},
		{"Service", "AmbientCapabilities", ""},
		{"Service", "NoNewPrivileges", "yes"},
		{"Service", "ProtectSystem", "strict"},
		{"Service", "ReadWritePaths", "-/var/lib/stvor-agent-telemetry"},
		{"Service", "ReadOnlyPaths", "-/etc/stvor-agent-telemetry"},
		{"Service", "ProtectHome", "read-only"},
		{"Service", "PrivateTmp", "yes"},
		{"Service", "PrivateDevices", "yes"},
		{"Service", "ProtectKernelTunables", "yes"},
		{"Service", "ProtectKernelModules", "yes"},
		{"Service", "ProtectControlGroups", "yes"},
		{"Service", "RestrictAddressFamilies", "AF_UNIX AF_INET AF_INET6 AF_NETLINK"},
		{"Service", "RestrictNamespaces", "yes"},
		{"Service", "MemoryDenyWriteExecute", "yes"},
		{"Service", "SystemCallArchitectures", "native"},
		{"Install", "WantedBy", "multi-user.target"},
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

// Controller ruling on the budget (Stvor plan Task 18 review): MemoryMax=64M
// and CPUQuota=10% are hard caps, GOMEMLIMIT=40MiB makes the Go runtime collect
// well before the cap; the spec budget (< 30 MB RSS, < 1 % CPU) is a measured
// target (Stvor plan Task 21), not a unit setting. Exactly these values, nothing else.
func TestTelemetryUnitBudgetCaps(t *testing.T) {
	t.Parallel()
	u := parseUnit(t, telemetryUnitFile)
	for key, want := range map[string][]string{
		"MemoryMax":   {"64M"},
		"CPUQuota":    {"10%"},
		"Environment": {"GOMEMLIMIT=40MiB"},
	} {
		if got := u["Service"][key]; !slices.Equal(got, want) {
			t.Errorf("[Service] %s = %q, want %q", key, got, want)
		}
	}
	for _, key := range []string{"MemoryHigh", "MemorySwapMax", "CPUWeight", "CPUShares", "MemoryLimit"} {
		if v, ok := u["Service"][key]; ok {
			t.Errorf("[Service] %s=%q: бюджет — только MemoryMax/CPUQuota/GOMEMLIMIT", key, v)
		}
	}
}

// ProtectClock= and a syscall filter would break adjtimex(2) of the clock
// collector; LoadCredential= needs systemd 247; DynamicUser= would lose the
// buffer owner; MemoryLimit= is deprecated (237 applies MemoryMax= itself).
func TestTelemetryUnitForbiddenKeys(t *testing.T) {
	t.Parallel()
	u := parseUnit(t, telemetryUnitFile)
	for _, key := range []string{"ProtectClock", "SystemCallFilter", "LoadCredential", "SetCredential",
		"DynamicUser", "MemoryLimit", "PrivateNetwork", "PrivateUsers", "KillMode"} {
		for section, keys := range u {
			if v, ok := keys[key]; ok {
				t.Errorf("[%s] %s=%q запрещён (Task 18)", section, key, v)
			}
		}
	}
}

func TestSysusersConf(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("sysusers.d/stvor-agent.conf")
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for l := range strings.SplitSeq(string(data), "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			lines = append(lines, l)
		}
	}
	// Four columns: the shell column needs systemd 238.
	if len(lines) != 1 || lines[0] != `u stvor-telemetry - "Stvor agent telemetry" -` {
		t.Errorf("sysusers lines = %q", lines)
	}
}
