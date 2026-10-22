package collect

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"syscall"
	"unicode/utf8"
)

// Filesystem metrics (portal contract v1), labelled mountpoint, device
// (without /dev/), type.
const (
	MetricFSUsage       = "system.filesystem.usage"        // gauge, bytes; state=used|free|reserved
	MetricFSLimit       = "system.filesystem.limit"        // gauge, bytes
	MetricFSInodesUsage = "system.filesystem.inodes.usage" // gauge; state=used|free
	MetricFSInodesLimit = "system.filesystem.inodes.limit" // gauge
	MetricFSReadonly    = "system.filesystem.readonly"     // gauge 0 rw / 1 ro
)

// maxFilesystems is filesystems_per_machine_max of the contract.
const maxFilesystems = 64

// maxLabel is label_value_max of the contract (mountpoint).
const maxLabel = 128

// FSStat is the usage of one filesystem in bytes and inodes.
type FSStat struct {
	Size, Free, Avail uint64 // Free includes the root-reserved blocks, Avail does not
	Files, FilesFree  uint64 // zero Files — the filesystem has no inode limit (vfat, btrfs)
}

// StatfsFunc reads the usage of the filesystem mounted at path.
type StatfsFunc func(path string) (FSStat, error)

// Statfs is statfs(2): no privileges beyond search permission on the path.
func Statfs(path string) (FSStat, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return FSStat{}, fmt.Errorf("statfs %s: %w", path, err)
	}
	bs := uint64(st.Frsize) //nolint:gosec // block sizes are positive
	if bs == 0 {
		bs = uint64(st.Bsize) //nolint:gosec // block sizes are positive
	}
	// ST_RDONLY of statfs is the mount-level flag: under the unit's
	// ProtectSystem=strict every mount the agent sees is read-only, so the
	// read-only metric comes from the superblock options of mountinfo.
	return FSStat{
		Size:      st.Blocks * bs,
		Free:      st.Bfree * bs,
		Avail:     st.Bavail * bs,
		Files:     st.Files,
		FilesFree: st.Ffree,
	}, nil
}

// realFSTypes are local filesystems on block devices, with the type label
// of the contract; anything else (tmpfs, overlay, squashfs, proc, sysfs,
// cgroup, fuse.* and fuseblk, and network filesystems — statfs(2) has no
// timeout and a hung server or FUSE daemon would block the collection loop)
// is not reported. Types outside the contract enum are "other".
func realFSType(fstype string) (string, bool) {
	switch fstype {
	case "ext2", "ext3", "ext4", "xfs", "btrfs", "f2fs", "vfat", "exfat", "ntfs", "ntfs3", "zfs", "jfs":
		return fstype, true
	case "msdos", "bcachefs", "nilfs2", "reiserfs", "hfsplus", "udf":
		return "other", true
	}
	return "", false
}

type mountEntry struct {
	devno, mountpoint, fstype, source string
	// readonly is the superblock "ro" (superoptions after the "-"). The
	// mount-level options (field 6) are not used: the agent's unit runs
	// with ProtectSystem=strict, which makes every mount it sees ro at
	// mount level while the filesystem itself is writable.
	readonly bool
}

// parseMountinfo reads /proc/self/mountinfo lines:
// "id parent major:minor root mountpoint options [optional…] - fstype source superoptions".
func parseMountinfo(data []byte) []mountEntry {
	var out []mountEntry
	for line := range bytes.Lines(data) {
		fields := bytes.Fields(line)
		sep := -1
		for i := 6; i < len(fields); i++ {
			if string(fields[i]) == "-" {
				sep = i
				break
			}
		}
		if sep < 0 || sep+3 > len(fields) {
			continue
		}
		e := mountEntry{
			devno:      string(fields[2]),
			mountpoint: unescapeMount(fields[4]),
			fstype:     string(fields[sep+1]),
			source:     unescapeMount(fields[sep+2]),
		}
		if sep+3 < len(fields) {
			e.readonly = hasOption(fields[sep+3], "ro")
		}
		out = append(out, e)
	}
	return out
}

func hasOption(opts []byte, opt string) bool {
	for o := range bytes.SplitSeq(opts, []byte(",")) {
		if string(o) == opt {
			return true
		}
	}
	return false
}

// unescapeMount decodes the octal escapes (\040 space, \011 tab, \012
// newline, \134 backslash) the kernel writes in mount paths.
func unescapeMount(b []byte) string {
	if bytes.IndexByte(b, '\\') < 0 {
		return string(b)
	}
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		if b[i] == '\\' && i+3 < len(b) && isOctal(b[i+1]) && isOctal(b[i+2]) && isOctal(b[i+3]) {
			out = append(out, (b[i+1]-'0')<<6|(b[i+2]-'0')<<3|(b[i+3]-'0'))
			i += 3
			continue
		}
		out = append(out, b[i])
	}
	return string(out)
}

func isOctal(c byte) bool { return c >= '0' && c <= '7' }

// validMountpoint is the mountpoint label pattern: absolute, no control
// characters, at most maxLabel characters.
func validMountpoint(m string) bool {
	if len(m) == 0 || m[0] != '/' || utf8.RuneCountInString(m) > maxLabel || !utf8.ValidString(m) {
		return false
	}
	for _, r := range m {
		if r < 0x20 {
			return false
		}
	}
	return true
}

// maskUserMount replaces the user name in /home/<user>[/…],
// /run/media/<user>[/…] and /media/<user>/… (udisks2) with "*": mount
// points leave the machine as labels and inventory, user names must not.
// /media/<label> without a deeper segment is a label, not a user.
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

// deviceLabel is the source without /dev/ when it fits the device label
// pattern ^[A-Za-z0-9_.:/-]{1,64}$; otherwise the label is left out.
func deviceLabel(source string) (string, bool) {
	d := source
	if len(d) > 5 && d[:5] == "/dev/" {
		d = d[5:]
	}
	if len(d) == 0 || len(d) > 64 {
		return "", false
	}
	if !labelChars(d, "_.:/-") {
		return "", false
	}
	return d, true
}

// Filesystems collects space, inodes and the read-only flag of real
// filesystems (at most maxFilesystems, in mount order). A bind mount or a
// second btrfs subvolume of an already reported filesystem is skipped, as
// are loop-mounted images. User names in mount points are masked
// (maskUserMount). statfs is Collector.Statfs; nil — nothing.
func (c Collector) Filesystems(s *Snapshot) error {
	if c.Statfs == nil {
		return nil
	}
	data, absent, err := c.readFile("proc/self/mountinfo")
	if absent || err != nil {
		return err
	}
	seen := map[string]bool{}
	var errs []error
	n := 0
	for _, m := range parseMountinfo(data) {
		if n == maxFilesystems {
			break
		}
		typ, ok := realFSType(m.fstype)
		if !ok || !validMountpoint(m.mountpoint) || bytes.HasPrefix([]byte(m.source), []byte("/dev/loop")) {
			continue
		}
		if seen["dev:"+m.devno] || seen["src:"+m.source] {
			continue
		}
		st, err := c.Statfs(m.mountpoint)
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) {
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		seen["dev:"+m.devno], seen["src:"+m.source] = true, true
		label := maskUserMount(m.mountpoint)
		d, hasDev := deviceLabel(m.source)
		// Two users' mounts of the same name are one series after masking
		// unless the device tells them apart.
		series := "lbl:" + label + "\x00" + d
		if seen[series] {
			continue
		}
		seen[series] = true
		n++
		attrs := []Attr{{AttrMountpoint, label}}
		if hasDev {
			attrs = append(attrs, Attr{AttrDevice, d})
		}
		attrs = append(attrs, Attr{AttrType, typ})
		with := func(state string) []Attr { return append(attrs[:len(attrs):len(attrs)], Attr{AttrState, state}) }
		free := min(st.Free, st.Size)
		avail := min(st.Avail, free)
		s.add(MetricFSUsage, Gauge, float64(st.Size-free), with("used")...)
		s.add(MetricFSUsage, Gauge, float64(avail), with("free")...)
		s.add(MetricFSUsage, Gauge, float64(free-avail), with("reserved")...)
		s.add(MetricFSLimit, Gauge, float64(st.Size), attrs...)
		if st.Files > 0 {
			ffree := min(st.FilesFree, st.Files)
			s.add(MetricFSInodesUsage, Gauge, float64(st.Files-ffree), with("used")...)
			s.add(MetricFSInodesUsage, Gauge, float64(ffree), with("free")...)
			s.add(MetricFSInodesLimit, Gauge, float64(st.Files), attrs...)
		}
		ro := 0.0
		if m.readonly {
			ro = 1
		}
		s.add(MetricFSReadonly, Gauge, ro, attrs...)
	}
	return errors.Join(errs...)
}
