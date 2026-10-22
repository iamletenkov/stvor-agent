package collect

import (
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"sort"
	"strings"
	"syscall"
	"testing"
	"testing/fstest"
)

func labelValues(s *Snapshot, key string) []string {
	var out []string
	for _, p := range s.Points {
		for _, a := range p.Attrs {
			if a.Key == key && !slices.Contains(out, a.Value) {
				out = append(out, a.Value)
			}
		}
	}
	sort.Strings(out)
	return out
}

func TestDisks(t *testing.T) {
	t.Parallel()
	const sector = 512
	tests := []struct {
		name    string
		devices []string
		want    map[string]float64
	}{
		{
			name:    "debian12-amd64",
			devices: []string{"nvme0n1", "sda"},
			want: map[string]float64{
				"system.disk.io{device=nvme0n1,direction=read}":          98765432 * sector,
				"system.disk.io{device=nvme0n1,direction=write}":         445566778 * sector,
				"system.disk.operations{device=nvme0n1,direction=read}":  1123456,
				"system.disk.operations{device=nvme0n1,direction=write}": 2233445,
				"system.disk.io_time{device=nvme0n1}":                    1234.567,
				"system.disk.io{device=sda,direction=read}":              7766554 * sector,
			},
		},
		{
			name:    "ubuntu2404-amd64",
			devices: []string{"nvme0n1", "sdb"},
			want: map[string]float64{
				"system.disk.io{device=sdb,direction=write}": 9900 * sector,
				"system.disk.io_time{device=sdb}":            6.677,
			},
		},
		{
			// Kernel 4.9: 11 counters per line; eMMC boot partitions are
			// listed in /sys/block but are not disks.
			name:    "jetson-l4t-r32",
			devices: []string{"mmcblk0"},
			want: map[string]float64{
				"system.disk.io{device=mmcblk0,direction=read}":          22334456 * sector,
				"system.disk.operations{device=mmcblk0,direction=write}": 223344,
				"system.disk.io_time{device=mmcblk0}":                    667.788,
			},
		},
		{
			name:    "pve-vm",
			devices: []string{"sda"},
			want: map[string]float64{
				"system.disk.io{device=sda,direction=read}":  1227105739 * sector,
				"system.disk.io{device=sda,direction=write}": 7350333524 * sector,
				"system.disk.io_time{device=sda}":            13565.328,
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var s Snapshot
			if err := fixture(t, tc.name).Disks(&s); err != nil {
				t.Fatal(err)
			}
			if got := labelValues(&s, AttrDevice); !slices.Equal(got, tc.devices) {
				t.Errorf("devices = %v, want %v", got, tc.devices)
			}
			kinds := map[string]Kind{}
			for k := range tc.want {
				kinds[k] = Counter
			}
			checkPoints(t, &s, tc.want, kinds, nil)
		})
	}
}

func TestDisksLimitAndMalformed(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{}
	var b strings.Builder
	for i := range 70 {
		name := fmt.Sprintf("vd%03d", i)
		fmt.Fprintf(&b, "252 %d %s 1 0 8 1 1 0 8 1 0 5 2\n", i, name)
		fsys["sys/block/"+name+"/dev"] = &fstest.MapFile{Data: []byte("252:0\n")}
	}
	b.WriteString("8 0 sdz 1 x 0 0 0 0 0 0 0 0 0\n8 1 sdy 1 2 3\n")
	fsys["sys/block/sdz/dev"] = &fstest.MapFile{Data: []byte("8:0\n")}
	fsys["sys/block/sdy/dev"] = &fstest.MapFile{Data: []byte("8:1\n")}
	fsys["proc/diskstats"] = &fstest.MapFile{Data: []byte(b.String())}
	var s Snapshot
	if err := (Collector{Root: fsys}).Disks(&s); err != nil {
		t.Fatal(err)
	}
	got := labelValues(&s, AttrDevice)
	if len(got) != maxDisks || got[0] != "vd000" || got[len(got)-1] != fmt.Sprintf("vd%03d", maxDisks-1) {
		t.Errorf("devices = %d %v…, want the first %d by name", len(got), got[:3], maxDisks)
	}
	// No /sys/block — no way to tell disks from partitions: nothing.
	s.Reset()
	if err := (Collector{Root: fstest.MapFS{"proc/diskstats": fsys["proc/diskstats"]}}).Disks(&s); err != nil || len(s.Points) != 0 {
		t.Errorf("without /sys/block: %d points, %v", len(s.Points), err)
	}
}

// statfsFake answers for the listed mount points; others do not exist.
func statfsFake(m map[string]FSStat, fail map[string]error) StatfsFunc {
	return func(path string) (FSStat, error) {
		if err, ok := fail[path]; ok {
			return FSStat{}, err
		}
		st, ok := m[path]
		if !ok {
			return FSStat{}, fs.ErrNotExist
		}
		return st, nil
	}
}

const gib = 1 << 30

func TestFilesystems(t *testing.T) {
	t.Parallel()
	root := FSStat{Size: 100 * gib, Free: 40 * gib, Avail: 35 * gib, Files: 6_000_000, FilesFree: 5_000_000}
	efi := FSStat{Size: 512 << 20, Free: 500 << 20, Avail: 500 << 20}
	tests := []struct {
		name   string
		stats  map[string]FSStat
		mounts []string
		want   map[string]float64
		absent []string
	}{
		{
			name: "debian12-amd64",
			stats: map[string]FSStat{
				"/": root, "/boot/efi": efi, "/home": root, "/data": root, "/srv/data": root,
				"/media/usb disk": efi, "/backup": root, "/snap/core22/1612": root, "/mnt/image": root,
				"/mnt/nas": root, "/run/user/1000": root,
			},
			// bind mount /srv/data (same device as /data), squashfs, a loop
			// image, NFS, overlay and tmpfs are not reported.
			mounts: []string{"/", "/backup", "/boot/efi", "/data", "/home", "/media/usb disk"},
			want: map[string]float64{
				"system.filesystem.usage{mountpoint=/,device=nvme0n1p2,type=ext4,state=used}":        60 * gib,
				"system.filesystem.usage{mountpoint=/,device=nvme0n1p2,type=ext4,state=free}":        35 * gib,
				"system.filesystem.usage{mountpoint=/,device=nvme0n1p2,type=ext4,state=reserved}":    5 * gib,
				"system.filesystem.limit{mountpoint=/,device=nvme0n1p2,type=ext4}":                   100 * gib,
				"system.filesystem.inodes.usage{mountpoint=/,device=nvme0n1p2,type=ext4,state=used}": 1_000_000,
				"system.filesystem.inodes.usage{mountpoint=/,device=nvme0n1p2,type=ext4,state=free}": 5_000_000,
				"system.filesystem.inodes.limit{mountpoint=/,device=nvme0n1p2,type=ext4}":            6_000_000,
				"system.filesystem.readonly{mountpoint=/,device=nvme0n1p2,type=ext4}":                0,
				"system.filesystem.readonly{mountpoint=/home,device=mapper/vg0-home,type=ext4}":      0,
				"system.filesystem.readonly{mountpoint=/data,device=sda1,type=xfs}":                  0,
				"system.filesystem.readonly{mountpoint=/media/usb disk,device=sdb1,type=vfat}":       1,
				"system.filesystem.readonly{mountpoint=/backup,device=sdc1,type=ext4}":               1,
				"system.filesystem.limit{mountpoint=/boot/efi,device=nvme0n1p1,type=vfat}":           512 << 20,
			},
			// vfat has no inodes: no inode series rather than zeros.
			absent: []string{"system.filesystem.inodes.limit{mountpoint=/boot/efi", "system.filesystem.inodes.usage{mountpoint=/boot/efi"},
		},
		{
			name:  "ubuntu2404-amd64",
			stats: map[string]FSStat{"/": root, "/boot/efi": efi, "/home": root, "/var/lib/libvirt": root, "/media/user/CAMERA": efi},
			// A second subvolume of the same btrfs is the same filesystem.
			mounts: []string{"/", "/boot/efi", "/home", "/media/*/CAMERA"},
			want: map[string]float64{
				"system.filesystem.limit{mountpoint=/home,device=nvme0n1p3,type=btrfs}":      100 * gib,
				"system.filesystem.limit{mountpoint=/media/*/CAMERA,device=sdb1,type=exfat}": 512 << 20,
			},
		},
		{
			name:   "jetson-l4t-r32",
			stats:  map[string]FSStat{"/": root},
			mounts: []string{"/"},
			want: map[string]float64{
				"system.filesystem.usage{mountpoint=/,device=mmcblk0p1,type=ext4,state=used}": 60 * gib,
			},
		},
		{
			// The agent's own view under ProtectSystem=strict, PrivateTmp and
			// ReadWritePaths: every mount is read-only at mount level, the
			// superblocks are rw. Only the superblock option counts. Sandbox
			// bind mounts of / are the same filesystem. User names in
			// /media/<user>, /run/media/<user> and /home/<user> become "*";
			// fuseblk (ntfs-3g) is not statfs'ed: no timeout on a hung daemon.
			name: "sandbox-strict",
			stats: map[string]FSStat{
				"/": root, "/boot/efi": efi, "/home": root, "/tmp": root, "/var/tmp": root,
				"/var/lib/stvor-agent": root, "/media/alice/CAMERA": efi, "/home/bob/data": root,
				"/run/media/carol/BACKUP": root, "/media/dave/CAMERA": efi, "/media/usbkey": root,
			},
			mounts: []string{"/", "/boot/efi", "/home", "/home/*/data", "/media/*/CAMERA", "/run/media/*/BACKUP"},
			want: map[string]float64{
				"system.filesystem.readonly{mountpoint=/,device=nvme0n1p2,type=ext4}":              0,
				"system.filesystem.readonly{mountpoint=/boot/efi,device=nvme0n1p1,type=vfat}":      0,
				"system.filesystem.readonly{mountpoint=/home,device=mapper/vg0-home,type=ext4}":    0,
				"system.filesystem.readonly{mountpoint=/media/*/CAMERA,device=sdc1,type=exfat}":    0,
				"system.filesystem.readonly{mountpoint=/media/*/CAMERA,device=sdf1,type=exfat}":    0,
				"system.filesystem.readonly{mountpoint=/home/*/data,device=sdd1,type=ext4}":        0,
				"system.filesystem.readonly{mountpoint=/run/media/*/BACKUP,device=sde1,type=ext4}": 1,
			},
			absent: []string{"system.filesystem.limit{mountpoint=/media/usbkey"},
		},
		{
			name:   "pve-vm",
			stats:  map[string]FSStat{"/": root, "/boot/efi": efi},
			mounts: []string{"/", "/boot/efi"},
			want: map[string]float64{
				"system.filesystem.limit{mountpoint=/,device=sda1,type=ext4}":          100 * gib,
				"system.filesystem.limit{mountpoint=/boot/efi,device=sda15,type=vfat}": 512 << 20,
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := fixture(t, tc.name)
			c.Statfs = statfsFake(tc.stats, nil)
			var s Snapshot
			if err := c.Filesystems(&s); err != nil {
				t.Fatal(err)
			}
			if got := labelValues(&s, AttrMountpoint); !slices.Equal(got, tc.mounts) {
				t.Errorf("mount points = %v, want %v", got, tc.mounts)
			}
			checkPoints(t, &s, tc.want, nil, tc.absent)
		})
	}
}

func TestFilesystemsStatfsErrors(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	c := fixture(t, "pve-vm")
	c.Statfs = statfsFake(map[string]FSStat{"/": {Size: 10, Free: 5, Avail: 5}},
		map[string]error{"/boot/efi": boom})
	var s Snapshot
	if err := c.Filesystems(&s); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if got := labelValues(&s, AttrMountpoint); !slices.Equal(got, []string{"/"}) {
		t.Errorf("a failing mount point must not hide the others: %v", got)
	}
	// Permission denied or a mount point gone between reads: skipped quietly.
	c.Statfs = statfsFake(nil, map[string]error{"/": syscall.EACCES, "/boot/efi": syscall.ENOENT})
	s.Reset()
	if err := c.Filesystems(&s); err != nil || len(s.Points) != 0 {
		t.Errorf("EACCES/ENOENT: %d points, %v", len(s.Points), err)
	}
	// No Statfs hook: no filesystem series.
	c.Statfs = nil
	s.Reset()
	if err := c.Filesystems(&s); err != nil || len(s.Points) != 0 {
		t.Errorf("nil Statfs: %d points, %v", len(s.Points), err)
	}
}

func TestFilesystemsLimitAndLabels(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	for i := range 70 {
		fmt.Fprintf(&b, "%d 1 8:%d / /m%03d rw - ext4 /dev/sd%d rw\n", 100+i, i, i, i)
	}
	long := "/" + strings.Repeat("x", 128)
	fmt.Fprintf(&b, "300 1 9:1 / %s rw - ext4 /dev/sdx1 rw\n", long)
	fmt.Fprintf(&b, "301 1 9:2 / /odd rw - ext4 /dev/disk/by-label/a\\040b rw\n")
	fmt.Fprintf(&b, "302 1 9:3 / /bcache rw - bcachefs /dev/sdy rw\n")
	fmt.Fprintf(&b, "broken line\n")
	fsys := fstest.MapFS{"proc/self/mountinfo": {Data: []byte(b.String())}}
	all := func(string) (FSStat, error) { return FSStat{Size: 1, Free: 1, Avail: 1}, nil }

	var s Snapshot
	if err := (Collector{Root: fsys, Statfs: all}).Filesystems(&s); err != nil {
		t.Fatal(err)
	}
	if got := labelValues(&s, AttrMountpoint); len(got) != maxFilesystems {
		t.Errorf("filesystems = %d, want %d", len(got), maxFilesystems)
	}

	fsys["proc/self/mountinfo"] = &fstest.MapFile{Data: []byte(fmt.Sprintf(
		"300 1 9:1 / %s rw - ext4 /dev/sdx1 rw\n301 1 9:2 / /odd rw - ext4 /dev/disk/by-label/a\\040b rw\n"+
			"302 1 9:3 / /bcache rw - bcachefs /dev/sdy rw\n", long))}
	s.Reset()
	if err := (Collector{Root: fsys, Statfs: all}).Filesystems(&s); err != nil {
		t.Fatal(err)
	}
	got := pointMap(t, &s)
	// A mount point over 128 characters breaks the label limit: skipped. A
	// device name outside the label pattern is left out; an unlisted type
	// is "other".
	checkPoints(t, &s, map[string]float64{
		"system.filesystem.limit{mountpoint=/odd,type=ext4}":                1,
		"system.filesystem.limit{mountpoint=/bcache,device=sdy,type=other}": 1,
	}, nil, []string{"system.filesystem.limit{mountpoint=/xxx"})
	if len(got) == 0 {
		t.Error("no points")
	}
}

func TestUnescapeMount(t *testing.T) {
	t.Parallel()
	tests := []struct{ in, want string }{
		{`/media/usb\040disk`, "/media/usb disk"},
		{`/a\011b\012c\134d`, "/a\tb\nc\\d"},
		{`/plain`, "/plain"},
		{`/bad\09`, `/bad\09`},
	}
	for _, tc := range tests {
		if got := unescapeMount([]byte(tc.in)); got != tc.want {
			t.Errorf("unescapeMount(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestStatfsRoot(t *testing.T) {
	t.Parallel()
	st, err := Statfs("/")
	if err != nil {
		t.Fatal(err)
	}
	if st.Size == 0 || st.Avail > st.Free || st.Free > st.Size {
		t.Errorf("statfs(/) = %+v", st)
	}
	if _, err := Statfs("/nonexistent-stvor-path"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing path: %v, want ErrNotExist", err)
	}
}

func TestThermal(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		want   map[string]float64
		zones  int
		absent []string
		caps   []string
	}{
		{
			name: "debian12-amd64",
			want: map[string]float64{
				"system.thermal.temperature{zone=acpitz}":                      27.8,
				"system.thermal.temperature{zone=acpitz#2}":                    29.8,
				"system.thermal.temperature{zone=x86_pkg_temp}":                45,
				"system.thermal.temperature{zone=hwmon:acpitz/temp1}":          27.8,
				"system.thermal.temperature{zone=hwmon:coretemp/Package id 0}": 45,
				"system.thermal.temperature{zone=hwmon:coretemp/Core 0}":       44,
				"system.thermal.temperature{zone=hwmon:coretemp/Core 1}":       46,
				"system.thermal.temperature{zone=hwmon:nvme/Composite}":        38.85,
				"system.thermal.temperature{zone=hwmon:nvme/Sensor 1}":         38.85,
			},
			// A zone without temp, a hwmon without temp inputs and a
			// sensor reading −273.15 °C are absent.
			zones:  9,
			absent: []string{"system.thermal.temperature{zone=iwlwifi", "system.thermal.temperature{zone=hwmon:nvme/Sensor 2"},
			caps:   []string{CapThermal},
		},
		{
			name: "ubuntu2404-amd64",
			want: map[string]float64{
				"system.thermal.temperature{zone=acpitz}":                 16.8,
				"system.thermal.temperature{zone=hwmon:k10temp/Tctl}":     52.125,
				"system.thermal.temperature{zone=hwmon:k10temp/Tccd1}":    48,
				"system.thermal.temperature{zone=hwmon:amdgpu/edge}":      41,
				"system.thermal.temperature{zone=hwmon:nvme/Composite}":   39.85,
				"system.thermal.temperature{zone=hwmon:nvme/Composite#2}": 33.85,
				"system.thermal.temperature{zone=hwmon:iwlwifi_1/temp1}":  44,
			},
			zones: 8,
			caps:  []string{CapThermal},
		},
		{
			name: "jetson-l4t-r32",
			want: map[string]float64{
				"system.thermal.temperature{zone=AO-therm}":                 34.5,
				"system.thermal.temperature{zone=CPU-therm}":                37.5,
				"system.thermal.temperature{zone=GPU-therm}":                36,
				"system.thermal.temperature{zone=PLL-therm}":                35,
				"system.thermal.temperature{zone=PMIC-Die}":                 100,
				"system.thermal.temperature{zone=thermal-fan-est}":          36.7,
				"system.thermal.temperature{zone=hwmon:tegra_soctherm/CPU}": 37,
			},
			zones: 7,
			caps:  []string{CapThermal},
		},
		{
			// A Proxmox VM has no thermal zones and no hwmon: no series, no
			// capability.
			name:  "pve-vm",
			zones: 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var s Snapshot
			if err := fixture(t, tc.name).Thermal(&s); err != nil {
				t.Fatal(err)
			}
			if n := len(labelValues(&s, AttrZone)); n != tc.zones {
				t.Errorf("zones = %d (%v), want %d", n, labelValues(&s, AttrZone), tc.zones)
			}
			checkPoints(t, &s, tc.want, nil, tc.absent)
			for _, p := range s.Points {
				if p.Name != MetricTemperature || p.Kind != Gauge {
					t.Errorf("unexpected point %s (%v)", key(p), p.Kind)
				}
			}
			if !slices.Equal(s.Capabilities, tc.caps) {
				t.Errorf("capabilities = %v, want %v", s.Capabilities, tc.caps)
			}
		})
	}
}

func TestThermalLimit(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{}
	for i := range 80 {
		d := fmt.Sprintf("sys/class/thermal/thermal_zone%d/", i)
		fsys[d+"type"] = &fstest.MapFile{Data: []byte(fmt.Sprintf("z%02d\n", i))}
		fsys[d+"temp"] = &fstest.MapFile{Data: []byte("40000\n")}
	}
	var s Snapshot
	if err := (Collector{Root: fsys}).Thermal(&s); err != nil {
		t.Fatal(err)
	}
	if n := len(s.Points); n != maxThermalZones {
		t.Errorf("zones = %d, want %d", n, maxThermalZones)
	}
}

// A user name in a mount point is personal data: the segment becomes "*".
func TestMaskUserMount(t *testing.T) {
	t.Parallel()
	tests := []struct{ in, want string }{
		{"/", "/"},
		{"/home", "/home"},
		{"/home/alice", "/home/*"},
		{"/home/alice/data disk", "/home/*/data disk"},
		{"/media/alice/CAMERA", "/media/*/CAMERA"},
		{"/media/usb disk", "/media/usb disk"}, // /media/<label> without a user directory
		{"/media", "/media"},
		{"/run/media/bob/BACKUP", "/run/media/*/BACKUP"},
		{"/run/media/bob", "/run/media/*"},
		{"/homework/x", "/homework/x"},
		{"/mnt/home/alice", "/mnt/home/alice"},
	}
	for _, tc := range tests {
		if got := maskUserMount(tc.in); got != tc.want {
			t.Errorf("maskUserMount(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRealFSType(t *testing.T) {
	t.Parallel()
	// fuseblk is served by a user-space daemon: statfs on a hung one blocks
	// the collection loop with no timeout, so it is never reported.
	for _, typ := range []string{"fuseblk", "fuse.sshfs", "nfs4", "cifs", "tmpfs", "overlay", "squashfs"} {
		if _, ok := realFSType(typ); ok {
			t.Errorf("%s must not be statfs'ed", typ)
		}
	}
	for _, typ := range []string{"ext4", "xfs", "btrfs", "vfat", "exfat", "ntfs3"} {
		if got, ok := realFSType(typ); !ok || got != typ {
			t.Errorf("realFSType(%s) = %q, %v", typ, got, ok)
		}
	}
}
