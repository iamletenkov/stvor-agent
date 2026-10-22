package inventory

import (
	"bytes"
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

// NIC types of the contract.
const (
	NICEthernet = "ethernet"
	NICWiFi     = "wifi"
	NICWWAN     = "wwan"
	NICCAN      = "can"
	NICOther    = "other"
)

// ephemeralIface are per-container and per-VM interfaces that come and go
// with workloads, and the br-<12 hex> bridges Docker Compose creates per
// project network; listing them would change the inventory on every
// container restart or stack change.
func ephemeralIface(name string) bool {
	for _, p := range []string{"veth", "tap", "vnet", "fwbr", "fwln", "fwpr", "cali", "lxc", "flannel.", "cni", "vxlan.calico"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return composeBridge(name)
}

// composeBridge: "br-" and the first 12 hex digits of a Docker network id.
func composeBridge(name string) bool {
	id, ok := strings.CutPrefix(name, "br-")
	if !ok || len(id) != 12 {
		return false
	}
	for i := range len(id) {
		if !isHexDigit(id[i]) {
			return false
		}
	}
	return true
}

func (r *reader) nics(ipv4 map[string][]netip.Addr) []NIC {
	ipv6 := r.ipv6()
	var out []NIC
	for _, e := range r.dir("sys/class/net") {
		name := e.Name()
		if !validName(name) || ephemeralIface(name) || name == "bonding_masters" {
			continue
		}
		dir := "sys/class/net/" + name
		n := NIC{
			Name:    name,
			Type:    r.nicType(name, dir),
			Driver:  r.linkBase(dir + "/device/driver"),
			MAC:     normalizeMAC(r.line(dir + "/address")),
			IPv4:    []string{},
			IPv6:    []string{},
			Service: r.link(dir+"/device") == "",
		}
		for _, a := range ipv4[name] {
			if len(n.IPv4) < maxAddrs {
				n.IPv4 = append(n.IPv4, a.String())
			}
		}
		for _, a := range ipv6[name] {
			if len(n.IPv6) < maxAddrs {
				n.IPv6 = append(n.IPv6, a)
			}
		}
		sortStrings(n.IPv4)
		sortStrings(n.IPv6)
		out = append(out, n)
	}
	sortNICs(out)
	if len(out) > maxNICs {
		out = out[:maxNICs]
	}
	return out
}

// sortNICs puts physical interfaces first, then service ones, each by name.
func sortNICs(n []NIC) {
	slices.SortStableFunc(n, func(a, b NIC) int {
		if a.Service != b.Service {
			if a.Service {
				return 1
			}
			return -1
		}
		return natCompare(a.Name, b.Name)
	})
}

func (r *reader) nicType(name, dir string) string {
	uevent := r.line(dir + "/uevent")
	switch {
	case strings.Contains(uevent, "DEVTYPE=wlan") || r.exists(dir+"/wireless") || r.exists(dir+"/phy80211"):
		return NICWiFi
	case strings.Contains(uevent, "DEVTYPE=wwan") || strings.HasPrefix(name, "wwan"):
		return NICWWAN
	}
	t, _ := strconv.Atoi(r.line(dir + "/type"))
	switch t {
	case 1: // ARPHRD_ETHER
		return NICEthernet
	case 280: // ARPHRD_CAN
		return NICCAN
	case 519: // ARPHRD_RAWIP: qmi_wwan in raw-IP mode
		return NICWWAN
	}
	return NICOther
}

// normalizeMAC accepts a 6-octet MAC in lower case; the all-zero address of
// lo and dummy devices is left out.
func normalizeMAC(s string) string {
	s = strings.ToLower(s)
	if len(s) != 17 || s == "00:00:00:00:00:00" {
		return ""
	}
	for i := 0; i < len(s); i++ {
		if i%3 == 2 {
			if s[i] != ':' {
				return ""
			}
		} else if !isHexDigit(s[i]) {
			return ""
		}
	}
	return s
}

func isHexDigit(c byte) bool { return isDigit(c) || c >= 'a' && c <= 'f' }

// IPv6 address flags of /proc/net/if_inet6 (linux/if_addr.h) for addresses
// that change on their own: privacy (temporary), deprecated, tentative and
// failed duplicate address detection.
const (
	ifaFTemporary  = 0x01
	ifaFDADFailed  = 0x08
	ifaFDeprecated = 0x20
	ifaFTentative  = 0x40
)

// ipv6 reads /proc/net/if_inet6 ("addr ifindex plen scope flags name"),
// leaving out addresses that rotate.
func (r *reader) ipv6() map[string][]string {
	out := map[string][]string{}
	data, ok := r.file("proc/net/if_inet6")
	if !ok {
		return out
	}
	for line := range bytes.Lines(data) {
		f := strings.Fields(string(line))
		if len(f) != 6 || len(f[0]) != 32 {
			continue
		}
		flags, err := strconv.ParseUint(f[4], 16, 32)
		if err != nil || flags&(ifaFTemporary|ifaFDADFailed|ifaFDeprecated|ifaFTentative) != 0 {
			continue
		}
		var raw [16]byte
		okAddr := true
		for i := range 16 {
			v, err := strconv.ParseUint(f[0][2*i:2*i+2], 16, 8)
			if err != nil {
				okAddr = false
				break
			}
			raw[i] = byte(v)
		}
		if !okAddr {
			continue
		}
		out[f[5]] = append(out[f[5]], netip.AddrFrom16(raw).String())
	}
	return out
}
