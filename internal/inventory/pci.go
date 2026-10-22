package inventory

import (
	"bufio"
	"bytes"
	"io/fs"
	"regexp"
	"strings"
)

// rePCIAddr is a PCI function address domain:bus:device.function.
var rePCIAddr = regexp.MustCompile(`^[0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-7]$`)

const pciDir = "sys/bus/pci/devices/"

// pciDev is a PCI function with the upstream bridge from the sysfs topology.
type pciDev struct {
	PCI
	bridge string // address of the upstream bridge, "" under the root complex
}

// pciDevices reads the PCI functions sorted by address, at most maxPCI.
func (r *reader) pciDevices() []pciDev {
	var out []pciDev
	for _, e := range r.dir(strings.TrimSuffix(pciDir, "/")) {
		addr := e.Name()
		if !rePCIAddr.MatchString(addr) {
			continue
		}
		dir := pciDir + addr
		d := pciDev{PCI: PCI{
			Address:  addr,
			VendorID: hexID(r.line(dir+"/vendor"), 4),
			DeviceID: hexID(r.line(dir+"/device"), 4),
			Class:    hexID(r.line(dir+"/class"), 6),
			Driver:   r.linkBase(dir + "/driver"),
			Children: []PCI{},
		}}
		if d.VendorID == "" || d.DeviceID == "" || d.Class == "" {
			continue
		}
		// ../../../devices/pci0000:00/0000:00:1c.4/0000:07:00.0: the
		// element before the device itself is its bridge, if it is one.
		parts := strings.Split(r.link(dir), "/")
		if n := len(parts); n >= 2 && rePCIAddr.MatchString(parts[n-2]) {
			d.bridge = parts[n-2]
		}
		out = append(out, d)
	}
	sortBy(out, func(d pciDev) string { return d.Address })
	if len(out) > maxPCI {
		out = out[:maxPCI]
	}
	return out
}

// hexID turns "0x10de" into "10de" when it has exactly n hex digits.
func hexID(s string, n int) string {
	s = strings.ToLower(strings.TrimPrefix(s, "0x"))
	if len(s) != n {
		return ""
	}
	for i := 0; i < len(s); i++ {
		if !isHexDigit(s[i]) {
			return ""
		}
	}
	return s
}

// pciParent: functions 1–7 of a slot hang under function 0 of the same slot
// (a GPU's audio and USB-C functions under the GPU), function 0 under its
// bridge.
func pciParent(devs map[string]*pciDev) func(string) string {
	return func(addr string) string {
		d := devs[addr]
		if fn0 := addr[:len(addr)-1] + "0"; fn0 != addr {
			if f, ok := devs[fn0]; ok && f.bridge == d.bridge {
				return fn0
			}
		}
		return d.bridge
	}
}

func pciTree(devs []pciDev) []PCI {
	nodes := make(map[string]*pciDev, len(devs))
	order := make([]string, 0, len(devs))
	for i := range devs {
		nodes[devs[i].Address] = &devs[i]
		order = append(order, devs[i].Address)
	}
	// Parents come first: bridges precede what is behind them in sysfs
	// order except when a bridge sits on a higher bus number; a second
	// pass over the order places those.
	order = parentsFirst(order, pciParent(nodes))
	tree := buildTree(order, nodes, pciParent(nodes), func(p, c *pciDev) {
		p.Children = append(p.Children, c.PCI)
	})
	out := make([]PCI, len(tree))
	for i, t := range tree {
		out[i] = t.PCI
	}
	sortPCI(out)
	return out
}

// parentsFirst orders nodes so each comes after its parent.
func parentsFirst(order []string, parent func(string) string) []string {
	in := make(map[string]bool, len(order))
	for _, n := range order {
		in[n] = true
	}
	done := make(map[string]bool, len(order))
	out := make([]string, 0, len(order))
	var visit func(n string, depth int)
	visit = func(n string, depth int) {
		if done[n] || depth > len(order) {
			return
		}
		if p := parent(n); p != "" && in[p] && !done[p] {
			visit(p, depth+1)
		}
		if !done[n] {
			done[n] = true
			out = append(out, n)
		}
	}
	for _, n := range order {
		visit(n, 0)
	}
	return out
}

func sortPCI(s []PCI) {
	sortBy(s, func(p PCI) string { return p.Address })
	for i := range s {
		s[i].Children = orEmpty(s[i].Children)
		sortPCI(s[i].Children)
	}
}

// pciIDsPaths are where distributions keep the PCI ID database.
func pciIDsPaths() []string {
	return []string{"usr/share/misc/pci.ids", "usr/share/hwdata/pci.ids", "usr/share/pci.ids"}
}

// PCINames caches vendor and device names from pci.ids: the file (1–2 MB)
// is scanned only when a device not seen before appears.
type PCINames struct {
	vendors map[string]string // "10de" → "NVIDIA Corporation"
	devices map[string]string // "10de:1f82" → "TU117 [GeForce GTX 1650]"
}

// pciNames fills the vendor and device names; names stay empty without
// pci.ids.
func (r *reader) pciNames(devs []pciDev, cache *PCINames) {
	if cache == nil {
		cache = &PCINames{}
	}
	if cache.vendors == nil {
		cache.vendors, cache.devices = map[string]string{}, map[string]string{}
	}
	want := map[string]bool{}
	for _, d := range devs {
		if _, ok := cache.devices[d.VendorID+":"+d.DeviceID]; !ok {
			want[d.VendorID+":"+d.DeviceID] = true
		}
	}
	if len(want) > 0 {
		r.scanPCIIDs(want, cache)
		for k := range want { // negative entries: do not rescan for them
			if _, ok := cache.devices[k]; !ok {
				cache.devices[k] = ""
			}
		}
	}
	for i := range devs {
		devs[i].Vendor = cache.vendors[devs[i].VendorID]
		devs[i].Device = cache.devices[devs[i].VendorID+":"+devs[i].DeviceID]
	}
}

// scanPCIIDs reads the vendor and device lines of pci.ids:
// "10de  NVIDIA Corporation", "\t1f82  TU117 [GeForce GTX 1650]".
func (r *reader) scanPCIIDs(want map[string]bool, cache *PCINames) {
	var data []byte
	for _, p := range pciIDsPaths() {
		if d, err := fs.ReadFile(r.root, p); err == nil {
			data = d
			break
		}
	}
	if data == nil {
		return
	}
	vendors := map[string]bool{}
	for k := range want {
		vendors[k[:4]] = true
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	vendor := ""
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 || line[0] == '#' {
			continue
		}
		if line[0] == 'C' && len(line) > 1 && line[1] == ' ' {
			break // device classes follow the vendors
		}
		if line[0] != '\t' {
			vendor = ""
			if len(line) > 6 && vendors[string(line[:4])] {
				vendor = string(line[:4])
				cache.vendors[vendor] = clean(string(line[4:]))
			}
			continue
		}
		if vendor == "" || len(line) < 7 || line[1] == '\t' {
			continue
		}
		key := vendor + ":" + string(line[1:5])
		if want[key] {
			cache.devices[key] = clean(string(line[5:]))
		}
	}
}

// gpus lists display controllers (PCI class 0x03) and the Jetson
// integrated GPU, with the driver and its version.
func (r *reader) gpus(devs []pciDev, l4t string) []GPU {
	var out []GPU
	for _, d := range devs {
		if !strings.HasPrefix(d.Class, "03") {
			continue
		}
		g := GPU{
			PCIAddress: d.Address,
			Vendor:     d.Vendor,
			Model:      d.Device,
			Driver:     d.Driver,
		}
		if g.Vendor == "" {
			g.Vendor = gpuVendor(d.VendorID)
		}
		if d.Driver == "nvidia" {
			if m := r.nvidiaModel(d.Address); m != "" {
				g.Model = m
			}
			g.DriverVersion = r.nvidiaVersion()
		} else if d.Driver != "" {
			g.DriverVersion = r.line("sys/module/" + d.Driver + "/version")
		}
		if g.Model == "" {
			g.Model = d.VendorID + ":" + d.DeviceID
		}
		out = append(out, g)
	}
	out = append(out, r.tegraGPUs(l4t)...)
	if len(out) > maxGPUs {
		out = out[:maxGPUs]
	}
	return out
}

func gpuVendor(id string) string {
	switch id {
	case "10de":
		return "NVIDIA"
	case "1002":
		return "AMD"
	case "8086":
		return "Intel"
	case "1234":
		return "QEMU"
	case "1af4":
		return "Red Hat (virtio)"
	case "15ad":
		return "VMware"
	}
	return ""
}

// reNvidiaVersion is the version in /proc/driver/nvidia/version:
// "NVRM version: NVIDIA UNIX x86_64 Kernel Module  550.54.14  Thu Feb…"
// or "NVRM version: NVIDIA UNIX Open Kernel Module for x86_64  570.86.15 …".
var reNvidiaVersion = regexp.MustCompile(`Kernel Module(?: for \S+)?\s+(\d+(?:\.\d+)+)`)

func (r *reader) nvidiaVersion() string {
	data, ok := r.file("proc/driver/nvidia/version")
	if !ok {
		return ""
	}
	if m := reNvidiaVersion.FindSubmatch(data); m != nil {
		return string(m[1])
	}
	return ""
}

// nvidiaModel is "Model:" of /proc/driver/nvidia/gpus/<address>/information.
func (r *reader) nvidiaModel(addr string) string {
	data, ok := r.file("proc/driver/nvidia/gpus/" + addr + "/information")
	if !ok {
		return ""
	}
	for line := range bytes.Lines(data) {
		if k, v, found := bytes.Cut(line, []byte(":")); found && string(bytes.TrimSpace(k)) == "Model" {
			return clean(string(v))
		}
	}
	return ""
}

// tegraGPU names the integrated GPUs of Jetson modules by the device-tree
// compatible of their platform device.
func tegraGPU(compatible string) string {
	switch {
	case strings.HasSuffix(compatible, "gm20b"):
		return "Maxwell GM20B (Tegra X1)"
	case strings.HasSuffix(compatible, "gp10b"):
		return "Pascal GP10B (Tegra X2)"
	case strings.HasSuffix(compatible, "gv11b"):
		return "Volta GV11B (Xavier)"
	case strings.HasSuffix(compatible, "ga10b"):
		return "Ampere GA10B (Orin)"
	case strings.HasSuffix(compatible, "gk20a"):
		return "Kepler GK20A (Tegra K1)"
	}
	return ""
}

// tegraGPUs finds the Jetson GPU among platform devices ("57000000.gpu",
// "17000000.gpu"); its driver version is the L4T release.
func (r *reader) tegraGPUs(l4t string) []GPU {
	var out []GPU
	for _, e := range r.dir("sys/bus/platform/devices") {
		name := e.Name()
		if !strings.Contains(name, "gpu") || !validName(name) {
			continue
		}
		dir := "sys/bus/platform/devices/" + name
		data, ok := r.file(dir + "/of_node/compatible")
		if !ok {
			continue
		}
		for c := range strings.SplitSeq(string(data), "\x00") {
			if model := tegraGPU(c); model != "" {
				out = append(out, GPU{Vendor: "NVIDIA", Model: model, Driver: r.linkBase(dir + "/driver"), DriverVersion: l4t})
				break
			}
		}
	}
	return out
}
