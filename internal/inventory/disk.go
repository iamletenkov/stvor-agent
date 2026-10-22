package inventory

import (
	"bytes"
	"slices"
	"strings"
)

// Disk types of the contract.
const (
	DiskNVMe    = "nvme"
	DiskSSD     = "ssd"
	DiskHDD     = "hdd"
	DiskEMMC    = "emmc"
	DiskSD      = "sd"
	DiskUSB     = "usb"
	DiskVirtual = "virtual"
	DiskOther   = "other"
)

// skippedBlock are block devices that are not disks: loop images, RAM
// disks, zram swap, network block devices, SPI boot flash, eMMC boot areas
// and RPMB, and stacked devices (LVM, dm-crypt, md RAID) whose mount points
// are reported on the partitions under them.
func skippedBlock(name string) bool {
	for _, p := range []string{"loop", "ram", "zram", "nbd", "mtdblock", "dm-", "md", "fd"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return strings.HasPrefix(name, "mmcblk") && (strings.Contains(name, "boot") || strings.Contains(name, "rpmb"))
}

type mount struct {
	devno, mountpoint, fstype string
}

func (r *reader) disks() []Disk {
	mounts := r.mounts()
	swaps := r.swaps()
	var out []Disk
	for _, e := range r.dir("sys/block") {
		name := e.Name()
		if skippedBlock(name) || !validName(name) {
			continue
		}
		dir := "sys/block/" + name
		sectors, ok := r.int64(dir + "/size")
		if !ok || sectors <= 0 {
			continue // no medium: an empty card reader or optical drive
		}
		d := Disk{
			Name:      name,
			SizeBytes: sectors * 512,
			Removable: r.line(dir+"/removable") == "1",
			Model:     r.diskModel(dir),
			Type:      r.diskType(name, dir),
		}
		serial := r.line(dir + "/device/serial")
		if serial == "" {
			serial = r.line(dir + "/serial")
		}
		d.SerialHash = serialHash(serial)
		d.Partitions = r.partitions(dir, mounts, swaps)
		out = append(out, d)
	}
	sortBy(out, func(d Disk) string { return d.Name })
	if len(out) > maxDisks {
		out = out[:maxDisks]
	}
	return out
}

func (r *reader) diskModel(dir string) string {
	model := dmi(r.line(dir + "/device/model"))
	if model == "" {
		model = dmi(r.line(dir + "/device/name")) // MMC/SD card product name
	}
	vendor := dmi(r.line(dir + "/device/vendor"))
	if vendor != "" && vendor != "ATA" && !strings.HasPrefix(model, vendor) && !strings.HasPrefix(vendor, "0x") {
		model = strings.TrimSpace(vendor + " " + model)
	}
	return model
}

func (r *reader) diskType(name, dir string) string {
	target := r.link(dir)
	vendor := r.line(dir + "/device/vendor")
	model := r.line(dir + "/device/model")
	switch {
	case strings.HasPrefix(name, "nvme"):
		return DiskNVMe
	case strings.HasPrefix(name, "mmcblk"):
		if r.line(dir+"/device/type") == "MMC" {
			return DiskEMMC
		}
		return DiskSD
	case strings.Contains(target, "/usb"):
		return DiskUSB
	case strings.HasPrefix(name, "vd") || strings.HasPrefix(name, "xvd") ||
		vendor == "QEMU" || vendor == "VMware" || vendor == "VMware," || vendor == "Msft" ||
		strings.Contains(model, "Virtual") || strings.Contains(model, "VBOX"):
		return DiskVirtual
	case strings.HasPrefix(name, "sr"):
		return DiskOther
	case r.line(dir+"/queue/rotational") == "1":
		return DiskHDD
	case strings.HasPrefix(name, "sd"):
		return DiskSSD
	}
	return DiskOther
}

// partitions lists the partitions of a disk with where they are mounted,
// directly or through the devices stacked on them (LVM, dm-crypt, md).
func (r *reader) partitions(dir string, mounts []mount, swaps map[string]bool) []Partition {
	var out []Partition
	for _, e := range r.dir(dir) {
		name := e.Name()
		pdir := dir + "/" + name
		if !validName(name) || !r.exists(pdir+"/partition") {
			continue
		}
		sectors, _ := r.int64(pdir + "/size")
		p := Partition{Name: name, SizeBytes: sectors * 512, Mountpoints: []string{}}
		devnos := r.stackedDevnos(pdir, 0)
		for _, m := range mounts {
			if !devnos[m.devno] {
				continue
			}
			if p.FSType == "" {
				p.FSType = m.fstype
			}
			if len(p.Mountpoints) < maxMountpoints && !slices.Contains(p.Mountpoints, m.mountpoint) {
				p.Mountpoints = append(p.Mountpoints, m.mountpoint)
			}
			switch m.mountpoint {
			case "/":
				p.IsRoot = true
			case "/boot/efi", "/efi":
				p.IsEFI = true
			}
		}
		if p.FSType == "" && swaps[name] {
			p.FSType = "swap"
		}
		sortStrings(p.Mountpoints)
		out = append(out, p)
	}
	sortBy(out, func(p Partition) string { return p.Name })
	if len(out) > maxPartitions {
		out = out[:maxPartitions]
	}
	return out
}

// stackedDevnos is the device number of a block device and of the devices
// stacked on it (holders), a few levels deep.
func (r *reader) stackedDevnos(dir string, depth int) map[string]bool {
	out := map[string]bool{}
	if dev := r.line(dir + "/dev"); dev != "" {
		out[dev] = true
	}
	if depth >= 4 {
		return out
	}
	for _, h := range r.dir(dir + "/holders") {
		if !validName(h.Name()) {
			continue
		}
		for k := range r.stackedDevnos("sys/block/"+h.Name(), depth+1) {
			out[k] = true
		}
	}
	return out
}

// mounts reads /proc/self/mountinfo:
// "id parent major:minor root mountpoint options [optional…] - fstype source superoptions".
//
// Only mounts of a whole filesystem count: a line whose root (field 4) is
// not "/" is a bind mount of a directory — the agent's own unit sandbox
// (ReadWritePaths, PrivateTmp /tmp and /var/tmp, BindPaths) or a user's
// bind — and would add mount points that exist only in the agent's
// namespace. btrfs is the explicit exception: a subvolume mount has the
// subvolume path as its root, equal to the subvol= superoption, and is
// kept; a directory inside a subvolume (root ≠ subvol=) is a bind and is
// skipped. User names in mount points are masked (maskUserMount).
func (r *reader) mounts() []mount {
	data, ok := r.file("proc/self/mountinfo")
	if !ok {
		return nil
	}
	var out []mount
	for line := range bytes.Lines(data) {
		f := bytes.Fields(line)
		sep := -1
		for i := 6; i < len(f); i++ {
			if string(f[i]) == "-" {
				sep = i
				break
			}
		}
		if sep < 0 || sep+1 >= len(f) {
			continue
		}
		var superopts []byte
		if sep+3 < len(f) {
			superopts = f[sep+3]
		}
		if !wholeFilesystem(unescapeMount(string(f[3])), string(f[sep+1]), superopts) {
			continue
		}
		mp := clean(maskUserMount(unescapeMount(string(f[4]))))
		if mp == "" || mp[0] != '/' {
			continue
		}
		out = append(out, mount{devno: string(f[2]), mountpoint: mp, fstype: clean(string(f[sep+1]))})
	}
	return out
}

// wholeFilesystem: the mount shows the filesystem root, or a btrfs
// subvolume as a whole.
func wholeFilesystem(root, fstype string, superopts []byte) bool {
	if root == "/" {
		return true
	}
	if fstype != "btrfs" {
		return false
	}
	for o := range bytes.SplitSeq(superopts, []byte(",")) {
		if v, ok := bytes.CutPrefix(o, []byte("subvol=")); ok {
			return unescapeMount(string(v)) == root
		}
	}
	return false
}

// maskUserMount replaces the user name in /home/<user>[/…],
// /run/media/<user>[/…] and /media/<user>/… (udisks2) with "*": user names
// must not leave the machine. /media/<label> without a deeper segment is a
// label, not a user. The same rule as in internal/collect.
func maskUserMount(m string) string {
	for _, p := range []struct {
		prefix string
		deeper bool // the segment is a user only when something follows it
	}{{"/home/", false}, {"/run/media/", false}, {"/media/", true}} {
		rest, ok := strings.CutPrefix(m, p.prefix)
		if !ok || rest == "" {
			continue
		}
		user, tail, found := strings.Cut(rest, "/")
		if user == "" || p.deeper && (!found || tail == "") {
			return m
		}
		if found {
			return p.prefix + "*/" + tail
		}
		return p.prefix + "*"
	}
	return m
}

// swaps lists the swap partitions of /proc/swaps by device name.
func (r *reader) swaps() map[string]bool {
	out := map[string]bool{}
	data, ok := r.file("proc/swaps")
	if !ok {
		return out
	}
	for line := range bytes.Lines(data) {
		f := bytes.Fields(line)
		if len(f) >= 2 && string(f[1]) == "partition" && bytes.HasPrefix(f[0], []byte("/dev/")) {
			out[string(f[0][len("/dev/"):])] = true
		}
	}
	return out
}

// unescapeMount decodes the octal escapes (\040 space…) of mount paths.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) && isOctal(s[i+1]) && isOctal(s[i+2]) && isOctal(s[i+3]) {
			b.WriteByte((s[i+1]-'0')<<6 | (s[i+2]-'0')<<3 | (s[i+3] - '0'))
			i += 3
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isOctal(c byte) bool { return c >= '0' && c <= '7' }

// validName accepts kernel device names: no path separators, no dots-only,
// printable ASCII.
func validName(s string) bool {
	if s == "" || s == "." || s == ".." || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c <= ' ' || c >= 0x7f || c == '/' {
			return false
		}
	}
	return true
}
