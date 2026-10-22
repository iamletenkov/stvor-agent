package collect

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
)

// netFixture wires the netlink hooks to the dumps of testdata/netlink.
func netFixture(t *testing.T, name, linkDump string, stations map[int]string) Collector {
	t.Helper()
	c := fixture(t, name)
	if linkDump != "" {
		b := readNetlink(t, linkDump)
		c.LinkDump = func() ([]byte, error) { return b, nil }
	}
	if stations != nil {
		c.WiFiStations = func(ifindex int) ([]byte, error) {
			f, ok := stations[ifindex]
			if !ok {
				return nil, ErrAbsent
			}
			return readNetlink(t, f), nil
		}
	}
	return c
}

func ifacesOf(s *Snapshot) []string {
	var out []string
	for _, p := range s.Points {
		for _, a := range p.Attrs {
			if a.Key == AttrIface && !slices.Contains(out, a.Value) {
				out = append(out, a.Value)
			}
		}
	}
	sort.Strings(out)
	return out
}

func TestNetwork(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		linkDump string
		stations map[int]string
		ifaces   []string
		want     map[string]float64
		kinds    map[string]Kind
		absent   []string
		caps     []string
	}{
		{
			name:     "debian12-amd64",
			linkDump: "can-debian12.bin",
			ifaces:   []string{"can0", "enp3s0"},
			want: map[string]float64{
				"system.network.io{iface=enp3s0,direction=receive}":               9876543210,
				"system.network.io{iface=enp3s0,direction=transmit}":              1234567890,
				"system.network.packets{iface=enp3s0,direction=receive}":          8123456,
				"system.network.packets{iface=enp3s0,direction=transmit}":         3456789,
				"system.network.errors{iface=enp3s0,direction=receive}":           2,
				"system.network.errors{iface=enp3s0,direction=transmit}":          0,
				"system.network.dropped{iface=enp3s0,direction=receive}":          17,
				"system.network.link.up{iface=enp3s0}":                            1,
				"system.network.carrier_changes{iface=enp3s0}":                    4,
				"system.network.speed{iface=enp3s0}":                              1e9,
				"system.network.duplex{iface=enp3s0}":                             1,
				"system.network.mtu{iface=enp3s0}":                                1500,
				"system.network.io{iface=can0,direction=receive}":                 18432000,
				"system.network.errors{iface=can0,direction=receive}":             41,
				"system.network.errors{iface=can0,direction=transmit}":            3,
				"system.network.mtu{iface=can0}":                                  16,
				"system.network.can.state{iface=can0}":                            2,
				"system.network.can.error_counter{iface=can0,direction=transmit}": 128,
				"system.network.can.error_counter{iface=can0,direction=receive}":  3,
				"system.network.can.bus_errors{iface=can0}":                       17,
				"system.network.can.restarts{iface=can0}":                         0,
			},
			kinds: map[string]Kind{
				"system.network.io{iface=enp3s0,direction=receive}":              Counter,
				"system.network.carrier_changes{iface=enp3s0}":                   Counter,
				"system.network.speed{iface=enp3s0}":                             Gauge,
				"system.network.can.state{iface=can0}":                           Gauge,
				"system.network.can.error_counter{iface=can0,direction=receive}": Gauge,
				"system.network.can.restarts{iface=can0}":                        Counter,
			},
			// CAN has no speed/duplex in sysfs; no Wi-Fi here.
			absent: []string{"system.network.speed{iface=can0}", "system.network.wifi.signal"},
			caps:   []string{CapCAN},
		},
		{
			name:     "ubuntu2404-amd64",
			stations: map[int]string{3: "station-ubuntu.bin"},
			ifaces:   []string{"enp5s0", "wlp4s0", "wwan0"},
			want: map[string]float64{
				"system.network.io{iface=wlp4s0,direction=receive}":       7766554433,
				"system.network.dropped{iface=wlp4s0,direction=receive}":  321,
				"system.network.dropped{iface=wlp4s0,direction=transmit}": 4,
				"system.network.wifi.signal{iface=wlp4s0}":                -52,
				"system.network.wifi.bitrate{iface=wlp4s0}":               866.7e6,
				"system.network.link.up{iface=wlp4s0}":                    1,
				"system.network.link.up{iface=enp5s0}":                    0,
				"system.network.carrier_changes{iface=enp5s0}":            1,
				"system.network.io{iface=wwan0,direction=transmit}":       9988776,
				"system.network.errors{iface=wwan0,direction=transmit}":   1,
			},
			// A link that is down reports speed -1 and duplex unknown: no point;
			// operstate "unknown" with no carrier file: no link state.
			absent: []string{
				"system.network.speed{iface=enp5s0}", "system.network.duplex{iface=enp5s0}",
				"system.network.link.up{iface=wwan0}", "system.network.can",
			},
			caps: []string{CapWiFi},
		},
		{
			name:     "jetson-l4t-r32",
			linkDump: "can-jetson.bin",
			ifaces:   []string{"can0", "eth0"},
			want: map[string]float64{
				"system.network.io{iface=eth0,direction=receive}": 1203344556,
				"system.network.speed{iface=eth0}":                1e9,
				"system.network.can.state{iface=can0}":            0,
				"system.network.can.restarts{iface=can0}":         2,
				"system.network.can.bus_errors{iface=can0}":       0,
				"system.network.carrier_changes{iface=can0}":      2,
			},
			// gs_usb on 4.9 has no error counters: absent, not zero.
			absent: []string{"system.network.can.error_counter"},
			caps:   []string{CapCAN},
		},
		{
			name:   "pve-vm",
			ifaces: []string{"eth0"},
			want: map[string]float64{
				"system.network.io{iface=eth0,direction=receive}":  369176773960,
				"system.network.io{iface=eth0,direction=transmit}": 50833823008,
				"system.network.link.up{iface=eth0}":               1,
				"system.network.carrier_changes{iface=eth0}":       2,
				"system.network.mtu{iface=eth0}":                   1500,
			},
			// virtio_net reports speed -1 and duplex unknown.
			absent: []string{"system.network.speed", "system.network.duplex", "system.network.wifi", "system.network.can"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := netFixture(t, tc.name, tc.linkDump, tc.stations)
			var s Snapshot
			if err := c.Network(&s); err != nil {
				t.Fatalf("Network: %v", err)
			}
			if err := c.NetworkLinks(&s); err != nil {
				t.Fatalf("NetworkLinks: %v", err)
			}
			if got := ifacesOf(&s); !slices.Equal(got, tc.ifaces) {
				t.Errorf("interfaces = %v, want %v", got, tc.ifaces)
			}
			checkPoints(t, &s, tc.want, tc.kinds, tc.absent)
			caps := slices.Clone(s.Capabilities)
			sort.Strings(caps)
			if !slices.Equal(caps, tc.caps) {
				t.Errorf("capabilities = %v, want %v", caps, tc.caps)
			}
		})
	}
}

// checkPoints compares points by key; an absent entry is a key or a prefix.
func checkPoints(t *testing.T, s *Snapshot, want map[string]float64, kinds map[string]Kind, absent []string) {
	t.Helper()
	got := pointMap(t, s)
	for k, v := range want {
		p, ok := got[k]
		if !ok {
			t.Errorf("no point %s", k)
			continue
		}
		if !near(p.Value, v) {
			t.Errorf("%s = %v, want %v", k, p.Value, v)
		}
	}
	for k, kind := range kinds {
		if p, ok := got[k]; ok && p.Kind != kind {
			t.Errorf("%s kind = %v, want %v", k, p.Kind, kind)
		}
	}
	for _, name := range absent {
		for k := range got {
			if k == name || strings.HasPrefix(k, name) {
				t.Errorf("point %s must be absent, got %v", k, got[k].Value)
			}
		}
	}
}

// Wi-Fi never leaks SSID or BSSID: only the interface name and the direction
// label the points.
func TestWiFiNoSSIDOrBSSID(t *testing.T) {
	t.Parallel()
	c := netFixture(t, "ubuntu2404-amd64", "", map[int]string{3: "station-ubuntu.bin"})
	var s Snapshot
	if err := c.Network(&s); err != nil {
		t.Fatal(err)
	}
	for _, p := range s.Points {
		for _, a := range p.Attrs {
			if a.Key != AttrIface && a.Key != AttrDirection {
				t.Errorf("%s: unexpected label %s", key(p), a.Key)
			}
			v := strings.ToLower(a.Value)
			if strings.Contains(v, "corp-wifi") || strings.Contains(v, "aa:bb") || strings.Contains(v, "aabb") {
				t.Errorf("%s leaks the network identity", key(p))
			}
		}
	}
	if dump := fmt.Sprintf("%+v", s); strings.Contains(dump, "corp-wifi") {
		t.Error("SSID found in the snapshot")
	}
}

// Without nl80211 the signal still comes from /proc/net/wireless; a failing
// station dump is reported and hides nothing else.
func TestWiFiStationErrors(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	tests := []struct {
		name    string
		hook    func(int) ([]byte, error)
		wantErr error
	}{
		{"nil hook", nil, nil},
		{"absent", func(int) ([]byte, error) { return nil, ErrAbsent }, nil},
		{"failure", func(int) ([]byte, error) { return nil, boom }, boom},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := fixture(t, "ubuntu2404-amd64")
			c.WiFiStations = tc.hook
			var s Snapshot
			err := c.Network(&s)
			if !errors.Is(err, tc.wantErr) || (tc.wantErr == nil) != (err == nil) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			checkPoints(t, &s, map[string]float64{
				"system.network.wifi.signal{iface=wlp4s0}":          -52,
				"system.network.io{iface=wlp4s0,direction=receive}": 7766554433,
			}, nil, []string{"system.network.wifi.bitrate"})
		})
	}
}

// The signal falls back to nl80211 when /proc/net/wireless has no line.
func TestWiFiSignalFromNL80211(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{
		"proc/net/dev":                      {Data: []byte("h1\nh2\nwlan0: 10 1 0 0 0 0 0 0 20 2 0 0 0 0 0 0\n")},
		"sys/class/net/wlan0/type":          {Data: []byte("1\n")},
		"sys/class/net/wlan0/ifindex":       {Data: []byte("3\n")},
		"sys/class/net/wlan0/phy80211/name": {Data: []byte("phy0\n")},
		"sys/class/net/wlan0/device/uevent": {Data: []byte("DRIVER=brcmfmac\n")},
	}
	b := buildStationUbuntu()
	c := Collector{Root: fsys, WiFiStations: func(int) ([]byte, error) { return b, nil }}
	var s Snapshot
	if err := c.Network(&s); err != nil {
		t.Fatal(err)
	}
	checkPoints(t, &s, map[string]float64{"system.network.wifi.signal{iface=wlan0}": -51}, nil, nil)
}

func TestCANDumpErrors(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	c := fixture(t, "debian12-amd64")
	c.LinkDump = func() ([]byte, error) { return nil, boom }
	var s Snapshot
	if err := c.Network(&s); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	checkPoints(t, &s, map[string]float64{"system.network.io{iface=can0,direction=receive}": 18432000},
		nil, []string{"system.network.can."})
	if !s.Has(CapCAN) {
		t.Error("a CAN interface is a capability even when its bus state is unreadable")
	}
	// No LinkDump hook — CAN interfaces still have traffic, no bus state.
	c.LinkDump = nil
	s.Reset()
	if err := c.Network(&s); err != nil {
		t.Fatal(err)
	}
	checkPoints(t, &s, nil, nil, []string{"system.network.can."})
}

// The filter: physical, CAN, Wi-Fi and WWAN only, never virtual devices even
// when they look physical.
func TestInterfaceFilter(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{}
	var dev strings.Builder
	dev.WriteString("Inter-|\n face |\n")
	add := func(name string, files map[string]string) {
		fmt.Fprintf(&dev, "%s: 1 1 0 0 0 0 0 0 1 1 0 0 0 0 0 0\n", name)
		for f, v := range files {
			fsys["sys/class/net/"+name+"/"+f] = &fstest.MapFile{Data: []byte(v + "\n")}
		}
	}
	phys := map[string]string{"type": "1", "device/uevent": "DRIVER=igb"}
	add("eno1", phys)
	add("lo", map[string]string{"type": "772"})
	add("veth9", phys)                             // denied by name even with a device link
	add("docker0", phys)                           // denied by name
	add("br-abc", phys)                            // denied by name
	add("virbr0", phys)                            // denied by name
	add("tailscale0", phys)                        // denied by name
	add("bond0", map[string]string{"type": "1"})   // no device: virtual
	add("vcan0", map[string]string{"type": "280"}) // CAN type, even virtual
	add("wlan0", map[string]string{"type": "1", "wireless/.keep": ""})
	add("wwan0", map[string]string{"type": "519"}) // WWAN by name
	add("ghost", nil)                              // no sysfs entry: gone
	fsys["proc/net/dev"] = &fstest.MapFile{Data: []byte(dev.String())}
	var s Snapshot
	if err := (Collector{Root: fsys}).Network(&s); err != nil {
		t.Fatal(err)
	}
	want := []string{"eno1", "vcan0", "wlan0", "wwan0"}
	if got := ifacesOf(&s); !slices.Equal(got, want) {
		t.Errorf("interfaces = %v, want %v", got, want)
	}
}

// At most maxInterfaces interfaces, chosen by name.
func TestInterfaceLimit(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{}
	var dev strings.Builder
	dev.WriteString("h1\nh2\n")
	for i := 39; i >= 0; i-- {
		name := fmt.Sprintf("eth%02d", i)
		fmt.Fprintf(&dev, "%s: 1 1 0 0 0 0 0 0 1 1 0 0 0 0 0 0\n", name)
		fsys["sys/class/net/"+name+"/device/uevent"] = &fstest.MapFile{Data: []byte("x\n")}
	}
	fsys["proc/net/dev"] = &fstest.MapFile{Data: []byte(dev.String())}
	var s Snapshot
	if err := (Collector{Root: fsys}).Network(&s); err != nil {
		t.Fatal(err)
	}
	got := ifacesOf(&s)
	if len(got) != maxInterfaces || got[0] != "eth00" || got[len(got)-1] != "eth31" {
		t.Errorf("interfaces = %v (%d), want eth00…eth31", got, len(got))
	}
}

func TestProcNetDevMalformed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		data string
		want []string
	}{
		{"short line skipped", "h1\nh2\neth0: 1 2 3\n", nil},
		{"non-number skipped", "h1\nh2\neth0: 1 x 0 0 0 0 0 0 1 1 0 0 0 0 0 0\n", nil},
		{"no space after colon", "h1\nh2\neth0:123 1 0 0 0 0 0 0 1 1 0 0 0 0 0 0\n", []string{"eth0"}},
		{"too long name", "h1\nh2\nabcdefghijklmnopq: 1 1 0 0 0 0 0 0 1 1 0 0 0 0 0 0\n", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fsys := fstest.MapFS{
				"proc/net/dev":                                  {Data: []byte(tc.data)},
				"sys/class/net/eth0/device/uevent":              {Data: []byte("x\n")},
				"sys/class/net/abcdefghijklmnopq/device/uevent": {Data: []byte("x\n")},
			}
			var s Snapshot
			if err := (Collector{Root: fsys}).Network(&s); err != nil {
				t.Fatal(err)
			}
			if got := ifacesOf(&s); !slices.Equal(got, tc.want) {
				t.Errorf("interfaces = %v, want %v", got, tc.want)
			}
		})
	}
}

// Link properties (speed, duplex, MTU) are the "slow" group of the contract
// (60 s): Network (fast, 10 s) does not read them, NetworkLinks reads only
// them, for the same interfaces.
func TestNetworkGroups(t *testing.T) {
	t.Parallel()
	slowNames := []string{MetricNetSpeed, MetricNetDuplex, MetricNetMTU}
	c := netFixture(t, "debian12-amd64", "can-debian12.bin", nil)
	var fast, slow Snapshot
	if err := c.Network(&fast); err != nil {
		t.Fatal(err)
	}
	if err := c.NetworkLinks(&slow); err != nil {
		t.Fatal(err)
	}
	for _, p := range fast.Points {
		if slices.Contains(slowNames, p.Name) {
			t.Errorf("fast Network emitted %s", key(p))
		}
	}
	if len(slow.Points) == 0 {
		t.Fatal("NetworkLinks emitted nothing")
	}
	for _, p := range slow.Points {
		if !slices.Contains(slowNames, p.Name) {
			t.Errorf("slow NetworkLinks emitted %s", key(p))
		}
	}
	if f, s := ifacesOf(&fast), ifacesOf(&slow); !slices.Equal(f, s) {
		t.Errorf("interfaces differ: fast %v, slow %v", f, s)
	}
	if len(slow.Capabilities) != 0 {
		t.Errorf("NetworkLinks must not report capabilities: %v", slow.Capabilities)
	}
}
