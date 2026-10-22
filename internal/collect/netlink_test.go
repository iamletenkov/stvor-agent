package collect

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"slices"
	"syscall"
	"testing"
)

// Netlink fixtures under testdata/netlink:
//   - container-getlink.bin — a real RTM_GETLINK dump taken with
//     syscall.NetlinkRIB inside a Docker container (lo, eth0; no CAN);
//   - the other files are built by the functions below, byte for byte after
//     the kernel UAPI (linux/rtnetlink.h, linux/can/netlink.h,
//     linux/nl80211.h, linux/genetlink.h): no CAN or Wi-Fi hardware on the
//     hosts the fixtures were taken from. TestNetlinkFixturesUpToDate keeps
//     the files equal to the builders; STVOR_UPDATE_NETLINK=1 rewrites them.

func nlattr(typ uint16, data []byte) []byte {
	l := nlaHdrLen + len(data)
	out := make([]byte, nlAlign(l))
	binary.NativeEndian.PutUint16(out, uint16(l))
	binary.NativeEndian.PutUint16(out[2:], typ)
	copy(out[nlaHdrLen:], data)
	return out
}

func nlnest(typ uint16, attrs ...[]byte) []byte {
	return nlattr(typ|nlaFNested, bytes.Join(attrs, nil))
}

func nlmsg(typ, flags uint16, payload ...[]byte) []byte {
	body := bytes.Join(payload, nil)
	out := make([]byte, syscall.NLMSG_HDRLEN, syscall.NLMSG_HDRLEN+len(body))
	binary.NativeEndian.PutUint32(out, uint32(syscall.NLMSG_HDRLEN+len(body)))
	binary.NativeEndian.PutUint16(out[4:], typ)
	binary.NativeEndian.PutUint16(out[6:], flags)
	binary.NativeEndian.PutUint32(out[8:], 1) // seq
	return append(out, body...)
}

func le16(v uint16) []byte { return binary.NativeEndian.AppendUint16(nil, v) }
func le32(v uint32) []byte { return binary.NativeEndian.AppendUint32(nil, v) }
func cstr(s string) []byte { return append([]byte(s), 0) }

// ifinfomsg: family, pad, type (ARPHRD_*), index, flags, change.
func ifinfo(arphrd uint16, index int32) []byte {
	b := make([]byte, 16)
	binary.NativeEndian.PutUint16(b[2:], arphrd)
	binary.NativeEndian.PutUint32(b[4:], uint32(index))
	binary.NativeEndian.PutUint32(b[8:], syscall.IFF_UP|syscall.IFF_RUNNING)
	return b
}

func done() []byte { return nlmsg(syscall.NLMSG_DONE, syscall.NLM_F_MULTI, le32(0)) }

func link(arphrd uint16, index int32, name string, attrs ...[]byte) []byte {
	return nlmsg(syscall.RTM_NEWLINK, syscall.NLM_F_MULTI,
		append([][]byte{ifinfo(arphrd, index), nlattr(syscall.IFLA_IFNAME, cstr(name))}, attrs...)...)
}

// canXstats is struct can_device_stats: bus_error, error_warning,
// error_passive, bus_off, arbitration_lost, restarts.
func canXstats(v ...uint32) []byte {
	var b []byte
	for _, x := range v {
		b = binary.NativeEndian.AppendUint32(b, x)
	}
	return nlattr(iflaInfoXstats, b)
}

const arphrdCAN = 280

// buildCANDebian12: PEAK USB adapter on kernel 6.1 — error-passive, error
// counters, xstats; NLA_F_NESTED set as nla_nest_start sets it since 5.2.
func buildCANDebian12() []byte {
	return slices.Concat(
		link(syscall.ARPHRD_LOOPBACK, 1, "lo", nlattr(syscall.IFLA_MTU, le32(65536))),
		link(syscall.ARPHRD_ETHER, 2, "enp3s0", nlattr(syscall.IFLA_MTU, le32(1500)),
			nlattr(syscall.IFLA_ADDRESS, []byte{0x3c, 0x7c, 0x3f, 0x10, 0x20, 0x30})),
		link(arphrdCAN, 3, "can0", nlattr(syscall.IFLA_MTU, le32(16)),
			nlnest(syscall.IFLA_LINKINFO,
				nlattr(iflaInfoKind, cstr("can")),
				nlnest(iflaInfoData,
					nlattr(1, make([]byte, 32)), // IFLA_CAN_BITTIMING
					nlattr(3, le32(80000000)),   // IFLA_CAN_CLOCK
					nlattr(iflaCANState, le32(2)),
					nlattr(5, make([]byte, 8)), // IFLA_CAN_CTRLMODE
					nlattr(6, le32(100)),       // IFLA_CAN_RESTART_MS
					nlattr(iflaCANBerrCounter, slices.Concat(le16(128), le16(3))),
				),
				canXstats(17, 3, 1, 0, 0, 0),
			)),
		done(),
	)
}

// buildCANJetson: gs_usb on kernel 4.9 — no error counters (the driver has
// no do_get_berr_counter), two restarts after bus-off; nests without
// NLA_F_NESTED as 4.9 sends them.
func buildCANJetson() []byte {
	nest := func(typ uint16, attrs ...[]byte) []byte { return nlattr(typ, bytes.Join(attrs, nil)) }
	return slices.Concat(
		link(syscall.ARPHRD_LOOPBACK, 1, "lo"),
		link(syscall.ARPHRD_ETHER, 2, "dummy0", nest(syscall.IFLA_LINKINFO, nlattr(iflaInfoKind, cstr("dummy")))),
		link(syscall.ARPHRD_ETHER, 3, "eth0"),
		link(arphrdCAN, 6, "can0",
			nest(syscall.IFLA_LINKINFO,
				nlattr(iflaInfoKind, cstr("can")),
				nest(iflaInfoData, nlattr(iflaCANState, le32(0)), nlattr(6, le32(0))),
				canXstats(0, 2, 2, 2, 0, 2),
			)),
		done(),
	)
}

const familyNL80211 = 34

func genl(cmd uint8, attrs ...[]byte) []byte {
	return slices.Concat(append([][]byte{{cmd, 1, 0, 0}}, attrs...)...)
}

// buildFamilyReply: CTRL_CMD_NEWFAMILY for nl80211.
func buildFamilyReply() []byte {
	return nlmsg(genlIDCtrl, 0, genl(1,
		nlattr(ctrlAttrFamilyID, le16(familyNL80211)),
		nlattr(ctrlAttrFamilyName, cstr("nl80211")),
		nlattr(3, le32(1)), // CTRL_ATTR_VERSION
	))
}

// buildFamilyMissing: NLMSG_ERROR -ENOENT (no cfg80211 in the kernel).
func buildFamilyMissing() []byte {
	errno := -int32(syscall.ENOENT)
	return nlmsg(syscall.NLMSG_ERROR, 0, binary.NativeEndian.AppendUint32(nil, uint32(errno)), make([]byte, 16))
}

// buildStationUbuntu: NL80211_CMD_NEW_STATION of the access point, with the
// BSSID (NL80211_ATTR_MAC) and an SSID attribute that must never leave the
// parser.
func buildStationUbuntu() []byte {
	return slices.Concat(
		nlmsg(familyNL80211, syscall.NLM_F_MULTI, genl(19,
			nlattr(3, le32(3)), // NL80211_ATTR_IFINDEX
			nlattr(6, []byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}), // NL80211_ATTR_MAC — BSSID
			nlattr(52, []byte("corp-wifi")),                       // NL80211_ATTR_SSID
			nlattr(46, le32(7)),                                   // NL80211_ATTR_GENERATION
			nlnest(nl80211AttrStaInfo,
				nlattr(1, le32(40)),       // INACTIVE_TIME
				nlattr(2, le32(99887766)), // RX_BYTES
				nlattr(staInfoSignal, []byte{0xcd}),
				nlnest(staInfoTxBitrate,
					nlattr(rateInfoBitrate, le16(8667)),
					nlattr(rateInfoBitrate32, le32(8667)),
				),
				nlnest(staInfoRxBitrate, nlattr(rateInfoBitrate, le16(7800))),
			),
		)),
		done(),
	)
}

func netlinkFixtures() map[string][]byte {
	return map[string][]byte{
		"can-debian12.bin":       buildCANDebian12(),
		"can-jetson.bin":         buildCANJetson(),
		"genl-family.bin":        buildFamilyReply(),
		"genl-family-enoent.bin": buildFamilyMissing(),
		"station-ubuntu.bin":     buildStationUbuntu(),
	}
}

func readNetlink(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/netlink/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestNetlinkFixturesUpToDate(t *testing.T) {
	t.Parallel()
	for name, want := range netlinkFixtures() {
		path := "testdata/netlink/" + name
		if os.Getenv("STVOR_UPDATE_NETLINK") == "1" {
			if err := os.WriteFile(path, want, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s differs from its builder (STVOR_UPDATE_NETLINK=1 rewrites)", path)
		}
	}
}

func TestParseLinkDump(t *testing.T) {
	t.Parallel()
	tests := []struct {
		file  string
		names []string
		can   map[string]canInfo
	}{
		{
			file:  "container-getlink.bin",
			names: []string{"lo", "eth0"},
			can:   map[string]canInfo{},
		},
		{
			file:  "can-debian12.bin",
			names: []string{"lo", "enp3s0", "can0"},
			can: map[string]canInfo{"can0": {
				state: 2, hasState: true, txErr: 128, rxErr: 3, hasBerr: true,
				stats: [6]uint32{17, 3, 1, 0, 0, 0}, hasStats: true,
			}},
		},
		{
			file:  "can-jetson.bin",
			names: []string{"lo", "dummy0", "eth0", "can0"},
			can: map[string]canInfo{"can0": {
				state: 0, hasState: true, stats: [6]uint32{0, 2, 2, 2, 0, 2}, hasStats: true,
			}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.file, func(t *testing.T) {
			t.Parallel()
			links, err := parseLinkDump(readNetlink(t, tc.file))
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			can := map[string]canInfo{}
			for _, l := range links {
				names = append(names, l.name)
				if l.can != nil {
					can[l.name] = *l.can
				}
			}
			if !slices.Equal(names, tc.names) {
				t.Errorf("names = %v, want %v", names, tc.names)
			}
			if len(can) != len(tc.can) {
				t.Errorf("CAN links = %+v, want %+v", can, tc.can)
			}
			for name, want := range tc.can {
				if can[name] != want {
					t.Errorf("%s = %+v, want %+v", name, can[name], want)
				}
			}
		})
	}
}

func TestParseLinkDumpMalformed(t *testing.T) {
	t.Parallel()
	good := buildCANDebian12()
	tests := []struct {
		name string
		b    []byte
	}{
		{"truncated header", good[:10]},
		{"length beyond buffer", func() []byte {
			b := slices.Clone(good)
			binary.NativeEndian.PutUint32(b, uint32(len(b)+100))
			return b
		}()},
		{"short ifinfomsg", nlmsg(syscall.RTM_NEWLINK, 0, make([]byte, 8))},
		{"attribute beyond message", nlmsg(syscall.RTM_NEWLINK, 0, ifinfo(1, 1), []byte{0xff, 0x00, 0x03, 0x00})},
		{"kernel error", buildFamilyMissing()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := parseLinkDump(tc.b); err == nil {
				t.Error("malformed dump must be an error")
			}
		})
	}
	// A short CAN payload is ignored, not trusted.
	short := link(arphrdCAN, 3, "can0", nlnest(syscall.IFLA_LINKINFO,
		nlattr(iflaInfoKind, cstr("can")),
		nlnest(iflaInfoData, nlattr(iflaCANState, []byte{1}), nlattr(iflaCANBerrCounter, []byte{1, 2})),
		nlattr(iflaInfoXstats, []byte{1, 2, 3}),
	))
	links, err := parseLinkDump(short)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || links[0].can == nil || links[0].can.hasState || links[0].can.hasBerr || links[0].can.hasStats {
		t.Errorf("short CAN attributes must be dropped: %+v", links)
	}
}

func TestParseFamilyID(t *testing.T) {
	t.Parallel()
	id, err := parseFamilyID(readNetlink(t, "genl-family.bin"))
	if err != nil || id != familyNL80211 {
		t.Errorf("family = %d, %v; want %d", id, err, familyNL80211)
	}
	if _, err := parseFamilyID(readNetlink(t, "genl-family-enoent.bin")); !errors.Is(err, ErrAbsent) {
		t.Errorf("ENOENT must be ErrAbsent, got %v", err)
	}
	if _, err := parseFamilyID(nlmsg(genlIDCtrl, 0, genl(1, nlattr(3, le32(1))))); err == nil {
		t.Error("a reply without the family id must be an error")
	}
}

func TestParseStationDump(t *testing.T) {
	t.Parallel()
	st, err := parseStationDump(readNetlink(t, "station-ubuntu.bin"))
	if err != nil {
		t.Fatal(err)
	}
	want := []wifiStation{{
		signal: -51, hasSignal: true,
		txBitrate: 866.7e6, hasTx: true,
		rxBitrate: 780e6, hasRx: true,
	}}
	if len(st) != 1 || !near(st[0].txBitrate, want[0].txBitrate) || !near(st[0].rxBitrate, want[0].rxBitrate) ||
		st[0].signal != want[0].signal || !st[0].hasSignal || !st[0].hasTx || !st[0].hasRx {
		t.Errorf("stations = %+v, want %+v", st, want)
	}
	// Empty dump (not associated) — no station.
	st, err = parseStationDump(done())
	if err != nil || len(st) != 0 {
		t.Errorf("empty dump = %+v, %v", st, err)
	}
}

// The real nl80211 path on a non-wireless interface (lo) yields no station:
// ErrAbsent (no cfg80211 — ENOENT for the family; ENODEV for lo) or an
// empty dump. Any other error is a bug in the exchange.
func TestNL80211StationsNotWireless(t *testing.T) {
	t.Parallel()
	b, err := NL80211Stations(1)
	if err != nil {
		if !errors.Is(err, ErrAbsent) {
			t.Errorf("lo: %v, want ErrAbsent", err)
		}
		return
	}
	st, err := parseStationDump(b)
	if err != nil || len(st) != 0 {
		t.Errorf("lo: stations %+v, %v", st, err)
	}
}

func TestRouteLinkDumpParses(t *testing.T) {
	t.Parallel()
	b, err := RouteLinkDump()
	if err != nil {
		t.Skipf("rtnetlink unavailable: %v", err)
	}
	links, err := parseLinkDump(b)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(links, func(l linkInfo) bool { return l.name == "lo" }) {
		t.Errorf("no lo in the live dump: %+v", links)
	}
}

// A dump that fails inside its callback reports the errno in the payload of
// NLMSG_DONE, not in NLMSG_ERROR.
func TestNLMessagesDoneErrno(t *testing.T) {
	t.Parallel()
	enodev := nlmsg(syscall.NLMSG_DONE, syscall.NLM_F_MULTI, negErrno(syscall.ENODEV))
	if _, err := nlMessages(enodev); !errors.Is(err, ErrAbsent) {
		t.Errorf("DONE with -ENODEV: %v, want ErrAbsent", err)
	}
	eperm := nlmsg(syscall.NLMSG_DONE, syscall.NLM_F_MULTI, negErrno(syscall.EPERM))
	if _, err := nlMessages(eperm); !errors.Is(err, syscall.EPERM) || errors.Is(err, ErrAbsent) {
		t.Errorf("DONE with -EPERM: %v", err)
	}
	if msgs, err := nlMessages(done()); err != nil || len(msgs) != 0 {
		t.Errorf("plain DONE: %v, %v", msgs, err)
	}
}

// negErrno is the kernel's negative errno as it travels in a message.
func negErrno(e syscall.Errno) []byte {
	return binary.NativeEndian.AppendUint32(nil, ^uint32(e)+1)
}

// withSeq sets the sequence number of every message in b.
func withSeq(b []byte, seq uint32) []byte {
	b = slices.Clone(b)
	for off := 0; off+syscall.NLMSG_HDRLEN <= len(b); {
		binary.NativeEndian.PutUint32(b[off+8:], seq)
		off += nlAlign(int(binary.NativeEndian.Uint32(b[off:])))
	}
	return b
}

// Only the kernel (port id 0) answers, and only messages with the request's
// sequence number belong to the exchange.
func TestAcceptReply(t *testing.T) {
	t.Parallel()
	kernel := &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}
	ours := withSeq(buildStationUbuntu(), 7)
	other := withSeq(done(), 8)
	if got := acceptReply(&syscall.SockaddrNetlink{Family: syscall.AF_NETLINK, Pid: 4242}, ours, 7); got != nil {
		t.Errorf("a datagram from user space must be dropped: %d bytes", len(got))
	}
	if got := acceptReply(&syscall.SockaddrInet4{}, ours, 7); got != nil {
		t.Errorf("a non-netlink sender must be dropped")
	}
	if got := acceptReply(kernel, ours, 7); !bytes.Equal(got, ours) {
		t.Errorf("our reply must pass unchanged")
	}
	mixed := append(slices.Clone(other), ours...)
	if got := acceptReply(kernel, mixed, 7); !bytes.Equal(got, ours) {
		t.Errorf("a message of another sequence must be dropped: %d bytes, want %d", len(got), len(ours))
	}
	if got := acceptReply(kernel, other, 7); len(got) != 0 {
		t.Errorf("only foreign messages: %d bytes", len(got))
	}
	// A truncated datagram is passed on for the parser to reject.
	if got := acceptReply(kernel, ours[:10], 7); !bytes.Equal(got, ours[:10]) {
		t.Errorf("truncated datagram must reach the parser")
	}
}

// netlinkSeeds are the fixture dumps, each also cut at a few lengths and
// with a corrupted length field: what a short read or a buggy driver could
// hand the parsers.
func netlinkSeeds() [][]byte {
	var out [][]byte
	for _, b := range [][]byte{
		buildCANDebian12(), buildCANJetson(), buildFamilyReply(), buildFamilyMissing(), buildStationUbuntu(), done(),
	} {
		out = append(out, b)
		for _, n := range []int{1, 4, syscall.NLMSG_HDRLEN, syscall.NLMSG_HDRLEN + 3, len(b) / 2, len(b) - 1} {
			if n < len(b) {
				out = append(out, b[:n])
			}
		}
		bad := slices.Clone(b)
		if len(bad) > syscall.NLMSG_HDRLEN+nlaHdrLen {
			binary.NativeEndian.PutUint16(bad[syscall.NLMSG_HDRLEN+genlHdrLen:], 0xffff)
		}
		out = append(out, bad)
	}
	return append(out, nil, []byte{0xff, 0xff, 0xff, 0xff})
}

// FuzzParseLinkDump and FuzzParseStationDump run their seed corpus in every
// `go test`: malformed or truncated messages are errors, never panics.
func FuzzParseLinkDump(f *testing.F) {
	for _, b := range netlinkSeeds() {
		f.Add(b)
	}
	f.Fuzz(func(_ *testing.T, b []byte) {
		_, _ = parseLinkDump(b)
	})
}

func FuzzParseStationDump(f *testing.F) {
	for _, b := range netlinkSeeds() {
		f.Add(b)
	}
	f.Fuzz(func(_ *testing.T, b []byte) {
		_, _ = parseStationDump(b)
		_, _ = parseFamilyID(b)
		_ = acceptReply(&syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}, b, 1)
	})
}
