package collect

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
)

// Network metrics (portal contract v1). Counters are /proc/net/dev raw
// values; link properties come from /sys/class/net/<iface>.
const (
	MetricNetIO             = "system.network.io"              // counter, bytes; iface, direction
	MetricNetPackets        = "system.network.packets"         // counter; iface, direction
	MetricNetErrors         = "system.network.errors"          // counter; iface, direction
	MetricNetDropped        = "system.network.dropped"         // counter; iface, direction
	MetricNetLinkUp         = "system.network.link.up"         // gauge 0/1; iface
	MetricNetCarrierChanges = "system.network.carrier_changes" // counter; iface
	MetricNetSpeed          = "system.network.speed"           // gauge, bit/s; iface
	MetricNetDuplex         = "system.network.duplex"          // gauge 0 half / 1 full; iface
	MetricNetMTU            = "system.network.mtu"             // gauge, bytes; iface
	MetricWiFiSignal        = "system.network.wifi.signal"     // gauge, dBm; iface
	MetricWiFiBitrate       = "system.network.wifi.bitrate"    // gauge, bit/s (transmit); iface
	MetricCANState          = "system.network.can.state"       // gauge, enum can_state; iface
	MetricCANErrorCounter   = "system.network.can.error_counter"
	MetricCANBusErrors      = "system.network.can.bus_errors" // counter; iface
	MetricCANRestarts       = "system.network.can.restarts"   // counter; iface
)

// maxInterfaces is ifaces_per_machine_max of the contract.
const maxInterfaces = 32

const sysNet = "sys/class/net"

// ARPHRD_CAN: the link type of CAN interfaces (physical, vcan, slcan).
const arphrdCANType = 280

// deniedIfacePrefixes are virtual interfaces never reported, even with a
// device link (spec §4): containers, bridges, VPN and loopback.
func deniedIfacePrefixes() []string {
	return []string{"lo", "veth", "docker", "br-", "virbr", "tailscale", "cni", "flannel", "cali", "vxlan",
		"tun", "tap", "wg", "zt", "vnet", "fwbr", "fwpr", "fwln", "vmbr"}
}

type ifaceKind uint8

const (
	ifaceSkip ifaceKind = iota
	ifacePhysical
	ifaceCAN
	ifaceWiFi
	ifaceWWAN
)

// netDev is one /proc/net/dev line: rx bytes, packets, errs, drop at 0–3,
// tx at 8–11.
type netDev struct {
	name string
	v    [16]uint64
	kind ifaceKind
}

// Network collects the "fast" group of the network: traffic counters and
// link state of the physical, CAN, Wi-Fi and WWAN interfaces (at most
// maxInterfaces, by name), the Wi-Fi signal and bitrate, and the CAN bus
// state. Speed, duplex and MTU are the "slow" group — NetworkLinks.
func (c Collector) Network(s *Snapshot) error {
	devs, err := c.interfaces()
	if err != nil {
		return err
	}
	var errs []error
	var wireless map[string]float64
	hasCAN := false
	for _, d := range devs {
		c.netCounters(s, d)
		c.linkState(s, d.name)
		switch d.kind {
		case ifaceCAN:
			hasCAN = true
		case ifaceWiFi:
			s.addCap(CapWiFi)
			if wireless == nil {
				wireless = c.procNetWireless()
			}
			if err := c.wifi(s, d.name, wireless); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if hasCAN {
		s.addCap(CapCAN)
		errs = append(errs, c.canStates(s, devs))
	}
	return errors.Join(errs...)
}

// NetworkLinks collects the "slow" group of the network (contract v1,
// 60 s): speed, duplex and MTU of the same interfaces as Network.
func (c Collector) NetworkLinks(s *Snapshot) error {
	devs, err := c.interfaces()
	for _, d := range devs {
		c.linkProps(s, d.name)
	}
	return err
}

// interfaces lists the reported interfaces of /proc/net/dev (none when the
// file is absent).
func (c Collector) interfaces() ([]netDev, error) {
	data, absent, err := c.readFile("proc/net/dev")
	if absent || err != nil {
		return nil, err
	}
	_, statErr := fs.Stat(c.Root, sysNet)
	haveSysfs := statErr == nil
	var devs []netDev
	for line := range bytes.Lines(data) {
		d, ok := parseNetDevLine(line)
		if !ok {
			continue
		}
		if d.kind = c.ifaceKind(d.name, haveSysfs); d.kind != ifaceSkip {
			devs = append(devs, d)
		}
	}
	slices.SortFunc(devs, func(a, b netDev) int { return strings.Compare(a.name, b.name) })
	return devs[:min(len(devs), maxInterfaces)], nil
}

// parseNetDevLine parses "  name: 16 numbers"; the header lines and
// anything malformed are skipped.
func parseNetDevLine(line []byte) (netDev, bool) {
	var d netDev
	colon := bytes.IndexByte(line, ':')
	if colon < 0 {
		return d, false
	}
	d.name = string(bytes.TrimSpace(line[:colon]))
	if !validIface(d.name) {
		return d, false
	}
	rest := line[colon+1:]
	for i := range d.v {
		var f []byte
		f, rest = nextField(rest)
		x, ok := parseUint(f)
		if !ok {
			return d, false
		}
		d.v[i] = x
	}
	return d, true
}

// validIface is the iface label pattern of the contract.
func validIface(name string) bool {
	if len(name) == 0 || len(name) > 15 {
		return false
	}
	return labelChars(name, "_.:@-")
}

// labelChars: every byte is an ASCII letter, a digit or one of extra.
func labelChars(v, extra string) bool {
	for i := range len(v) {
		ch := v[i]
		alnum := ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9'
		if !alnum && strings.IndexByte(extra, ch) < 0 {
			return false
		}
	}
	return true
}

// ifaceKind keeps CAN (by link type), Wi-Fi (wireless or phy80211 in
// sysfs), WWAN (by name) and devices with a "device" link (physical); the
// deny list wins. Without /sys/class/net at all only the deny list applies.
func (c Collector) ifaceKind(name string, haveSysfs bool) ifaceKind {
	for _, p := range deniedIfacePrefixes() {
		if strings.HasPrefix(name, p) {
			return ifaceSkip
		}
	}
	if !haveSysfs {
		return ifacePhysical
	}
	dir := sysNet + "/" + name
	if !c.exists(dir) {
		return ifaceSkip
	}
	if t, ok := c.sysfsUint(dir + "/type"); ok && t == arphrdCANType {
		return ifaceCAN
	}
	if c.exists(dir+"/wireless") || c.exists(dir+"/phy80211") {
		return ifaceWiFi
	}
	if strings.HasPrefix(name, "wwan") {
		return ifaceWWAN
	}
	if c.exists(dir + "/device") {
		return ifacePhysical
	}
	return ifaceSkip
}

func (c Collector) exists(name string) bool {
	_, err := fs.Stat(c.Root, name)
	return err == nil
}

func (c Collector) netCounters(s *Snapshot, d netDev) {
	iface := Attr{AttrIface, d.name}
	rx, tx := Attr{AttrDirection, "receive"}, Attr{AttrDirection, "transmit"}
	for i, name := range [4]string{MetricNetIO, MetricNetPackets, MetricNetErrors, MetricNetDropped} {
		s.add(name, Counter, float64(d.v[i]), iface, rx)
		s.add(name, Counter, float64(d.v[8+i]), iface, tx)
	}
}

// linkState reads operstate and carrier_changes (fast group). sysfs
// answers EINVAL for the carrier of a down link and an interface can vanish
// between reads, so an unreadable attribute is absent, not an error.
func (c Collector) linkState(s *Snapshot, name string) {
	dir := sysNet + "/" + name + "/"
	iface := Attr{AttrIface, name}
	if up, ok := c.linkUp(dir); ok {
		s.add(MetricNetLinkUp, Gauge, up, iface)
	}
	if v, ok := c.sysfsUint(dir + "carrier_changes"); ok {
		s.add(MetricNetCarrierChanges, Counter, float64(v), iface)
	}
}

// linkProps reads speed, duplex and mtu (slow group); sysfs answers EINVAL
// for speed/duplex of a down link: absent, not an error.
func (c Collector) linkProps(s *Snapshot, name string) {
	dir := sysNet + "/" + name + "/"
	iface := Attr{AttrIface, name}
	if v, ok := c.sysfsUint(dir + "speed"); ok && v > 0 { // "-1" fails parseUint
		s.add(MetricNetSpeed, Gauge, float64(v)*1e6, iface)
	}
	switch c.sysfsWord(dir + "duplex") {
	case "full":
		s.add(MetricNetDuplex, Gauge, 1, iface)
	case "half":
		s.add(MetricNetDuplex, Gauge, 0, iface)
	}
	if v, ok := c.sysfsUint(dir + "mtu"); ok {
		s.add(MetricNetMTU, Gauge, float64(v), iface)
	}
}

// linkUp: operstate "up" — 1, a definite down state — 0, "unknown" (CAN,
// tun, some drivers) — the carrier file when readable.
func (c Collector) linkUp(dir string) (float64, bool) {
	switch c.sysfsWord(dir + "operstate") {
	case "up":
		return 1, true
	case "down", "lowerlayerdown", "notpresent", "dormant", "testing":
		return 0, true
	}
	if v, ok := c.sysfsUint(dir + "carrier"); ok {
		return float64(min(v, 1)), true
	}
	return 0, false
}

func (c Collector) sysfsWord(name string) string {
	data, err := fs.ReadFile(c.Root, name)
	if err != nil {
		return ""
	}
	f, _ := nextField(data)
	return string(f)
}

func (c Collector) sysfsUint(name string) (uint64, bool) {
	data, err := fs.ReadFile(c.Root, name)
	if err != nil {
		return 0, false
	}
	f, _ := nextField(data)
	return parseUint(f)
}

// procNetWireless maps interface → signal level (dBm) from the wireless
// extensions table; levels outside a plausible dBm range are dropped.
func (c Collector) procNetWireless() map[string]float64 {
	out := map[string]float64{}
	data, err := fs.ReadFile(c.Root, "proc/net/wireless")
	if err != nil {
		return out
	}
	for line := range bytes.Lines(data) {
		colon := bytes.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		name := string(bytes.TrimSpace(line[:colon]))
		// status, link, level
		rest := line[colon+1:]
		var f []byte
		for range 3 {
			f, rest = nextField(rest)
		}
		lvl, ok := parseSignedFloat(bytes.TrimSuffix(f, []byte(".")))
		if ok && lvl < 0 && lvl >= -120 {
			out[name] = lvl
		}
	}
	return out
}

func parseSignedFloat(b []byte) (float64, bool) {
	if len(b) > 0 && b[0] == '-' {
		v, ok := parseNonNegFloat(b[1:])
		return -v, ok
	}
	return parseNonNegFloat(b)
}

// wifi reports the signal (/proc/net/wireless, else nl80211) and the
// transmit bitrate (nl80211) of a client interface: one station only — an
// access point with many stations has no single bitrate. Nothing that
// identifies the network (SSID, BSSID) is read.
func (c Collector) wifi(s *Snapshot, name string, wireless map[string]float64) error {
	iface := Attr{AttrIface, name}
	signal, haveSignal := wireless[name]
	var st wifiStation
	haveStation := false
	if c.WiFiStations != nil {
		if idx, ok := c.sysfsUint(sysNet + "/" + name + "/ifindex"); ok && idx > 0 && idx < 1<<31 {
			dump, err := c.WiFiStations(int(idx))
			switch {
			case errors.Is(err, ErrAbsent):
			case err != nil:
				if haveSignal {
					s.add(MetricWiFiSignal, Gauge, signal, iface)
				}
				return fmt.Errorf("станции Wi-Fi %s: %w", name, err)
			default:
				sts, err := parseStationDump(dump)
				if err != nil {
					return fmt.Errorf("станции Wi-Fi %s: %w", name, err)
				}
				if len(sts) == 1 {
					st, haveStation = sts[0], true
				}
			}
		}
	}
	if !haveSignal && haveStation && st.hasSignal && st.signal < 0 {
		signal, haveSignal = float64(st.signal), true
	}
	if haveSignal {
		s.add(MetricWiFiSignal, Gauge, signal, iface)
	}
	if haveStation && st.hasTx {
		s.add(MetricWiFiBitrate, Gauge, st.txBitrate, iface)
	}
	return nil
}

// canStates reads the bus state, error counters and statistics of the CAN
// interfaces from one rtnetlink dump. Attributes the driver does not report
// (no error counters on gs_usb before 6.x) are absent.
func (c Collector) canStates(s *Snapshot, devs []netDev) error {
	if c.LinkDump == nil {
		return nil
	}
	dump, err := c.LinkDump()
	if errors.Is(err, ErrAbsent) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("состояние CAN: %w", err)
	}
	links, err := parseLinkDump(dump)
	if err != nil {
		return fmt.Errorf("состояние CAN: %w", err)
	}
	for _, d := range devs {
		if d.kind != ifaceCAN {
			continue
		}
		i := slices.IndexFunc(links, func(l linkInfo) bool { return l.name == d.name && l.can != nil })
		if i < 0 {
			continue
		}
		ci, iface := links[i].can, Attr{AttrIface, d.name}
		if ci.hasState {
			s.add(MetricCANState, Gauge, float64(ci.state), iface)
		}
		if ci.hasBerr {
			s.add(MetricCANErrorCounter, Gauge, float64(ci.rxErr), iface, Attr{AttrDirection, "receive"})
			s.add(MetricCANErrorCounter, Gauge, float64(ci.txErr), iface, Attr{AttrDirection, "transmit"})
		}
		if ci.hasStats {
			s.add(MetricCANBusErrors, Counter, float64(ci.stats[canStatBusError]), iface)
			s.add(MetricCANRestarts, Counter, float64(ci.stats[canStatRestarts]), iface)
		}
	}
	return nil
}
