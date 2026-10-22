package inventory

import (
	"bufio"
	"bytes"
	"errors"
	"io/fs"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const dmiDir = "sys/class/dmi/id/"

// secureBootVar is the EFI global variable SecureBoot in efivarfs: 4 bytes
// of attributes, then 1 — enabled, 0 — disabled.
const secureBootVar = "sys/firmware/efi/efivars/SecureBoot-8be4df61-93ca-11d2-aa0d-00e098032b8c"

func (r *reader) board() Board {
	b := Board{
		Vendor:      dmi(r.line(dmiDir + "sys_vendor")),
		Product:     dmi(r.line(dmiDir + "product_name")),
		Version:     dmi(r.line(dmiDir + "product_version")),
		BoardVendor: dmi(r.line(dmiDir + "board_vendor")),
		BoardName:   dmi(r.line(dmiDir + "board_name")),
	}
	// DMI serials are root-only on most kernels; a readable one is hashed.
	for _, f := range []string{"product_serial", "board_serial"} {
		if h := serialHash(r.line(dmiDir + f)); h != "" {
			b.SerialHash = h
			break
		}
	}
	b.DTModel = r.line("proc/device-tree/model")
	if data, ok := r.file("proc/device-tree/compatible"); ok {
		for c := range strings.SplitSeq(string(data), "\x00") {
			if c = clean(c); c != "" && len(b.DTCompatible) < maxCompatible {
				b.DTCompatible = append(b.DTCompatible, c)
			}
		}
	}
	return b
}

// reProxmoxDrive is the drive id Proxmox VE gives guest disks
// ("drive-scsi0"), visible in the SCSI device identification page; libvirt
// names them "drive-scsi0-0-0-0".
var reProxmoxDrive = regexp.MustCompile(`drive-(scsi|sata|ide|virtio|efidisk|tpmstate)\d+$`)

// vm tells a VM from bare metal by DMI, /sys/hypervisor, the CPU flag
// "hypervisor" and the clock sources; Proxmox by the drive ids of its disks
// or the MAC prefix of its NICs.
func (r *reader) vm(disks []Disk) VM {
	vendor := r.line(dmiDir + "sys_vendor")
	product := r.line(dmiDir + "product_name")
	biosVendor := r.line(dmiDir + "bios_vendor")
	hypType := r.line("sys/hypervisor/type")
	clocks := r.line("sys/devices/system/clocksource/clocksource0/available_clocksource")
	flag := r.cpuFlag("hypervisor")
	kvmClock := strings.Contains(clocks, "kvm-clock")

	var v VM
	switch {
	case strings.EqualFold(hypType, "xen") || vendor == "Xen" || strings.Contains(clocks, "xen"):
		v = VM{IsVM: true, Hypervisor: "Xen"}
	case vendor == "QEMU" || strings.Contains(product, "KVM") || kvmClock:
		v = VM{IsVM: true, Hypervisor: "KVM"}
		if !kvmClock && !flag {
			v.Hypervisor = "QEMU"
		}
	case strings.HasPrefix(vendor, "VMware") || strings.HasPrefix(product, "VMware"):
		v = VM{IsVM: true, Hypervisor: "VMware"}
	case vendor == "Microsoft Corporation" && product == "Virtual Machine":
		v = VM{IsVM: true, Hypervisor: "Hyper-V"}
	case product == "VirtualBox" || vendor == "innotek GmbH":
		v = VM{IsVM: true, Hypervisor: "VirtualBox"}
	case strings.HasPrefix(vendor, "Parallels"):
		v = VM{IsVM: true, Hypervisor: "Parallels"}
	case vendor == "Amazon EC2":
		v = VM{IsVM: true, Hypervisor: "KVM", Platform: "Amazon EC2"}
	case vendor == "Google" && product == "Google Compute Engine":
		v = VM{IsVM: true, Hypervisor: "KVM", Platform: "Google Compute Engine"}
	case flag:
		v = VM{IsVM: true}
	default:
		return VM{}
	}
	if v.Platform == "" && (v.Hypervisor == "KVM" || v.Hypervisor == "QEMU") {
		switch {
		case strings.Contains(biosVendor, "Proxmox") || r.proxmoxDisks(disks) || r.proxmoxNICs():
			v.Platform = "Proxmox"
		case vendor == "OpenStack Foundation" || strings.HasPrefix(product, "OpenStack"):
			v.Platform = "OpenStack"
		}
	}
	return v
}

// proxmoxOUI is the MAC prefix Proxmox VE assigns to guest NICs.
const proxmoxOUI = "bc:24:11:"

func (r *reader) proxmoxNICs() bool {
	for _, e := range r.dir("sys/class/net") {
		if validName(e.Name()) && strings.HasPrefix(strings.ToLower(r.line("sys/class/net/"+e.Name()+"/address")), proxmoxOUI) &&
			r.link("sys/class/net/"+e.Name()+"/device") != "" {
			return true
		}
	}
	return false
}

func (r *reader) proxmoxDisks(disks []Disk) bool {
	for _, d := range disks {
		if data, ok := r.file("sys/block/" + d.Name + "/device/vpd_pg83"); ok && reProxmoxDrive.Match(bytes.TrimRight(data, "\x00\n")) {
			return true
		}
		if s := r.line("sys/block/" + d.Name + "/serial"); reProxmoxDrive.MatchString(s) {
			return true
		}
	}
	return false
}

// cpuFlag reports whether the first CPU's "flags" line has flag.
func (r *reader) cpuFlag(flag string) bool {
	data, ok := r.file("proc/cpuinfo")
	if !ok {
		return false
	}
	for line := range bytes.Lines(data) {
		k, v, found := bytes.Cut(line, []byte(":"))
		if found && string(bytes.TrimSpace(k)) == "flags" {
			for f := range bytes.FieldsSeq(v) {
				if string(f) == flag {
					return true
				}
			}
			return false
		}
	}
	return false
}

// reL4T is the first line of /etc/nv_tegra_release:
// "# R32 (release), REVISION: 6.1, GCID: …".
var reL4T = regexp.MustCompile(`^# R(\d+) \(release\), REVISION: ([0-9.]+)`)

func (r *reader) os(kernel string) OS {
	o := OS{ID: "linux", Kernel: clean(kernel)}
	data, ok := r.file("etc/os-release")
	if !ok {
		data, ok = r.file("usr/lib/os-release")
	}
	if ok {
		kv := parseOSRelease(data)
		if id := clean(kv["ID"]); id != "" {
			o.ID = id
		}
		o.VersionID = clean(kv["VERSION_ID"])
		o.PrettyName = clean(kv["PRETTY_NAME"])
	}
	if o.Kernel == "" {
		o.Kernel = "unknown"
	}
	if data, ok := r.file("etc/nv_tegra_release"); ok {
		first, _, _ := bytes.Cut(data, []byte("\n"))
		if m := reL4T.FindSubmatch(first); m != nil {
			o.L4T = "R" + string(m[1]) + "." + string(m[2])
		}
	}
	return o
}

// parseOSRelease reads KEY=VALUE lines with shell-like quoting.
func parseOSRelease(data []byte) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			q := v[0]
			v = v[1 : len(v)-1]
			if q == '"' {
				v = strings.NewReplacer(`\"`, `"`, `\\`, `\`, `\$`, `$`, "\\`", "`").Replace(v)
			}
		}
		out[k] = v
	}
	return out
}

func (r *reader) cpu() CPU {
	var c CPU
	data, ok := r.file("proc/cpuinfo")
	if !ok {
		c.Model = "unknown"
		return c
	}
	type core struct{ pkg, id string }
	cores := map[core]bool{}
	pkgs := map[string]bool{}
	var vendorID, modelName, hardware string
	var implementers, parts []string
	var pkg string
	for line := range bytes.Lines(data) {
		k, v, found := bytes.Cut(line, []byte(":"))
		if !found {
			continue
		}
		key, val := string(bytes.TrimSpace(k)), string(bytes.TrimSpace(v))
		switch key {
		case "processor":
			if _, err := strconv.Atoi(val); err == nil {
				c.Threads++
			}
		case "vendor_id":
			vendorID = val
		case "model name":
			if modelName == "" {
				modelName = val
			}
		case "Hardware":
			hardware = val
		case "physical id":
			pkg = val
			pkgs[val] = true
		case "core id":
			cores[core{pkg, val}] = true
		case "CPU implementer":
			implementers = appendNew(implementers, strings.ToLower(val))
		case "CPU part":
			if len(implementers) > 0 {
				parts = appendNew(parts, implementers[len(implementers)-1]+"/"+strings.ToLower(val))
			}
		}
	}
	if present := r.line("sys/devices/system/cpu/present"); present != "" {
		if n := countCPUList(present); n > c.Threads {
			c.Threads = n // offline cores (Jetson power modes) are still installed
		}
	}
	switch vendorID {
	case "GenuineIntel":
		c.Vendor = "Intel"
	case "AuthenticAMD":
		c.Vendor = "AMD"
	case "":
	default:
		c.Vendor = clean(vendorID)
	}
	if len(pkgs) > 0 {
		c.Sockets = len(pkgs)
	}
	if len(cores) > 0 {
		c.Cores = len(cores)
	}
	if len(implementers) > 0 {
		c.Vendor = armImplementer(implementers[0])
		var names []string
		for _, p := range parts {
			impl, part, _ := strings.Cut(p, "/")
			if n := armPart(impl, part); n != "" {
				names = appendNew(names, n)
			}
		}
		if len(names) > 0 {
			modelName = strings.Join(names, " + ")
		}
		c.Cores = c.Threads // no SMT on the ARM cores robots use
	}
	c.Model = clean(modelName)
	if c.Model == "" {
		c.Model = clean(hardware)
	}
	if c.Model == "" {
		c.Model = "unknown"
	}
	return c
}

func appendNew(s []string, v string) []string {
	if slices.Contains(s, v) {
		return s
	}
	return append(s, v)
}

// countCPUList counts the CPUs of a kernel CPU list "0-3,6,8-9".
func countCPUList(s string) int {
	n := 0
	for part := range strings.SplitSeq(s, ",") {
		lo, hi, isRange := strings.Cut(part, "-")
		a, err := strconv.Atoi(lo)
		if err != nil {
			return 0
		}
		b := a
		if isRange {
			if b, err = strconv.Atoi(hi); err != nil || b < a {
				return 0
			}
		}
		n += b - a + 1
	}
	return n
}

func armImplementer(impl string) string {
	switch impl {
	case "0x41":
		return "ARM"
	case "0x42":
		return "Broadcom"
	case "0x43":
		return "Cavium"
	case "0x48":
		return "HiSilicon"
	case "0x4e":
		return "NVIDIA"
	case "0x51":
		return "Qualcomm"
	case "0x61":
		return "Apple"
	case "0xc0":
		return "Ampere"
	}
	return ""
}

// armPart names the cores robots use; others keep the kernel's model name.
func armPart(impl, part string) string {
	if impl == "0x4e" && part == "0x004" {
		return "Carmel"
	}
	if impl != "0x41" {
		return ""
	}
	switch part {
	case "0xd03":
		return "Cortex-A53"
	case "0xd04":
		return "Cortex-A35"
	case "0xd05":
		return "Cortex-A55"
	case "0xd07":
		return "Cortex-A57"
	case "0xd08":
		return "Cortex-A72"
	case "0xd09":
		return "Cortex-A73"
	case "0xd0a":
		return "Cortex-A75"
	case "0xd0b":
		return "Cortex-A76"
	case "0xd0c":
		return "Neoverse-N1"
	case "0xd0d":
		return "Cortex-A77"
	case "0xd41":
		return "Cortex-A78"
	case "0xd42":
		return "Cortex-A78AE"
	case "0xd4b":
		return "Cortex-A78C"
	case "0xd46":
		return "Cortex-A510"
	case "0xd47":
		return "Cortex-A710"
	case "0xd4d":
		return "Cortex-A715"
	case "0xd80":
		return "Cortex-A520"
	case "0xd81":
		return "Cortex-A720"
	}
	return ""
}

func (r *reader) memory() Memory {
	var m Memory
	data, ok := r.file("proc/meminfo")
	if !ok {
		return m
	}
	for line := range bytes.Lines(data) {
		k, v, found := bytes.Cut(line, []byte(":"))
		if !found {
			continue
		}
		f := bytes.Fields(v)
		if len(f) == 0 {
			continue
		}
		n, err := strconv.ParseInt(string(f[0]), 10, 64)
		if err != nil || n < 0 {
			continue
		}
		switch string(k) {
		case "MemTotal":
			m.TotalBytes = n * 1024
		case "SwapTotal":
			m.SwapBytes = n * 1024
		}
	}
	if total, ok := r.memoryBlocks(); ok {
		m.TotalBytes = total
	}
	return m
}

// memoryBlocks is the installed RAM from memory hotplug sysfs: the number of
// memoryN blocks × block_size_bytes (hex). MemTotal drifts with a balloon
// driver in a VM and with kernel reservations, which would change the
// inventory hash; the block count does not. Kernels without
// CONFIG_MEMORY_HOTPLUG (L4T R32) have no such directory — MemTotal then.
func (r *reader) memoryBlocks() (int64, bool) {
	const dir = "sys/devices/system/memory"
	bs, err := strconv.ParseInt(strings.TrimPrefix(r.line(dir+"/block_size_bytes"), "0x"), 16, 64)
	if err != nil || bs <= 0 {
		return 0, false
	}
	var n int64
	for _, e := range r.dir(dir) {
		if num, ok := strings.CutPrefix(e.Name(), "memory"); ok && num != "" && allDigits(num) {
			n++
		}
	}
	if n == 0 || n > math.MaxInt64/bs {
		return 0, false
	}
	return n * bs, true
}

func allDigits(s string) bool {
	for i := range len(s) {
		if !isDigit(s[i]) {
			return false
		}
	}
	return true
}

// reDMIDate is the SMBIOS date MM/DD/YYYY.
var reDMIDate = regexp.MustCompile(`^(\d{2})/(\d{2})/(\d{4})$`)

func (r *reader) bios() BIOS {
	b := BIOS{
		Vendor:  dmi(r.line(dmiDir + "bios_vendor")),
		Version: dmi(r.line(dmiDir + "bios_version")),
		Date:    dmi(r.line(dmiDir + "bios_date")),
		UEFI:    r.exists("sys/firmware/efi"),
	}
	if m := reDMIDate.FindStringSubmatch(b.Date); m != nil {
		b.Date = m[3] + "-" + m[1] + "-" + m[2]
	}
	return b
}

func (r *reader) secureBoot() SecureBoot {
	if !r.exists("sys/firmware/efi") {
		return SecureBoot{State: SecureBootUnsupported}
	}
	data, err := fs.ReadFile(r.root, secureBootVar)
	switch {
	case err == nil && len(data) >= 5 && data[4] == 1:
		return SecureBoot{State: SecureBootEnabled}
	case err == nil && len(data) >= 5 && data[4] == 0:
		return SecureBoot{State: SecureBootDisabled}
	case err != nil && !r.exists("sys/firmware/efi/efivars"):
		return SecureBoot{State: SecureBootUnknown} // efivarfs not mounted
	case errors.Is(err, fs.ErrNotExist):
		return SecureBoot{State: SecureBootUnsupported}
	}
	return SecureBoot{State: SecureBootUnknown}
}
