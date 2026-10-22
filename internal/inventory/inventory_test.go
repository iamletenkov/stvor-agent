package inventory

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// Fixture trees under testdata/ are copied read-only from real machines by a
// script that keeps only what a non-root user may read and recreates the
// sysfs symbolic links (drivers, USB and PCI topology):
//   - pve-vm — Proxmox VE guest (Debian 12, Q35, SeaBIOS, virtio-scsi disk
//     "drive-scsi0", Docker bridges, QEMU tablet on USB);
//   - pve-robot-debian13 — the test robot pve-robot-1, Proxmox guest with
//     Debian 13;
//   - debian13-amd64 — bare metal (Proxmox VE host on Debian 13, Huananzhi
//     X99, 2×Xeon, NVMe with LVM, UEFI with Secure Boot variable, Realtek
//     NICs, Intel USB hubs);
//   - jetson-l4t-r32 — Jetson Nano (L4T R32.6.1, kernel 4.9, arm64): device
//     tree instead of DMI, gm20b GPU as a platform device, root on a USB
//     SSD behind a hub, Intel Wi-Fi/BT, cores 2–3 may be offline.
//
// Serial numbers are placeholders; MAC addresses of real NICs keep the OUI
// with a placeholder tail; pci.ids is the subset for the fixture's devices.
// Provenance of every fixture: testdata/README.md.

var update = flag.Bool("update", false, "rewrite testdata/golden/*.json")

var fixedNow = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

func fixture(name string, un Uname, ipv4 map[string][]netip.Addr) Source {
	return Source{
		Root:     os.DirFS(filepath.Join("testdata", name)),
		Uname:    func() (Uname, error) { return un, nil },
		Hostname: func() (string, error) { return "robot-" + name, nil },
		IPv4:     func() (map[string][]netip.Addr, error) { return ipv4, nil },
		Now:      func() time.Time { return fixedNow },
	}
}

func addrs(s ...string) []netip.Addr {
	out := make([]netip.Addr, len(s))
	for i, a := range s {
		out[i] = netip.MustParseAddr(a)
	}
	return out
}

func collectFixture(t *testing.T, name string) Body {
	t.Helper()
	src := map[string]Source{
		"pve-vm": fixture("pve-vm", Uname{"6.1.0-53-amd64", "x86_64"},
			map[string][]netip.Addr{"eth0": addrs("192.0.2.10"), "docker0": addrs("172.17.0.1"), "lo": addrs("127.0.0.1")}),
		"pve-robot-debian13": fixture("pve-robot-debian13", Uname{"6.12.111+deb13-amd64", "x86_64"},
			map[string][]netip.Addr{"ens18": addrs("192.0.2.21")}),
		"debian13-amd64": fixture("debian13-amd64", Uname{"6.17.2-2-pve", "x86_64"},
			map[string][]netip.Addr{"vmbr0": addrs("192.0.2.206")}),
		"jetson-l4t-r32": fixture("jetson-l4t-r32", Uname{"4.9.253-tegra-kvm", "aarch64"},
			map[string][]netip.Addr{"eth0": addrs("192.0.2.109"), "l4tbr0": addrs("192.168.55.1")}),
	}[name]
	b, err := src.Collect()
	if err != nil {
		t.Fatalf("Collect(%s): %v", name, err)
	}
	return b
}

func fixtureNames() []string {
	return []string{"pve-vm", "pve-robot-debian13", "debian13-amd64", "jetson-l4t-r32"}
}

// TestCollectGolden: the inventory of each fixture is the golden JSON.
func TestCollectGolden(t *testing.T) {
	t.Parallel()
	for _, name := range fixtureNames() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			b := collectFixture(t, name)
			got, err := json.MarshalIndent(b, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			path := filepath.Join("testdata", "golden", name+".json")
			if *update {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("golden: %v (go test -run TestCollectGolden -update)", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("inventory of %s differs from %s:\n%s", name, path, got)
			}
			enc, err := Encode(b)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			if bytes.Contains(enc, []byte("FIXTURE-")) {
				t.Errorf("a serial number leaked in clear text")
			}
		})
	}
}

func TestCollectFacts(t *testing.T) {
	t.Parallel()
	vm := collectFixture(t, "pve-vm")
	robot := collectFixture(t, "pve-robot-debian13")
	metal := collectFixture(t, "debian13-amd64")
	jetson := collectFixture(t, "jetson-l4t-r32")

	tests := []struct {
		name      string
		got, want any
	}{
		{"pve-vm VM", vm.VM, VM{IsVM: true, Hypervisor: "KVM", Platform: "Proxmox"}},
		{"pve-robot VM", robot.VM, VM{IsVM: true, Hypervisor: "KVM", Platform: "Proxmox"}},
		{"metal not VM", metal.VM, VM{}},
		{"jetson not VM", jetson.VM, VM{}},
		{"pve-vm arch", vm.System.Arch, "amd64"},
		{"jetson arch", jetson.System.Arch, "arm64"},
		{"pve-vm OS", vm.OS.ID + " " + vm.OS.VersionID, "debian 12"},
		{"robot OS", robot.OS.ID + " " + robot.OS.VersionID, "debian 13"},
		{"jetson L4T", jetson.OS.L4T, "R32.6.1"},
		{"jetson kernel", jetson.OS.Kernel, "4.9.253-tegra-kvm"},
		{"jetson dt model", jetson.Board.DTModel, "NVIDIA Jetson Nano Developer Kit"},
		{"jetson dt compatible", jetson.Board.DTCompatible[1], "nvidia,jetson-nano"},
		{"jetson CPU", jetson.CPU, CPU{Vendor: "ARM", Model: "Cortex-A57", Cores: 4, Threads: 4}},
		{"metal CPU sockets", metal.CPU.Sockets, 2},
		{"metal CPU threads", metal.CPU.Threads, 72},
		{"metal board", metal.Board.BoardName, "X99-F8D PLUS"},
		{"metal board product placeholder", metal.Board.Version, ""},
		{"metal BIOS", metal.BIOS, BIOS{Vendor: "American Megatrends Inc.", Version: "5.11", Date: "2023-04-26", UEFI: true}},
		{"pve-vm BIOS", vm.BIOS.Vendor + " " + vm.BIOS.Date, "SeaBIOS 2014-04-01"},
		{"pve-vm legacy BIOS", vm.SecureBoot.State, SecureBootUnsupported},
		{"jetson no UEFI", jetson.SecureBoot.State, SecureBootUnsupported},
		{"jetson GPU", jetson.GPUs, []GPU{{Vendor: "NVIDIA", Model: "Maxwell GM20B (Tegra X1)", Driver: "gk20a", DriverVersion: "R32.6.1"}}},
		{"pve-vm GPU driver", vm.GPUs[0].Driver, "bochs-drm"},
		{"pve-vm disk type", diskByName(vm, "sda").Type, DiskVirtual},
		{"jetson disk on USB", diskByName(jetson, "sda").Type, DiskUSB},
		{"metal NVMe", diskByName(metal, "nvme0n1").Type, DiskNVMe},
		{"pve-vm USB tree", usbPaths(vm.USB), "usb1 usb2 usb3[3-1] usb4 usb5 usb6 usb7 usb8"},
		{"jetson USB tree", usbPaths(jetson.USB), "usb1[1-2 1-3] usb2[2-1[2-1.2]]"},
		{"jetson USB SSD driver", jetson.USB[1].Children[0].Children[0].Driver, "uas"},
		{"jetson BT driver", jetson.USB[0].Children[1].Driver, "btusb"},
		{"jetson root hub without kernel version", jetson.USB[0].Manufacturer, ""},
		{"jetson PCI tree", pciAddrs(jetson.PCI), "0000:00:01.0[0000:01:00.0] 0000:00:02.0[0000:02:00.0]"},
		{"jetson PCI driver", jetson.PCI[0].Children[0].Driver, "iwlwifi"},
		{"jetson Wi-Fi", nicByName(jetson, "wlan0").Type, NICWiFi},
		{"jetson service bridge", nicByName(jetson, "l4tbr0").Service, true},
		{"pve-vm docker0 service", nicByName(vm, "docker0").Service, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			g, _ := json.Marshal(tc.got)
			w, _ := json.Marshal(tc.want)
			if !bytes.Equal(g, w) {
				t.Errorf("got %s, want %s", g, w)
			}
		})
	}

	root := rootPartition(vm)
	if root == nil || root.FSType == "" {
		t.Errorf("pve-vm: no partition marked as root: %+v", vm.Disks)
	}
	if p := rootPartition(metal); p == nil {
		t.Errorf("metal: root on LVM is not traced to its partition: %+v", metal.Disks)
	}
	if !hasEFI(metal) {
		t.Errorf("metal: no EFI system partition marked")
	}
	for _, n := range vm.NICs {
		if strings.HasPrefix(n.Name, "veth") {
			t.Errorf("pve-vm: ephemeral %s listed", n.Name)
		}
	}
	if eth := nicByName(vm, "eth0"); eth.Driver != "virtio_net" || eth.Service || !slices.Equal(eth.IPv4, []string{"192.0.2.10"}) {
		t.Errorf("pve-vm eth0 = %+v", eth)
	}
}

func diskByName(b Body, name string) Disk {
	for _, d := range b.Disks {
		if d.Name == name {
			return d
		}
	}
	return Disk{}
}

func nicByName(b Body, name string) NIC {
	for _, n := range b.NICs {
		if n.Name == name {
			return n
		}
	}
	return NIC{}
}

func rootPartition(b Body) *Partition {
	for i := range b.Disks {
		for j := range b.Disks[i].Partitions {
			if b.Disks[i].Partitions[j].IsRoot {
				return &b.Disks[i].Partitions[j]
			}
		}
	}
	return nil
}

func hasEFI(b Body) bool {
	for _, d := range b.Disks {
		for _, p := range d.Partitions {
			if p.IsEFI {
				return true
			}
		}
	}
	return false
}

func usbPaths(us []USB) string {
	parts := make([]string, len(us))
	for i, u := range us {
		parts[i] = u.Path
		if len(u.Children) > 0 {
			parts[i] += "[" + usbPaths(u.Children) + "]"
		}
	}
	return strings.Join(parts, " ")
}

func pciAddrs(ps []PCI) string {
	parts := make([]string, len(ps))
	for i, p := range ps {
		parts[i] = p.Address
		if len(p.Children) > 0 {
			parts[i] += "[" + pciAddrs(p.Children) + "]"
		}
	}
	return strings.Join(parts, " ")
}

// TestHashStable: the hash ignores collected_at and the order of lists,
// and follows any change of the hardware.
func TestHashStable(t *testing.T) {
	t.Parallel()
	b := collectFixture(t, "pve-vm")
	h1, err := Hash(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(h1) != 64 {
		t.Fatalf("hash %q is not sha256 hex", h1)
	}

	again := collectFixture(t, "pve-vm")
	again.CollectedAt = "2030-01-01T00:00:00Z"
	if h, _ := Hash(again); h != h1 {
		t.Errorf("collected_at changed the hash")
	}

	shuffled := collectFixture(t, "pve-vm")
	slices.Reverse(shuffled.NICs)
	slices.Reverse(shuffled.USB)
	slices.Reverse(shuffled.PCI)
	slices.Reverse(shuffled.Disks[0].Partitions)
	slices.Reverse(shuffled.NICs[0].IPv4)
	Normalize(&shuffled)
	if h, _ := Hash(shuffled); h != h1 {
		t.Errorf("order of lists changed the hash")
	}

	changed := collectFixture(t, "pve-vm")
	changed.USB[2].Children[0].PID = "0002"
	if h, _ := Hash(changed); h == h1 {
		t.Errorf("a changed USB device kept the hash")
	}
}

// TestHashIgnoresAddresses: the hash leaves out the interface addresses
// (ruling of the Stvor plan controller, Task 21: a Wi-Fi link that loses and
// regains its DHCP lease must not make a new inventory version), but follows
// the interface itself; the addresses still go in the body.
func TestHashIgnoresAddresses(t *testing.T) {
	t.Parallel()
	base := collectFixture(t, "pve-vm")
	if len(base.NICs) == 0 {
		t.Fatal("fixture pve-vm has no NICs")
	}
	h0, err := Hash(base)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		change func(n *NIC)
		same   bool
	}{
		{"ipv4 gone", func(n *NIC) { n.IPv4 = []string{} }, true},
		{"ipv4 other", func(n *NIC) { n.IPv4 = []string{"198.51.100.7"} }, true},
		{"ipv6 added", func(n *NIC) { n.IPv6 = append(n.IPv6, "2001:db8::7") }, true},
		{"mac", func(n *NIC) { n.MAC = "02:00:00:00:00:99" }, false},
		{"name", func(n *NIC) { n.Name += "x" }, false},
		{"driver", func(n *NIC) { n.Driver += "x" }, false},
		{"type", func(n *NIC) { n.Type = "wifi" }, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := collectFixture(t, "pve-vm")
			tc.change(&b.NICs[0])
			ipv4 := slices.Clone(b.NICs[0].IPv4)
			h, err := Hash(b)
			if err != nil {
				t.Fatal(err)
			}
			if (h == h0) != tc.same {
				t.Errorf("hash equal = %v, want %v", h == h0, tc.same)
			}
			if !slices.Equal(b.NICs[0].IPv4, ipv4) {
				t.Errorf("Hash changed the body: ipv4 %v, want %v", b.NICs[0].IPv4, ipv4)
			}
			data, err := Encode(b)
			if err != nil {
				t.Fatal(err)
			}
			for _, a := range ipv4 {
				if !strings.Contains(string(data), a) {
					t.Errorf("body lost address %s", a)
				}
			}
		})
	}
}

// TestHashDropsAddressKeys pins the rule of contract v1 (inventory.flow), as
// the portal computes it: the keys collected_at and nics[].ipv4/ipv6 are
// absent from the hashed JSON, not empty.
func TestHashDropsAddressKeys(t *testing.T) {
	t.Parallel()
	full := collectFixture(t, "pve-vm")
	noNICs := full
	noNICs.NICs = nil // Encode gives "nics":null, which the portal hashes as null
	for name, b := range map[string]Body{"pve-vm": full, "nil nics": noNICs} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got, want := mustHash(t, b), portalHash(t, b); got != want {
				t.Errorf("Hash = %s, want %s (the body without collected_at and nics[].ipv4/ipv6)", got, want)
			}
		})
	}
}

// portalHash is the hash by the portal's rule (validate.py _hashed_view):
// from the body as sent, drop collected_at and nics[].ipv4/ipv6.
func portalHash(t *testing.T, b Body) string {
	t.Helper()
	data, err := Encode(b)
	if err != nil {
		t.Fatal(err)
	}
	var view map[string]any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&view); err != nil {
		t.Fatal(err)
	}
	delete(view, "collected_at")
	if nics, ok := view["nics"].([]any); ok {
		for _, n := range nics {
			delete(n.(map[string]any), "ipv4")
			delete(n.(map[string]any), "ipv6")
		}
	}
	want, err := canonical(view)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(want)
	return hex.EncodeToString(sum[:])
}

func mustHash(t *testing.T, b Body) string {
	t.Helper()
	h, err := Hash(b)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// TestCanonicalMatchesPython: the canonical JSON is byte for byte what the
// portal's json.dumps(sort_keys=True, separators=(",", ":"),
// ensure_ascii=False) gives for the same values — U+2028/U+2029 raw (Go
// escapes them by default), invalid UTF-8 as a raw U+FFFD, control
// characters escaped alike, a literal backslash-u text kept. The expected
// bytes were produced by CPython 3.
func TestCanonicalMatchesPython(t *testing.T) {
	t.Parallel()
	v := map[string]any{
		"a": "x\u2028y\u2029z",
		"b": "bad\xff",
		"c": `lit\u2028`,
		"d": "ctl\x01\n\t\x7f",
	}
	want := "{\"a\":\"x\u2028y\u2029z\",\"b\":\"bad\ufffd\",\"c\":\"lit\\\\u2028\",\"d\":\"ctl\\u0001\\n\\t\x7f\"}"
	got, err := canonical(v)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("canonical:\n got %q\nwant %q", got, want)
	}
}

func TestCanonicalJSON(t *testing.T) {
	t.Parallel()
	b := Body{CollectedAt: "2026-10-21T12:00:00Z", OS: OS{ID: "debian", Kernel: "6.1"}, CPU: CPU{Model: "a<b>&c", Threads: 4},
		Memory: Memory{TotalBytes: 1 << 40}}
	Normalize(&b)
	got, err := Encode(b)
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	for _, want := range []string{
		`"bios":{"uefi":false},"board":{},"cameras":[],"collected_at":"2026-10-21T12:00:00Z","cpu":{"model":"a<b>&c","threads":4}`,
		`"memory":{"total_bytes":1099511627776}`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("canonical JSON %s lacks %s", s, want)
		}
	}
	if strings.ContainsAny(s, " \n") {
		t.Errorf("canonical JSON has whitespace: %s", s)
	}

	big := Body{OS: OS{ID: "x", Kernel: "y"}, CPU: CPU{Model: "m"}}
	for range 3000 {
		big.NICs = append(big.NICs, NIC{Name: strings.Repeat("n", 100), Type: NICOther, IPv4: []string{}, IPv6: []string{}})
	}
	if _, err := Encode(big); !errors.Is(err, ErrTooLarge) {
		t.Errorf("Encode of a huge body = %v, want ErrTooLarge", err)
	}
}

func TestClean(t *testing.T) {
	t.Parallel()
	tests := []struct{ in, want string }{
		{"  Generic\x00 ", "Generic"},
		{"a\x1b[31mred\x1b[0m", "a[31mred[0m"},
		{"tab\tand\nnewline", "tab and newline"},
		{"bad\xffutf8", "badutf8"},
		{strings.Repeat("я", 300), strings.Repeat("я", 256)},
	}
	for _, tc := range tests {
		if got := clean(tc.in); got != tc.want {
			t.Errorf("clean(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	for _, p := range []string{"To Be Filled By O.E.M.", "Default string", "System Product Name"} {
		if dmi(p) != "" {
			t.Errorf("dmi(%q) is not a placeholder", p)
		}
	}
}

func TestNatCompare(t *testing.T) {
	t.Parallel()
	in := []string{"sda10", "usb10", "1-1.10", "sda2", "usb2", "1-1.2", "sda1", "1-1"}
	sortStrings(in)
	want := []string{"1-1", "1-1.2", "1-1.10", "sda1", "sda2", "sda10", "usb2", "usb10"}
	if !slices.Equal(in, want) {
		t.Errorf("natural order = %v, want %v", in, want)
	}
}

func link(target string) *fstest.MapFile {
	return &fstest.MapFile{Mode: fs.ModeSymlink, Data: []byte(target)}
}

func file(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }

func mapSource(m fstest.MapFS) Source {
	return Source{Root: m, Uname: func() (Uname, error) { return Uname{"6.8.0", "x86_64"}, nil }}
}

// TestSynthetic covers what no fixture machine has: a UVC camera, an
// NVIDIA GPU with its audio and USB-C functions, SD and eMMC cards, Secure
// Boot states and IPv6 address flags.
func TestSynthetic(t *testing.T) {
	t.Parallel()
	usbDev := "sys/devices/pci0000:00/0000:00:14.0/usb1/1-1"
	gpu := "sys/devices/pci0000:00/0000:00:01.0/0000:01:00."
	m := fstest.MapFS{
		"sys/bus/usb/devices/usb1":                              link("../../../devices/pci0000:00/0000:00:14.0/usb1"),
		"sys/bus/usb/devices/1-1":                               link("../../../devices/pci0000:00/0000:00:14.0/usb1/1-1"),
		"sys/bus/usb/devices/1-1:1.0":                           link("../../../devices/pci0000:00/0000:00:14.0/usb1/1-1/1-1:1.0"),
		"sys/bus/usb/devices/1-1:1.1":                           link("../../../devices/pci0000:00/0000:00:14.0/usb1/1-1/1-1:1.1"),
		"sys/devices/pci0000:00/0000:00:14.0/usb1/idVendor":     file("1d6b\n"),
		"sys/devices/pci0000:00/0000:00:14.0/usb1/idProduct":    file("0002\n"),
		"sys/devices/pci0000:00/0000:00:14.0/usb1/manufacturer": file("Linux 6.8.0 xhci-hcd\n"),
		usbDev + "/idVendor":                                    file("046d\n"),
		usbDev + "/idProduct":                                   file("085B\n"),
		usbDev + "/manufacturer":                                file("Logitech\n"),
		usbDev + "/product":                                     file("C925e\x07\n"),
		usbDev + "/serial":                                      file("ABC123\n"),
		usbDev + "/speed":                                       file("480\n"),
		usbDev + "/bDeviceClass":                                file("EF\n"),
		usbDev + "/1-1:1.0/driver":                              link("../../../../../../bus/usb/drivers/uvcvideo"),
		usbDev + "/1-1:1.1/driver":                              link("../../../../../../bus/usb/drivers/snd-usb-audio"),
		"sys/class/video4linux/video0":                          link("../../devices/pci0000:00/0000:00:14.0/usb1/1-1/1-1:1.0/video4linux/video0"),
		"sys/class/video4linux/video1":                          link("../../devices/pci0000:00/0000:00:14.0/usb1/1-1/1-1:1.0/video4linux/video1"),
		usbDev + "/1-1:1.0/video4linux/video0/name":             file("C925e\n"),
		usbDev + "/1-1:1.0/video4linux/video0/index":            file("0\n"),
		usbDev + "/1-1:1.0/video4linux/video1/name":             file("C925e\n"),
		usbDev + "/1-1:1.0/video4linux/video1/index":            file("1\n"),

		"sys/bus/pci/devices/0000:00:01.0":                 link("../../../devices/pci0000:00/0000:00:01.0"),
		"sys/bus/pci/devices/0000:01:00.0":                 link("../../../devices/pci0000:00/0000:00:01.0/0000:01:00.0"),
		"sys/bus/pci/devices/0000:01:00.1":                 link("../../../devices/pci0000:00/0000:00:01.0/0000:01:00.1"),
		"sys/bus/pci/devices/0000:01:00.2":                 link("../../../devices/pci0000:00/0000:00:01.0/0000:01:00.2"),
		"sys/devices/pci0000:00/0000:00:01.0/vendor":       file("0x8086\n"),
		"sys/devices/pci0000:00/0000:00:01.0/device":       file("0x1901\n"),
		"sys/devices/pci0000:00/0000:00:01.0/class":        file("0x060400\n"),
		gpu + "0/vendor":                                   file("0x10de\n"),
		gpu + "0/device":                                   file("0x1f82\n"),
		gpu + "0/class":                                    file("0x030000\n"),
		gpu + "0/driver":                                   link("../../../../bus/pci/drivers/nvidia"),
		gpu + "1/vendor":                                   file("0x10de\n"),
		gpu + "1/device":                                   file("0x10fa\n"),
		gpu + "1/class":                                    file("0x040300\n"),
		gpu + "1/driver":                                   link("../../../../bus/pci/drivers/snd_hda_intel"),
		gpu + "2/vendor":                                   file("0x10de\n"),
		gpu + "2/device":                                   file("0x1ada\n"),
		gpu + "2/class":                                    file("0x0c0330\n"),
		"proc/driver/nvidia/version":                       file("NVRM version: NVIDIA UNIX Open Kernel Module for x86_64  570.86.15  Release Build\nGCC version: gcc 12\n"),
		"proc/driver/nvidia/gpus/0000:01:00.0/information": file("Model: \t\t NVIDIA GeForce GTX 1650\nIRQ: 130\n"),
		"usr/share/misc/pci.ids":                           file("# test\n10de  NVIDIA Corporation\n\t1f82  TU117 [GeForce GTX 1650]\n\t\t1043 8765  Sub\n\t10fa  TU117 HD Audio\n8086  Intel Corporation\n\t1901  PCIe Bridge\nC 00  Unclassified\n"),

		"sys/block/mmcblk0":      link("../devices/platform/sdhci/mmc_host/mmc0/mmc0:0001/block/mmcblk0"),
		"sys/block/mmcblk0boot0": link("../devices/platform/sdhci/mmc_host/mmc0/mmc0:0001/block/mmcblk0/mmcblk0boot0"),
		"sys/block/mmcblk1":      link("../devices/platform/sdhci2/mmc_host/mmc1/mmc1:aaaa/block/mmcblk1"),
		"sys/devices/platform/sdhci/mmc_host/mmc0/mmc0:0001/block/mmcblk0/size":                file("61071360\n"),
		"sys/devices/platform/sdhci/mmc_host/mmc0/mmc0:0001/type":                              file("MMC\n"),
		"sys/devices/platform/sdhci/mmc_host/mmc0/mmc0:0001/name":                              file("DG4032\n"),
		"sys/devices/platform/sdhci/mmc_host/mmc0/mmc0:0001/serial":                            file("0x12345678\n"),
		"sys/devices/platform/sdhci/mmc_host/mmc0/mmc0:0001/block/mmcblk0/device":              link("../../../mmc0:0001"),
		"sys/devices/platform/sdhci/mmc_host/mmc0/mmc0:0001/block/mmcblk0/mmcblk0p1/partition": file("1\n"),
		"sys/devices/platform/sdhci/mmc_host/mmc0/mmc0:0001/block/mmcblk0/mmcblk0p1/size":      file("61069312\n"),
		"sys/devices/platform/sdhci/mmc_host/mmc0/mmc0:0001/block/mmcblk0/mmcblk0p1/dev":       file("179:1\n"),
		"sys/devices/platform/sdhci/mmc_host/mmc0/mmc0:0001/block/mmcblk0/mmcblk0boot0/size":   file("8192\n"),
		"sys/devices/platform/sdhci2/mmc_host/mmc1/mmc1:aaaa/block/mmcblk1/size":               file("124735488\n"),
		"sys/devices/platform/sdhci2/mmc_host/mmc1/mmc1:aaaa/block/mmcblk1/device":             link("../../../mmc1:aaaa"),
		"sys/devices/platform/sdhci2/mmc_host/mmc1/mmc1:aaaa/type":                             file("SD\n"),
		"proc/self/mountinfo": file("22 1 179:1 / / rw,relatime shared:1 - ext4 /dev/mmcblk0p1 rw\n" +
			"23 22 179:1 / /srv/my\\040data rw - ext4 /dev/mmcblk0p1 rw\n"),

		"proc/net/if_inet6": file("20010db8000000000000000000000001 02 40 00 80 eth0\n" +
			"20010db8000000000000000000000002 02 40 00 01 eth0\n" +
			"fe800000000000000000000000000001 02 40 20 20 eth0\n" +
			"00000000000000000000000000000001 01 80 10 80 lo\n"),
		"sys/class/net/eth0": link("../../devices/pci0000:00/0000:00:1f.6/net/eth0"),
		"sys/devices/pci0000:00/0000:00:1f.6/net/eth0/type":    file("1\n"),
		"sys/devices/pci0000:00/0000:00:1f.6/net/eth0/address": file("AA:BB:CC:00:11:22\n"),
		"sys/devices/pci0000:00/0000:00:1f.6/net/eth0/device":  link("../../../0000:00:1f.6"),
		"sys/devices/pci0000:00/0000:00:1f.6/driver":           link("../../../bus/pci/drivers/e1000e"),
		"sys/class/net/can0":                       link("../../devices/platform/can/net/can0"),
		"sys/devices/platform/can/net/can0/type":   file("280\n"),
		"sys/devices/platform/can/net/can0/device": link("../../../can"),
		"sys/class/net/veth12ab":                   link("../../devices/virtual/net/veth12ab"),
		"sys/devices/virtual/net/veth12ab/type":    file("1\n"),

		"sys/firmware/efi/efivars/SecureBoot-8be4df61-93ca-11d2-aa0d-00e098032b8c": file("\x06\x00\x00\x00\x01"),
	}
	b, err := mapSource(m).Collect()
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if want := []Camera{{Device: "video0", Name: "C925e", USBPath: "1-1"}}; !slices.Equal(b.Cameras, want) {
		t.Errorf("cameras = %+v, want %+v", b.Cameras, want)
	}
	if got := usbPaths(b.USB); got != "usb1[1-1]" {
		t.Errorf("USB tree %s", got)
	}
	cam := b.USB[0].Children[0]
	if cam.PID != "085b" || cam.Product != "C925e" || cam.Driver != "snd-usb-audio,uvcvideo" ||
		cam.SerialHash != hashHex("ABC123") || cam.Class != "ef" || cam.SpeedMbps != 480 {
		t.Errorf("camera USB device = %+v", cam)
	}
	if b.USB[0].Manufacturer != "" || b.USB[0].SerialHash != "" {
		t.Errorf("root hub keeps kernel strings: %+v", b.USB[0])
	}

	if got := pciAddrs(b.PCI); got != "0000:00:01.0[0000:01:00.0[0000:01:00.1 0000:01:00.2]]" {
		t.Errorf("PCI tree %s", got)
	}
	nv := b.PCI[0].Children[0]
	if nv.Vendor != "NVIDIA Corporation" || nv.Device != "TU117 [GeForce GTX 1650]" || nv.Children[0].Device != "TU117 HD Audio" ||
		nv.Children[0].Driver != "snd_hda_intel" || nv.Children[1].Device != "" {
		t.Errorf("NVIDIA functions = %+v", nv)
	}
	if want := []GPU{{PCIAddress: "0000:01:00.0", Vendor: "NVIDIA Corporation", Model: "NVIDIA GeForce GTX 1650",
		Driver: "nvidia", DriverVersion: "570.86.15"}}; !slices.Equal(b.GPUs, want) {
		t.Errorf("GPUs = %+v, want %+v", b.GPUs, want)
	}

	if len(b.Disks) != 2 {
		t.Fatalf("disks = %+v", b.Disks)
	}
	emmc, sd := b.Disks[0], b.Disks[1]
	if emmc.Type != DiskEMMC || emmc.Model != "DG4032" || emmc.SerialHash != hashHex("0x12345678") || emmc.SizeBytes != 61071360*512 {
		t.Errorf("eMMC = %+v", emmc)
	}
	if p := emmc.Partitions; len(p) != 1 || !p[0].IsRoot || p[0].FSType != "ext4" ||
		!slices.Equal(p[0].Mountpoints, []string{"/", "/srv/my data"}) {
		t.Errorf("eMMC partitions = %+v", p)
	}
	if sd.Type != DiskSD || len(sd.Partitions) != 0 {
		t.Errorf("SD = %+v", sd)
	}

	if len(b.NICs) != 2 {
		t.Fatalf("NICs = %+v", b.NICs)
	}
	if eth := b.NICs[1]; eth.Name != "eth0" || eth.Driver != "e1000e" || eth.MAC != "aa:bb:cc:00:11:22" ||
		!slices.Equal(eth.IPv6, []string{"2001:db8::1"}) || eth.Service {
		t.Errorf("eth0 = %+v", eth)
	}
	if can := b.NICs[0]; can.Type != NICCAN || can.MAC != "" {
		t.Errorf("can0 = %+v", can)
	}

	if !b.BIOS.UEFI || b.SecureBoot.State != SecureBootEnabled {
		t.Errorf("UEFI %v, Secure Boot %s", b.BIOS.UEFI, b.SecureBoot.State)
	}
}

func TestSecureBoot(t *testing.T) {
	t.Parallel()
	const v = "sys/firmware/efi/efivars/SecureBoot-8be4df61-93ca-11d2-aa0d-00e098032b8c"
	tests := []struct {
		name string
		fs   fstest.MapFS
		want string
	}{
		{"legacy BIOS", fstest.MapFS{}, SecureBootUnsupported},
		{"disabled", fstest.MapFS{v: file("\x06\x00\x00\x00\x00")}, SecureBootDisabled},
		{"enabled", fstest.MapFS{v: file("\x06\x00\x00\x00\x01")}, SecureBootEnabled},
		{"no variable", fstest.MapFS{"sys/firmware/efi/efivars/Boot0000-x": file("x")}, SecureBootUnsupported},
		{"efivarfs not mounted", fstest.MapFS{"sys/firmware/efi/runtime": file("")}, SecureBootUnknown},
		{"truncated", fstest.MapFS{v: file("\x06")}, SecureBootUnknown},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := reader{root: tc.fs}
			if got := r.secureBoot().State; got != tc.want {
				t.Errorf("state = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestVMDetection(t *testing.T) {
	t.Parallel()
	dmiFS := func(vendor, product string, extra fstest.MapFS) fstest.MapFS {
		m := fstest.MapFS{dmiDir + "sys_vendor": file(vendor + "\n"), dmiDir + "product_name": file(product + "\n")}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	flags := fstest.MapFS{"proc/cpuinfo": file("processor\t: 0\nflags\t\t: fpu hypervisor sse\n")}
	tests := []struct {
		name string
		fs   fstest.MapFS
		want VM
	}{
		{"bare metal", dmiFS("Dell Inc.", "OptiPlex 7090", nil), VM{}},
		{"VMware", dmiFS("VMware, Inc.", "VMware Virtual Platform", flags), VM{IsVM: true, Hypervisor: "VMware"}},
		{"Hyper-V", dmiFS("Microsoft Corporation", "Virtual Machine", flags), VM{IsVM: true, Hypervisor: "Hyper-V"}},
		{"VirtualBox", dmiFS("innotek GmbH", "VirtualBox", flags), VM{IsVM: true, Hypervisor: "VirtualBox"}},
		{"plain KVM", dmiFS("QEMU", "Standard PC (Q35 + ICH9, 2009)", flags), VM{IsVM: true, Hypervisor: "KVM"}},
		{"QEMU TCG", dmiFS("QEMU", "Standard PC (i440FX + PIIX, 1996)", nil), VM{IsVM: true, Hypervisor: "QEMU"}},
		{"Proxmox OVMF", dmiFS("QEMU", "Standard PC (Q35 + ICH9, 2009)", fstest.MapFS{
			dmiDir + "bios_vendor": file("Proxmox distribution of EDK II\n"), "proc/cpuinfo": flags["proc/cpuinfo"]}),
			VM{IsVM: true, Hypervisor: "KVM", Platform: "Proxmox"}},
		{"Proxmox by NIC MAC", dmiFS("QEMU", "Standard PC (i440FX + PIIX, 1996)", fstest.MapFS{
			"proc/cpuinfo":                flags["proc/cpuinfo"],
			"sys/class/net/ens18/address": file("BC:24:11:12:34:56\n"),
			"sys/class/net/ens18/device":  link("../../../virtio0"),
			"sys/class/net/vmbr9/address": file("bc:24:11:00:00:01\n"),
		}), VM{IsVM: true, Hypervisor: "KVM", Platform: "Proxmox"}},
		{"Xen", fstest.MapFS{"sys/hypervisor/type": file("xen\n")}, VM{IsVM: true, Hypervisor: "Xen"}},
		{"unknown hypervisor", flags, VM{IsVM: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := reader{root: tc.fs}
			if got := r.vm(nil); got != tc.want {
				t.Errorf("vm = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestMountsSandboxAndUsers: the agent reads mountinfo inside its own unit
// sandbox. Bind mounts of a directory (root field ≠ "/": ReadWritePaths,
// PrivateTmp /tmp and /var/tmp) are not where the partition is mounted and
// are skipped; a btrfs subvolume (root = the subvol= superoption) is a
// mount of the filesystem and is kept. User names become "*".
func TestMountsSandboxAndUsers(t *testing.T) {
	t.Parallel()
	part := func(disk, name, devno string, sectors int) fstest.MapFS {
		d := "sys/block/" + disk + "/" + name
		return fstest.MapFS{d + "/partition": file("1\n"), d + "/size": file(fmt.Sprint(sectors) + "\n"), d + "/dev": file(devno + "\n")}
	}
	m := fstest.MapFS{
		"sys/block/sda/size": file("2000000\n"),
		"sys/block/sdb/size": file("1000000\n"),
		"proc/self/mountinfo": file(strings.Join([]string{
			"612 535 8:2 / / ro,relatime master:1 - ext4 /dev/sda2 rw",
			"619 612 8:2 /tmp/systemd-private-abc-stvor-agent.service-Jk3/tmp /tmp rw,relatime - ext4 /dev/sda2 rw",
			"620 612 8:2 /var/tmp/systemd-private-abc-stvor-agent.service-Qw8/tmp /var/tmp rw - ext4 /dev/sda2 rw",
			"621 612 8:2 /var/lib/stvor-agent /var/lib/stvor-agent rw - ext4 /dev/sda2 rw",
			"622 612 8:3 /@home /home ro,relatime - btrfs /dev/sda3 rw,ssd,subvolid=257,subvol=/@home",
			"623 612 8:3 /@home/alice/vm /var/lib/libvirt rw - btrfs /dev/sda3 rw,ssd,subvolid=257,subvol=/@home",
			"624 612 8:3 /@snapshots /home/alice/.snapshots ro - btrfs /dev/sda3 rw,subvolid=260,subvol=/@snapshots",
			"625 612 8:17 / /media/alice/CAMERA ro,nosuid - exfat /dev/sdb1 rw",
			"626 612 8:17 / /run/media/bob/CAMERA ro,nosuid - exfat /dev/sdb1 rw",
		}, "\n") + "\n"),
	}
	for _, f := range []fstest.MapFS{part("sda", "sda2", "8:2", 1000000), part("sda", "sda3", "8:3", 900000), part("sdb", "sdb1", "8:17", 900000)} {
		for k, v := range f {
			m[k] = v
		}
	}
	b, err := mapSource(m).Collect()
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	got := map[string][]string{}
	for _, d := range b.Disks {
		for _, p := range d.Partitions {
			got[p.Name] = p.Mountpoints
		}
	}
	want := map[string][]string{
		"sda2": {"/"},
		"sda3": {"/home", "/home/*/.snapshots"},
		"sdb1": {"/media/*/CAMERA", "/run/media/*/CAMERA"},
	}
	for name, mps := range want {
		if !slices.Equal(got[name], mps) {
			t.Errorf("%s mount points = %v, want %v", name, got[name], mps)
		}
	}
}

func TestMaskUserMount(t *testing.T) {
	t.Parallel()
	tests := []struct{ in, want string }{
		{"/home/alice", "/home/*"},
		{"/home/alice/x", "/home/*/x"},
		{"/media/alice/CAM", "/media/*/CAM"},
		{"/media/usb0", "/media/usb0"},
		{"/run/media/bob/X", "/run/media/*/X"},
		{"/srv/home/alice", "/srv/home/alice"},
	}
	for _, tc := range tests {
		if got := maskUserMount(tc.in); got != tc.want {
			t.Errorf("maskUserMount(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestMemoryInstalled: with memory hotplug sysfs the installed RAM is the
// number of memory blocks × the block size, which a balloon driver does not
// move; without it — MemTotal.
func TestMemoryInstalled(t *testing.T) {
	t.Parallel()
	meminfo := file("MemTotal:        3874800 kB\nSwapTotal:       1048572 kB\n")
	m := fstest.MapFS{
		"proc/meminfo": meminfo,
		"sys/devices/system/memory/block_size_bytes":   file("8000000\n"),
		"sys/devices/system/memory/auto_online_blocks": file("online\n"),
		"sys/devices/system/memory/power/control":      file("auto\n"),
	}
	for i := range 32 {
		m[fmt.Sprintf("sys/devices/system/memory/memory%d/online", i)] = file("1\n")
	}
	b, err := mapSource(m).Collect()
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(32 * 0x8000000); b.Memory.TotalBytes != want || b.Memory.SwapBytes != 1048572*1024 {
		t.Errorf("memory = %+v, want total %d", b.Memory, want)
	}
	// Broken block size: back to MemTotal.
	m["sys/devices/system/memory/block_size_bytes"] = file("zz\n")
	b, _ = mapSource(m).Collect()
	if b.Memory.TotalBytes != 3874800*1024 {
		t.Errorf("bad block size: total %d, want MemTotal", b.Memory.TotalBytes)
	}
	b, _ = mapSource(fstest.MapFS{"proc/meminfo": meminfo}).Collect()
	if b.Memory.TotalBytes != 3874800*1024 {
		t.Errorf("no memory sysfs: total %d, want MemTotal", b.Memory.TotalBytes)
	}
}

// TestComposeBridgesNotInventoried: Docker Compose creates br-<12 hex>
// bridges per project network; they come and go with stacks.
func TestComposeBridgesNotInventoried(t *testing.T) {
	t.Parallel()
	m := fstest.MapFS{}
	for _, n := range []string{"br-5d40d7a97867", "br-lan", "docker0"} {
		m["sys/class/net/"+n] = link("../../devices/virtual/net/" + n)
		m["sys/devices/virtual/net/"+n+"/type"] = file("1\n")
	}
	b, err := mapSource(m).Collect()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, n := range b.NICs {
		names = append(names, n.Name)
	}
	if want := []string{"br-lan", "docker0"}; !slices.Equal(names, want) {
		t.Errorf("NICs = %v, want %v", names, want)
	}
}
