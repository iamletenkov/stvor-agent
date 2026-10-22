package collect

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"sync/atomic"
	"syscall"
	"time"
)

// Minimal netlink parsing on the standard library: rtnetlink RTM_GETLINK for
// the CAN bus state (linux/can/netlink.h) and generic netlink nl80211 for the
// Wi-Fi bitrate (linux/nl80211.h). Only the attributes below are read; the
// rest — MAC addresses, SSID, BSSID — is skipped unparsed and never leaves
// this file. Netlink uses the host byte order.

const (
	nlaHdrLen   = 4
	nlaFNested  = 0x8000
	nlaTypeMask = 0x3fff
	ifinfoLen   = 16 // struct ifinfomsg
	genlHdrLen  = 4  // struct genlmsghdr

	// IFLA_LINKINFO nested attributes (linux/if_link.h).
	iflaInfoKind   = 1
	iflaInfoData   = 2
	iflaInfoXstats = 3

	// IFLA_INFO_DATA of kind "can".
	iflaCANState       = 4 // u32 enum can_state
	iflaCANBerrCounter = 8 // struct can_berr_counter {u16 txerr, rxerr}

	// Generic netlink controller.
	genlIDCtrl         = 0x10
	ctrlCmdGetFamily   = 3
	ctrlAttrFamilyID   = 1
	ctrlAttrFamilyName = 2

	// nl80211.
	nl80211CmdGetStation = 17
	nl80211AttrIfindex   = 3
	nl80211AttrStaInfo   = 21
	staInfoSignal        = 7  // s8, dBm
	staInfoTxBitrate     = 8  // nested rate_info
	staInfoRxBitrate     = 14 // nested rate_info
	rateInfoBitrate      = 1  // u16, 100 kbit/s
	rateInfoBitrate32    = 5  // u32, 100 kbit/s

	// nlTimeout bounds one netlink exchange.
	nlTimeout = 2 * time.Second
)

// struct can_device_stats (IFLA_INFO_XSTATS): bus_error, error_warning,
// error_passive, bus_off, arbitration_lost, restarts — the contract keeps
// bus_error and restarts.
const (
	canStatBusError = 0
	canStatRestarts = 5
	canStatCount    = 6
)

func nlAlign(n int) int { return (n + 3) &^ 3 }

type nlAttr struct {
	typ  uint16
	data []byte
}

// parseAttrs splits a run of netlink attributes; a length outside the buffer
// is an error.
func parseAttrs(b []byte) ([]nlAttr, error) {
	var out []nlAttr
	for len(b) >= nlaHdrLen {
		l := int(binary.NativeEndian.Uint16(b))
		if l < nlaHdrLen || l > len(b) {
			return nil, fmt.Errorf("атрибут netlink длиной %d: %w", l, errMalformed)
		}
		out = append(out, nlAttr{typ: binary.NativeEndian.Uint16(b[2:]) & nlaTypeMask, data: b[nlaHdrLen:l]})
		b = b[min(nlAlign(l), len(b)):]
	}
	return out, nil
}

// nlMessages splits a netlink buffer and turns NLMSG_ERROR into an error
// (ENOENT, ENODEV, EOPNOTSUPP — ErrAbsent); NLMSG_DONE ends the walk.
func nlMessages(b []byte) ([]syscall.NetlinkMessage, error) {
	if len(b) < syscall.NLMSG_HDRLEN {
		return nil, fmt.Errorf("сообщение netlink короче заголовка: %w", errMalformed)
	}
	msgs, err := syscall.ParseNetlinkMessage(b)
	if err != nil {
		return nil, fmt.Errorf("сообщение netlink: %w: %w", errMalformed, err)
	}
	var out []syscall.NetlinkMessage
	for _, m := range msgs {
		switch m.Header.Type {
		case syscall.NLMSG_DONE:
			// A dump that fails inside its callback (nl80211 on a
			// non-wireless interface) puts the negative errno here.
			if len(m.Data) >= 4 {
				if err := nlErrno(m.Data); err != nil {
					return nil, err
				}
			}
			return out, nil
		case syscall.NLMSG_ERROR:
			if len(m.Data) < 4 {
				return nil, fmt.Errorf("ошибка netlink: %w", errMalformed)
			}
			if err := nlErrno(m.Data); err != nil {
				return nil, err
			}
			continue // ACK
		}
		out = append(out, m)
	}
	return out, nil
}

// nlErrno reads the negative errno at the start of NLMSG_ERROR or
// NLMSG_DONE; zero — no error. ENOENT, ENODEV, EOPNOTSUPP — ErrAbsent.
func nlErrno(b []byte) error {
	errno := syscall.Errno(-int32(binary.NativeEndian.Uint32(b))) //nolint:gosec // the kernel writes a negative int
	switch errno {
	case 0:
		return nil
	case syscall.ENOENT, syscall.ENODEV, syscall.EOPNOTSUPP:
		return fmt.Errorf("netlink: %w: %w", ErrAbsent, errno)
	}
	return fmt.Errorf("netlink: %w", errno)
}

// canInfo is the CAN part of one link; has* tell which attributes came.
type canInfo struct {
	state        uint32
	hasState     bool
	txErr, rxErr uint16
	hasBerr      bool
	stats        [canStatCount]uint32
	hasStats     bool
}

type linkInfo struct {
	name string
	kind string
	can  *canInfo // kind "can" only
}

// parseLinkDump reads the RTM_NEWLINK messages of an RTM_GETLINK dump.
func parseLinkDump(b []byte) ([]linkInfo, error) {
	msgs, err := nlMessages(b)
	if err != nil {
		return nil, err
	}
	var out []linkInfo
	for _, m := range msgs {
		if m.Header.Type != syscall.RTM_NEWLINK {
			continue
		}
		if len(m.Data) < ifinfoLen {
			return nil, fmt.Errorf("RTM_NEWLINK короче ifinfomsg: %w", errMalformed)
		}
		attrs, err := parseAttrs(m.Data[ifinfoLen:])
		if err != nil {
			return nil, err
		}
		var l linkInfo
		for _, a := range attrs {
			switch a.typ {
			case syscall.IFLA_IFNAME:
				l.name = cString(a.data)
			case syscall.IFLA_LINKINFO:
				if err := l.parseLinkInfo(a.data); err != nil {
					return nil, err
				}
			}
		}
		out = append(out, l)
	}
	return out, nil
}

func (l *linkInfo) parseLinkInfo(b []byte) error {
	attrs, err := parseAttrs(b)
	if err != nil {
		return err
	}
	var data, xstats []byte
	for _, a := range attrs {
		switch a.typ {
		case iflaInfoKind:
			l.kind = cString(a.data)
		case iflaInfoData:
			data = a.data
		case iflaInfoXstats:
			xstats = a.data
		}
	}
	if l.kind != "can" {
		return nil
	}
	ci := &canInfo{}
	l.can = ci
	if len(xstats) >= 4*canStatCount {
		for i := range ci.stats {
			ci.stats[i] = binary.NativeEndian.Uint32(xstats[4*i:])
		}
		ci.hasStats = true
	}
	attrs, err = parseAttrs(data)
	if err != nil {
		return err
	}
	for _, a := range attrs {
		switch {
		case a.typ == iflaCANState && len(a.data) >= 4:
			ci.state, ci.hasState = binary.NativeEndian.Uint32(a.data), true
		case a.typ == iflaCANBerrCounter && len(a.data) >= 4:
			ci.txErr = binary.NativeEndian.Uint16(a.data)
			ci.rxErr = binary.NativeEndian.Uint16(a.data[2:])
			ci.hasBerr = true
		}
	}
	return nil
}

func cString(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

// parseFamilyID reads CTRL_ATTR_FAMILY_ID from a CTRL_CMD_NEWFAMILY reply;
// a missing family (ENOENT) is ErrAbsent.
func parseFamilyID(b []byte) (uint16, error) {
	msgs, err := nlMessages(b)
	if err != nil {
		return 0, err
	}
	for _, m := range msgs {
		if m.Header.Type != genlIDCtrl || len(m.Data) < genlHdrLen {
			continue
		}
		attrs, err := parseAttrs(m.Data[genlHdrLen:])
		if err != nil {
			return 0, err
		}
		for _, a := range attrs {
			if a.typ == ctrlAttrFamilyID && len(a.data) >= 2 {
				return binary.NativeEndian.Uint16(a.data), nil
			}
		}
	}
	return 0, fmt.Errorf("нет CTRL_ATTR_FAMILY_ID: %w", errMalformed)
}

// wifiStation is what the agent keeps of an nl80211 station: no address,
// no SSID.
type wifiStation struct {
	signal               int8
	hasSignal            bool
	txBitrate, rxBitrate float64 // bit/s
	hasTx, hasRx         bool
}

// parseStationDump reads NL80211_ATTR_STA_INFO of each station in a
// NL80211_CMD_GET_STATION dump.
func parseStationDump(b []byte) ([]wifiStation, error) {
	msgs, err := nlMessages(b)
	if err != nil {
		return nil, err
	}
	var out []wifiStation
	for _, m := range msgs {
		if len(m.Data) < genlHdrLen {
			continue
		}
		attrs, err := parseAttrs(m.Data[genlHdrLen:])
		if err != nil {
			return nil, err
		}
		for _, a := range attrs {
			if a.typ != nl80211AttrStaInfo {
				continue
			}
			st, err := parseStaInfo(a.data)
			if err != nil {
				return nil, err
			}
			out = append(out, st)
		}
	}
	return out, nil
}

func parseStaInfo(b []byte) (wifiStation, error) {
	var st wifiStation
	attrs, err := parseAttrs(b)
	if err != nil {
		return st, err
	}
	for _, a := range attrs {
		switch a.typ {
		case staInfoSignal:
			if len(a.data) >= 1 {
				st.signal, st.hasSignal = int8(a.data[0]), true
			}
		case staInfoTxBitrate:
			st.txBitrate, st.hasTx, err = parseRateInfo(a.data)
		case staInfoRxBitrate:
			st.rxBitrate, st.hasRx, err = parseRateInfo(a.data)
		}
		if err != nil {
			return st, err
		}
	}
	return st, nil
}

// parseRateInfo prefers NL80211_RATE_INFO_BITRATE32 (rates above 6.5 Gbit/s
// overflow the u16 one); units are 100 kbit/s.
func parseRateInfo(b []byte) (float64, bool, error) {
	attrs, err := parseAttrs(b)
	if err != nil {
		return 0, false, err
	}
	var v uint32
	have := false
	for _, a := range attrs {
		switch {
		case a.typ == rateInfoBitrate32 && len(a.data) >= 4:
			v, have = binary.NativeEndian.Uint32(a.data), true
		case a.typ == rateInfoBitrate && len(a.data) >= 2 && !have:
			v, have = uint32(binary.NativeEndian.Uint16(a.data)), true
		}
	}
	if !have || v == 0 {
		return 0, false, nil
	}
	return float64(v) * 100e3, true, nil
}

// RouteLinkDump returns the RTM_GETLINK dump of the agent's network
// namespace (no privileges needed).
func RouteLinkDump() ([]byte, error) {
	b, err := syscall.NetlinkRIB(syscall.RTM_GETLINK, syscall.AF_UNSPEC)
	if err != nil {
		return nil, fmt.Errorf("RTM_GETLINK: %w", err)
	}
	return b, nil
}

// NL80211Stations resolves the nl80211 family and dumps the stations of the
// interface (no privileges needed). No cfg80211 in the kernel, or not a
// wireless interface — ErrAbsent.
func NL80211Stations(ifindex int) ([]byte, error) {
	fd, err := syscall.Socket(syscall.AF_NETLINK, syscall.SOCK_RAW|syscall.SOCK_CLOEXEC, syscall.NETLINK_GENERIC)
	if err != nil {
		return nil, fmt.Errorf("сокет genetlink: %w", err)
	}
	defer syscall.Close(fd) //nolint:errcheck // read-only socket
	tv := syscall.NsecToTimeval(nlTimeout.Nanoseconds())
	if err := syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv); err != nil {
		return nil, fmt.Errorf("тайм-аут genetlink: %w", err)
	}
	if err := syscall.Bind(fd, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}); err != nil {
		return nil, fmt.Errorf("bind genetlink: %w", err)
	}
	seq := nlSeq.Add(1)
	req := genlRequest(genlIDCtrl, syscall.NLM_F_REQUEST, ctrlCmdGetFamily, seq,
		nlAttrBytes(ctrlAttrFamilyName, append([]byte("nl80211"), 0)))
	reply, err := nlExchange(fd, req, seq, false)
	if err != nil {
		return nil, err
	}
	family, err := parseFamilyID(reply)
	if err != nil {
		return nil, err
	}
	seq = nlSeq.Add(1)
	req = genlRequest(family, syscall.NLM_F_REQUEST|syscall.NLM_F_DUMP, nl80211CmdGetStation, seq,
		nlAttrBytes(nl80211AttrIfindex, binary.NativeEndian.AppendUint32(nil, uint32(ifindex)))) //nolint:gosec // ifindex is a positive int from sysfs
	dump, err := nlExchange(fd, req, seq, true)
	if err != nil {
		return nil, err
	}
	// Errors inside the dump (ENODEV for a non-wireless interface).
	if _, err := nlMessages(dump); err != nil {
		return nil, err
	}
	return dump, nil
}

func nlAttrBytes(typ uint16, data []byte) []byte {
	l := nlaHdrLen + len(data)
	out := make([]byte, nlAlign(l))
	binary.NativeEndian.PutUint16(out, uint16(l)) //nolint:gosec // short attributes built here
	binary.NativeEndian.PutUint16(out[2:], typ)
	copy(out[nlaHdrLen:], data)
	return out
}

func genlRequest(family uint16, flags uint16, cmd uint8, seq uint32, attrs []byte) []byte {
	l := syscall.NLMSG_HDRLEN + genlHdrLen + len(attrs)
	b := make([]byte, syscall.NLMSG_HDRLEN, l)
	binary.NativeEndian.PutUint32(b, uint32(l)) //nolint:gosec // short request built here
	binary.NativeEndian.PutUint16(b[4:], family)
	binary.NativeEndian.PutUint16(b[6:], flags)
	binary.NativeEndian.PutUint32(b[8:], seq)
	b = append(b, cmd, 1, 0, 0)
	return append(b, attrs...)
}

// nlSeq numbers the requests of this process.
var nlSeq atomic.Uint32

// nlExchange sends one request to the kernel and reads the reply: one
// datagram, or until NLMSG_DONE (or an error) for a dump. Datagrams not
// from the kernel and messages of another sequence number are dropped
// (acceptReply).
func nlExchange(fd int, req []byte, seq uint32, dump bool) ([]byte, error) {
	if err := syscall.Sendto(fd, req, 0, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}); err != nil {
		return nil, fmt.Errorf("запрос netlink: %w", err)
	}
	var out []byte
	buf := make([]byte, 32<<10)
	for range 1024 {
		n, from, err := syscall.Recvfrom(fd, buf, 0)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("ответ netlink: %w", err)
		}
		msgs := acceptReply(from, buf[:n], seq)
		if len(msgs) == 0 {
			continue
		}
		out = append(out, msgs...)
		if !dump || dumpFinished(msgs) {
			return out, nil
		}
	}
	return nil, fmt.Errorf("ответ netlink без NLMSG_DONE: %w", errMalformed)
}

// acceptReply keeps the messages of a datagram that answer our request: the
// sender is the kernel (netlink port id 0, not another process) and the
// sequence number is ours. nil — a foreign datagram. A datagram that does
// not split into messages is returned whole for the parser to reject.
func acceptReply(from syscall.Sockaddr, b []byte, seq uint32) []byte {
	sa, ok := from.(*syscall.SockaddrNetlink)
	if !ok || sa.Pid != 0 {
		return nil
	}
	var out []byte
	for rest := b; len(rest) > 0; {
		if len(rest) < syscall.NLMSG_HDRLEN {
			return b
		}
		l := int(binary.NativeEndian.Uint32(rest))
		if l < syscall.NLMSG_HDRLEN || l > len(rest) {
			return b
		}
		next := min(nlAlign(l), len(rest))
		if binary.NativeEndian.Uint32(rest[8:]) == seq {
			out = append(out, rest[:next]...)
		}
		rest = rest[next:]
	}
	return out
}

func dumpFinished(b []byte) bool {
	msgs, err := syscall.ParseNetlinkMessage(b)
	if err != nil {
		return true
	}
	for _, m := range msgs {
		if m.Header.Type == syscall.NLMSG_DONE || m.Header.Type == syscall.NLMSG_ERROR {
			return true
		}
	}
	return false
}
