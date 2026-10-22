package collect

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
)

// Disk I/O metrics (portal contract v1): raw /proc/diskstats counters of
// whole disks.
const (
	MetricDiskIO         = "system.disk.io"         // counter, bytes; device, direction=read|write
	MetricDiskOperations = "system.disk.operations" // counter; device, direction
	MetricDiskIOTime     = "system.disk.io_time"    // counter, s busy; device
)

// maxDisks is disks_per_machine_max of the contract.
const maxDisks = 64

// diskSector is the unit of /proc/diskstats sector counters: always 512
// bytes, whatever the device's block size.
const diskSector = 512

const sysBlock = "sys/block"

// skippedDisk tells block devices that are not disks: loop, RAM and zram
// devices, device-mapper volumes (their disks are counted), optical and
// floppy drives, network block devices, eMMC boot and RPMB areas.
func skippedDisk(name string) bool {
	for _, p := range []string{"loop", "ram", "zram", "dm-", "sr", "fd", "nbd"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return strings.HasPrefix(name, "mmcblk") && (strings.Contains(name, "boot") || strings.HasSuffix(name, "rpmb"))
}

// Disks collects bytes, operations and busy time of the whole disks listed
// in /sys/block (at most maxDisks, by name). Partitions are not in
// /sys/block; without it nothing tells disks from partitions, so nothing is
// reported.
func (c Collector) Disks(s *Snapshot) error {
	data, absent, err := c.readFile("proc/diskstats")
	if absent || err != nil {
		return err
	}
	entries, err := fs.ReadDir(c.Root, sysBlock)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("чтение /%s: %w", sysBlock, err)
	}
	disks := make(map[string]bool, len(entries))
	for _, e := range entries {
		if !skippedDisk(e.Name()) {
			disks[e.Name()] = true
		}
	}
	type stat struct {
		name string
		v    [11]uint64
	}
	var stats []stat
	for line := range bytes.Lines(data) {
		_, rest := nextField(line) // major
		_, rest = nextField(rest)  // minor
		name, rest := nextField(rest)
		if !disks[string(name)] || len(name) > 64 {
			continue
		}
		st := stat{name: string(name)}
		ok := true
		for i := range st.v {
			var f []byte
			f, rest = nextField(rest)
			if st.v[i], ok = parseUint(f); !ok {
				break
			}
		}
		if ok {
			stats = append(stats, st)
		}
	}
	slices.SortFunc(stats, func(a, b stat) int { return strings.Compare(a.name, b.name) })
	for _, st := range stats[:min(len(stats), maxDisks)] {
		dev := Attr{AttrDevice, st.name}
		rd, wr := Attr{AttrDirection, "read"}, Attr{AttrDirection, "write"}
		// Fields: reads, merged, sectors read, ms reading, writes, merged,
		// sectors written, ms writing, in flight, ms doing I/O, weighted ms.
		s.add(MetricDiskIO, Counter, float64(st.v[2])*diskSector, dev, rd)
		s.add(MetricDiskIO, Counter, float64(st.v[6])*diskSector, dev, wr)
		s.add(MetricDiskOperations, Counter, float64(st.v[0]), dev, rd)
		s.add(MetricDiskOperations, Counter, float64(st.v[4]), dev, wr)
		s.add(MetricDiskIOTime, Counter, float64(st.v[9])/1000, dev)
	}
	return nil
}
