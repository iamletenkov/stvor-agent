package inventory

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// USB device names in /sys/bus/usb/devices: root hubs "usbN", devices
// "bus-port[.port…]"; interfaces ("1-1:1.0") are not devices.
var (
	reUSBRoot   = regexp.MustCompile(`^usb(\d+)$`)
	reUSBDevice = regexp.MustCompile(`^(\d+)-(\d+(?:\.\d+)*)$`)
	reHex4      = regexp.MustCompile(`^[0-9a-f]{4}$`)
	reHex2      = regexp.MustCompile(`^[0-9a-f]{2}$`)
)

const usbDir = "sys/bus/usb/devices/"

func (r *reader) usb() []USB {
	ents := r.dir(strings.TrimSuffix(usbDir, "/"))
	var names, ifaces []string
	for _, e := range ents {
		n := e.Name()
		switch {
		case reUSBRoot.MatchString(n) || reUSBDevice.MatchString(n):
			names = append(names, n)
		case strings.Contains(n, ":") && validName(n):
			ifaces = append(ifaces, n)
		}
	}
	// Root hubs first, then devices in port order, so a parent always
	// precedes its children and the cap drops whole subtrees.
	slices.SortFunc(names, func(a, b string) int {
		ra, rb := reUSBRoot.MatchString(a), reUSBRoot.MatchString(b)
		if ra != rb {
			if ra {
				return -1
			}
			return 1
		}
		return natCompare(a, b)
	})
	if len(names) > maxUSB {
		names = names[:maxUSB]
	}
	nodes := make(map[string]*USB, len(names))
	var order []string
	for _, n := range names {
		d, ok := r.usbDevice(n, ifaces)
		if !ok {
			continue
		}
		nodes[n] = &d
		order = append(order, n)
	}
	return buildTree(order, nodes, usbParent, func(p, c *USB) { p.Children = append(p.Children, *c) })
}

func (r *reader) usbDevice(name string, ifaces []string) (USB, bool) {
	dir := usbDir + name
	d := USB{
		Path:     name,
		VID:      strings.ToLower(r.line(dir + "/idVendor")),
		PID:      strings.ToLower(r.line(dir + "/idProduct")),
		Product:  r.line(dir + "/product"),
		Children: []USB{},
	}
	if !reHex4.MatchString(d.VID) || !reHex4.MatchString(d.PID) {
		return USB{}, false
	}
	root := reUSBRoot.MatchString(name)
	if !root {
		// A root hub's manufacturer is "Linux <kernel> xhci-hcd" and its
		// serial the controller's PCI address: neither describes hardware.
		d.Manufacturer = r.line(dir + "/manufacturer")
		d.SerialHash = serialHash(r.line(dir + "/serial"))
	}
	if v, err := strconv.ParseFloat(r.line(dir+"/speed"), 64); err == nil && v > 0 {
		d.SpeedMbps = v
	}
	if c := strings.ToLower(r.line(dir + "/bDeviceClass")); reHex2.MatchString(c) {
		d.Class = c
	}
	// The driver of a device is that of its interfaces (hub, uvcvideo,
	// ftdi_sio…); the device itself is always bound to "usb".
	var drivers []string
	for _, i := range ifaces {
		if strings.HasPrefix(i, name+":") {
			if drv := r.linkBase(usbDir + i + "/driver"); drv != "" {
				drivers = appendNew(drivers, drv)
			}
		}
	}
	slices.Sort(drivers)
	d.Driver = clean(strings.Join(drivers, ","))
	if d.Driver == "" {
		d.Driver = r.linkBase(dir + "/driver")
	}
	return d, true
}

// usbParent: "1-1.2" → "1-1", "1-1" → "usb1", a root hub has none.
func usbParent(name string) string {
	m := reUSBDevice.FindStringSubmatch(name)
	if m == nil {
		return ""
	}
	if i := strings.LastIndexByte(m[2], '.'); i >= 0 {
		return m[1] + "-" + m[2][:i]
	}
	return "usb" + m[1]
}

func sortUSB(s []USB) {
	sortBy(s, func(u USB) string { return u.Path })
	for i := range s {
		s[i].Children = orEmpty(s[i].Children)
		sortUSB(s[i].Children)
	}
}

// buildTree attaches each node to its parent; order lists parents before
// children, a node whose parent is absent stays at the top.
func buildTree[T any](order []string, nodes map[string]*T, parent func(string) string, attach func(p, c *T)) []T {
	children := map[string][]string{}
	var roots []string
	for _, n := range order {
		p := parent(n)
		if _, ok := nodes[p]; p != "" && ok {
			children[p] = append(children[p], n)
		} else {
			roots = append(roots, n)
		}
	}
	var build func(n string) T
	build = func(n string) T {
		node := *nodes[n]
		for _, c := range children[n] {
			child := build(c)
			attach(&node, &child)
		}
		return node
	}
	out := make([]T, 0, len(roots))
	for _, n := range roots {
		out = append(out, build(n))
	}
	return out
}
