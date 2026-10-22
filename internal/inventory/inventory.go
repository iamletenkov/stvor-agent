// Package inventory describes the machine's hardware and software for the
// portal (spec "robot telemetry" §4, Stvor plan Task 9): board and firmware,
// VM or bare metal, OS, CPU, memory, disks with partitions, network
// interfaces, USB and PCI trees, GPUs and cameras.
//
// Everything is read without root from Source.Root (/proc, /sys, /etc), so
// tests run on fixture trees. Serial numbers never leave the machine in clear
// text: a serial the agent can read becomes its sha256, one it cannot read
// (DMI serials are root-only) is left out. Strings from the hardware are
// untrusted: control characters are dropped and each is cut to
// inventory_string_max of the contract.
//
// The agent sends the body only when Hash changes: the hash covers the
// canonical JSON of the body without collected_at, so the same hardware gives
// the same hash on every check.
package inventory

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/netip"
	"os"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"
)

// Limits of the portal contract v1 (inventory section and limits).
const (
	maxString     = 256 // inventory_string_max
	maxBodyBytes  = 256 << 10
	maxDisks      = 64
	maxPartitions = 128
	maxNICs       = 64
	maxUSB        = 256
	maxPCI        = 256
	maxGPUs       = 16
	maxCameras    = 32
	// Bounds of our own for lists the contract leaves open.
	maxMountpoints = 16
	maxAddrs       = 16
	maxCompatible  = 16
)

// Secure Boot states of the contract.
const (
	SecureBootEnabled     = "enabled"
	SecureBootDisabled    = "disabled"
	SecureBootUnsupported = "unsupported"
	SecureBootUnknown     = "unknown"
)

// ErrTooLarge is returned by Encode when the body exceeds
// inventory_body_max_bytes of the contract.
var ErrTooLarge = errors.New("тело инвентаря больше 256 КиБ")

// Body is the inventory body of the contract v1. Optional fields are left
// out when unknown.
type Body struct {
	CollectedAt string     `json:"collected_at,omitempty"`
	System      System     `json:"system"`
	Board       Board      `json:"board"`
	VM          VM         `json:"vm"`
	OS          OS         `json:"os"`
	CPU         CPU        `json:"cpu"`
	Memory      Memory     `json:"memory"`
	BIOS        BIOS       `json:"bios"`
	SecureBoot  SecureBoot `json:"secure_boot"`
	Disks       []Disk     `json:"disks"`
	NICs        []NIC      `json:"nics"`
	USB         []USB      `json:"usb"`
	PCI         []PCI      `json:"pci"`
	GPUs        []GPU      `json:"gpus"`
	Cameras     []Camera   `json:"cameras"`
}

// System is the architecture and the hashed hostname.
type System struct {
	Arch         string `json:"arch"`
	HostnameHash string `json:"hostname_hash,omitempty"`
}

// Board is DMI (x86) or the device tree (ARM boards such as Jetson).
type Board struct {
	Vendor       string   `json:"vendor,omitempty"`
	Product      string   `json:"product,omitempty"`
	Version      string   `json:"version,omitempty"`
	BoardVendor  string   `json:"board_vendor,omitempty"`
	BoardName    string   `json:"board_name,omitempty"`
	SerialHash   string   `json:"serial_hash,omitempty"`
	DTModel      string   `json:"dt_model,omitempty"`
	DTCompatible []string `json:"dt_compatible,omitempty"`
}

// VM tells a virtual machine from bare metal.
type VM struct {
	IsVM       bool   `json:"is_vm"`
	Hypervisor string `json:"hypervisor,omitempty"`
	Platform   string `json:"platform,omitempty"`
}

// OS is /etc/os-release, the kernel release and the Jetson L4T release.
type OS struct {
	ID         string `json:"id"`
	VersionID  string `json:"version_id,omitempty"`
	PrettyName string `json:"pretty_name,omitempty"`
	Kernel     string `json:"kernel"`
	L4T        string `json:"l4t,omitempty"`
}

// CPU is the processor model and its topology.
type CPU struct {
	Vendor  string `json:"vendor,omitempty"`
	Model   string `json:"model"`
	Cores   int    `json:"cores,omitempty"`
	Threads int    `json:"threads"`
	Sockets int    `json:"sockets,omitempty"`
}

// Memory is the installed RAM and swap as the kernel sees them.
type Memory struct {
	TotalBytes int64 `json:"total_bytes"`
	SwapBytes  int64 `json:"swap_bytes,omitempty"`
}

// BIOS is the firmware; UEFI tells UEFI boot from legacy BIOS.
type BIOS struct {
	Vendor  string `json:"vendor,omitempty"`
	Version string `json:"version,omitempty"`
	Date    string `json:"date,omitempty"`
	UEFI    bool   `json:"uefi"`
}

// SecureBoot is the UEFI Secure Boot state.
type SecureBoot struct {
	State string `json:"state"`
}

// Disk is a whole block device with its partitions.
type Disk struct {
	Name       string      `json:"name"`
	Model      string      `json:"model,omitempty"`
	Type       string      `json:"type"`
	SizeBytes  int64       `json:"size_bytes"`
	Removable  bool        `json:"removable"`
	SerialHash string      `json:"serial_hash,omitempty"`
	Partitions []Partition `json:"partitions"`
}

// Partition is a partition with where it is mounted; IsRoot marks «/»
// («системный»), IsEFI the EFI system partition («загрузочный»).
type Partition struct {
	Name        string   `json:"name"`
	SizeBytes   int64    `json:"size_bytes"`
	FSType      string   `json:"fstype,omitempty"`
	Mountpoints []string `json:"mountpoints"`
	IsRoot      bool     `json:"is_root"`
	IsEFI       bool     `json:"is_efi"`
}

// NIC is a network interface; Service marks virtual ones (bridges, docker0,
// lo), shown dimmed on the portal.
type NIC struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Driver  string   `json:"driver,omitempty"`
	MAC     string   `json:"mac,omitempty"`
	IPv4    []string `json:"ipv4"`
	IPv6    []string `json:"ipv6"`
	Service bool     `json:"service"`
}

// USB is a USB device (a root hub at the top) with the devices behind it.
type USB struct {
	Path         string  `json:"path"`
	VID          string  `json:"vid"`
	PID          string  `json:"pid"`
	Manufacturer string  `json:"manufacturer,omitempty"`
	Product      string  `json:"product,omitempty"`
	SpeedMbps    float64 `json:"speed_mbps,omitempty"`
	Class        string  `json:"class,omitempty"`
	SerialHash   string  `json:"serial_hash,omitempty"`
	Driver       string  `json:"driver,omitempty"`
	Children     []USB   `json:"children"`
}

// PCI is a PCI function; bridges hold the devices behind them, function 0
// of a multi-function device holds its other functions.
type PCI struct {
	Address  string `json:"address"`
	VendorID string `json:"vendor_id"`
	DeviceID string `json:"device_id"`
	Class    string `json:"class"`
	Vendor   string `json:"vendor,omitempty"`
	Device   string `json:"device,omitempty"`
	Driver   string `json:"driver,omitempty"`
	Children []PCI  `json:"children"`
}

// GPU is a display controller (PCI class 0x03) or a Jetson integrated GPU.
type GPU struct {
	PCIAddress    string `json:"pci_address,omitempty"`
	Vendor        string `json:"vendor,omitempty"`
	Model         string `json:"model"`
	Driver        string `json:"driver,omitempty"`
	DriverVersion string `json:"driver_version,omitempty"`
}

// Camera is a V4L2 capture node.
type Camera struct {
	Device  string `json:"device"`
	Name    string `json:"name"`
	USBPath string `json:"usb_path,omitempty"`
}

// Uname is the part of uname(2) the inventory needs.
type Uname struct {
	Release, Machine string
}

// Source reads the inventory. Root is the filesystem root ("proc/cpuinfo",
// "sys/…"); it must support fs.ReadLink (os.DirFS does) for drivers and the
// USB and PCI topology. A nil hook means the source is absent.
type Source struct {
	Root     fs.FS
	Uname    func() (Uname, error)
	Hostname func() (string, error)
	// IPv4 lists the IPv4 addresses of each interface by name. IPv6 comes
	// from /proc/net/if_inet6, which tells temporary addresses apart.
	IPv4 func() (map[string][]netip.Addr, error)
	Now  func() time.Time
	// Names caches PCI vendor and device names from pci.ids between
	// collections; nil — the file is read on every collection.
	Names *PCINames
}

// New wires the real machine.
func New() Source {
	return Source{
		Root:     os.DirFS("/"),
		Uname:    SystemUname,
		Hostname: os.Hostname,
		IPv4:     SystemIPv4,
		Now:      time.Now,
		Names:    &PCINames{},
	}
}

// Collect reads the inventory. A missing source leaves its part empty; read
// errors of present sources are joined, the rest is still returned.
func (s Source) Collect() (Body, error) {
	r := reader{root: s.Root}
	var b Body
	if s.Now != nil {
		b.CollectedAt = s.Now().UTC().Format(time.RFC3339)
	}
	var un Uname
	if s.Uname != nil {
		u, err := s.Uname()
		if err != nil {
			r.fail(fmt.Errorf("uname: %w", err))
		}
		un = u
	}
	b.System.Arch = goArch(un.Machine)
	if s.Hostname != nil {
		if h, err := s.Hostname(); err == nil && h != "" {
			b.System.HostnameHash = hashHex(h)
		}
	}
	b.Board = r.board()
	b.OS = r.os(un.Release)
	b.CPU = r.cpu()
	b.Memory = r.memory()
	b.BIOS = r.bios()
	b.SecureBoot = r.secureBoot()
	b.Disks = r.disks()
	b.VM = r.vm(b.Disks)
	var v4 map[string][]netip.Addr
	if s.IPv4 != nil {
		m, err := s.IPv4()
		if err != nil {
			r.fail(fmt.Errorf("адреса IPv4: %w", err))
		}
		v4 = m
	}
	b.NICs = r.nics(v4)
	b.USB = r.usb()
	devs := r.pciDevices()
	r.pciNames(devs, s.Names)
	b.PCI = pciTree(devs)
	b.GPUs = r.gpus(devs, b.OS.L4T)
	b.Cameras = r.cameras()
	Normalize(&b)
	return b, errors.Join(r.errs...)
}

// Normalize puts every list in its canonical order, so the hash does not
// depend on the order the kernel lists devices in.
func Normalize(b *Body) {
	b.Disks, b.NICs, b.USB = orEmpty(b.Disks), orEmpty(b.NICs), orEmpty(b.USB)
	b.PCI, b.GPUs, b.Cameras = orEmpty(b.PCI), orEmpty(b.GPUs), orEmpty(b.Cameras)
	sortBy(b.Disks, func(d Disk) string { return d.Name })
	for i := range b.Disks {
		d := &b.Disks[i]
		d.Partitions = orEmpty(d.Partitions)
		for j := range d.Partitions {
			d.Partitions[j].Mountpoints = orEmpty(d.Partitions[j].Mountpoints)
		}
		sortBy(d.Partitions, func(p Partition) string { return p.Name })
		for j := range d.Partitions {
			sortStrings(d.Partitions[j].Mountpoints)
		}
	}
	sortNICs(b.NICs)
	for i := range b.NICs {
		b.NICs[i].IPv4, b.NICs[i].IPv6 = orEmpty(b.NICs[i].IPv4), orEmpty(b.NICs[i].IPv6)
		sortStrings(b.NICs[i].IPv4)
		sortStrings(b.NICs[i].IPv6)
	}
	sortUSB(b.USB)
	sortPCI(b.PCI)
	sortBy(b.GPUs, func(g GPU) string { return g.PCIAddress + "\x00" + g.Model })
	sortBy(b.Cameras, func(c Camera) string { return c.Device })
}

func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// Hash is the sha256 of the canonical JSON of the body without the keys
// collected_at and nics[].ipv4, nics[].ipv6 (contract v1 inventory.flow, the
// rule the portal checks): unchanged hardware keeps its hash between checks,
// and a link that loses and regains its DHCP lease does not make a new
// inventory version. The addresses still go in the body (Encode); the
// interface name, type, driver and MAC stay in the hash.
func Hash(b Body) (string, error) {
	b.CollectedAt = ""
	var nics []hashedNIC // nil stays nil: Encode gives "nics":null, so does the portal
	if b.NICs != nil {
		nics = make([]hashedNIC, len(b.NICs))
	}
	for i, n := range b.NICs {
		nics[i] = hashedNIC{Name: n.Name, Type: n.Type, Driver: n.Driver, MAC: n.MAC, Service: n.Service}
	}
	data, err := canonical(hashedBody{Body: b, NICs: nics})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// hashedBody is the body as Hash sees it: nics without their addresses (the
// outer NICs field shadows the embedded one in encoding/json).
type hashedBody struct {
	Body
	NICs []hashedNIC `json:"nics"`
}

// hashedNIC is NIC without ipv4 and ipv6.
type hashedNIC struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Driver  string `json:"driver,omitempty"`
	MAC     string `json:"mac,omitempty"`
	Service bool   `json:"service"`
}

// Encode is the canonical JSON of the body for the request; ErrTooLarge
// when it exceeds the contract's limit.
func Encode(b Body) ([]byte, error) {
	data, err := canonical(b)
	if err != nil {
		return nil, err
	}
	if len(data) > maxBodyBytes {
		return nil, fmt.Errorf("%w: %d байт", ErrTooLarge, len(data))
	}
	return data, nil
}

// canonical is JSON with object keys in alphabetical order, no whitespace,
// no HTML escaping, arrays in the order Normalize gave them.
func canonical(v any) ([]byte, error) {
	first, err := marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(first))
	dec.UseNumber()
	var generic any
	if err := dec.Decode(&generic); err != nil {
		return nil, fmt.Errorf("канонический JSON: %w", err)
	}
	out, err := marshal(generic) // maps are encoded with sorted keys
	if err != nil {
		return nil, err
	}
	return rawLineSeparators(out), nil
}

// rawLineSeparators turns the escapes \u2028 and \u2029 that encoding/json
// always writes back into the raw characters: the portal hashes the body with
// json.dumps(ensure_ascii=False), which writes them as they are. Outside
// strings the encoder never writes a backslash; an escaped backslash is
// skipped as a pair, so a literal "\\u2028" text is kept.
func rawLineSeparators(data []byte) []byte {
	if !bytes.Contains(data, []byte(`\u202`)) {
		return data
	}
	out := make([]byte, 0, len(data))
	for i := 0; i < len(data); i++ {
		if data[i] != '\\' || i+1 >= len(data) {
			out = append(out, data[i])
			continue
		}
		if esc := data[i:min(i+6, len(data))]; string(esc) == `\u2028` || string(esc) == `\u2029` {
			out = append(out, string(rune(0x2028+int(esc[5]-'8')))...)
			i += 5
			continue
		}
		out = append(out, data[i], data[i+1])
		i++
	}
	return out
}

func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("JSON инвентаря: %w", err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// SystemUname is uname(2).
func SystemUname() (Uname, error) {
	var u syscall.Utsname
	if err := syscall.Uname(&u); err != nil {
		return Uname{}, err
	}
	return Uname{Release: utsString(u.Release[:]), Machine: utsString(u.Machine[:])}, nil
}

// utsString converts a NUL-terminated utsname field; its element type is
// int8 or uint8 depending on the architecture.
func utsString[T int8 | uint8](a []T) string {
	b := make([]byte, 0, len(a))
	for _, c := range a {
		if c == 0 {
			break
		}
		b = append(b, byte(c))
	}
	return string(b)
}

// SystemIPv4 lists the IPv4 addresses of the interfaces.
func SystemIPv4() (map[string][]netip.Addr, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	out := make(map[string][]netip.Addr, len(ifs))
	for _, ifc := range ifs {
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			pfx, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip, ok := netip.AddrFromSlice(pfx.IP)
			if ok && ip.Unmap().Is4() {
				out[ifc.Name] = append(out[ifc.Name], ip.Unmap())
			}
		}
	}
	return out, nil
}

// goArch maps uname's machine to Go's architecture names.
func goArch(machine string) string {
	switch machine {
	case "x86_64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	case "i386", "i486", "i586", "i686":
		return "386"
	case "armv6l", "armv7l", "armv8l":
		return "arm"
	case "":
		return "unknown"
	}
	return clean(machine)
}

func hashHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// serialHash is the sha256 of a serial number; "" for placeholders.
func serialHash(s string) string {
	s = clean(s)
	if s == "" || isPlaceholder(s) {
		return ""
	}
	return hashHex(s)
}

// clean makes an untrusted string safe to show: invalid UTF-8 and control
// characters are dropped, spaces trimmed, at most maxString characters.
func clean(s string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "")
	}
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == utf8.RuneError {
			if r == '\t' || r == '\n' {
				return ' '
			}
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > maxString {
		s = string([]rune(s)[:maxString])
		s = strings.TrimSpace(s)
	}
	return s
}

// isPlaceholder recognises the strings firmware vendors leave in unfilled
// DMI fields.
func isPlaceholder(s string) bool {
	switch strings.ToLower(s) {
	case "", "default string", "to be filled by o.e.m.", "to be filled by oem", "o.e.m.", "oem",
		"system product name", "system manufacturer", "system version", "system serial number",
		"not specified", "not applicable", "none", "n/a", "unknown", "0", "0123456789",
		"base board product name", "base board version", "default", "x.x", "type1productconfigid":
		return true
	}
	return false
}

// dmi is a cleaned DMI string, "" for placeholders.
func dmi(s string) string {
	s = clean(s)
	if isPlaceholder(s) {
		return ""
	}
	return s
}
