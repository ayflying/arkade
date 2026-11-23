package hostmetrics

import (
	"bufio"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"syscall"

	"arkade/protocol/envelope"
)

func newCollector(nativeDisk bool) *Collector {
	return &Collector{
		readCPU: func() *cpuTimes {
			f := openProcFile("/proc/stat")
			if f == nil {
				return nil
			}
			defer f.Close()
			return parseCPU(f)
		},
		readMemory: func() *envelope.ResourceUsage {
			f := openProcFile("/proc/meminfo")
			if f == nil {
				return nil
			}
			defer f.Close()
			return parseMemory(f)
		},
		readDisk: func() *envelope.DiskUsage { return sampleDisk(nativeDisk) },
	}
}

// Check the file itself rather than /proc: lxcfs may replace just one file.
func openProcFile(path string) *os.File {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	var stat syscall.Statfs_t
	if err := syscall.Fstatfs(int(f.Fd()), &stat); err != nil || stat.Type != 0x9fa0 { // PROC_SUPER_MAGIC
		f.Close()
		return nil
	}
	return f
}

func parseCPU(r io.Reader) *cpuTimes {
	s := bufio.NewScanner(r)
	if !s.Scan() {
		return nil
	}
	f := strings.Fields(s.Text())
	if len(f) < 9 || f[0] != "cpu" {
		return nil
	}
	var times cpuTimes
	for i := range times {
		value, err := strconv.ParseUint(f[i+1], 10, 64)
		if err != nil {
			return nil
		}
		times[i] = value
	}
	return &times
}

func parseMemory(r io.Reader) *envelope.ResourceUsage {
	var total, available uint64
	var haveTotal, haveAvailable bool
	s := bufio.NewScanner(r)
	for s.Scan() {
		f := strings.Fields(s.Text())
		if len(f) == 0 || (f[0] != "MemTotal:" && f[0] != "MemAvailable:") {
			continue
		}
		if len(f) != 3 || f[2] != "kB" {
			return nil
		}
		value, err := strconv.ParseUint(f[1], 10, 64)
		if err != nil || value > math.MaxUint64/1024 {
			return nil
		}
		switch f[0] {
		case "MemTotal:":
			if haveTotal {
				return nil
			}
			total, haveTotal = value*1024, true
		case "MemAvailable:":
			if haveAvailable {
				return nil
			}
			available, haveAvailable = value*1024, true
		}
	}
	if s.Err() != nil || !haveTotal || !haveAvailable || total == 0 || available > total {
		return nil
	}
	return &envelope.ResourceUsage{UsedBytes: total - available, TotalBytes: total}
}

func sampleDisk(native bool) *envelope.DiskUsage {
	// Merely creating the target directory (or setting a symlink) does not opt in.
	if f := openProcFile("/proc/self/mountinfo"); f != nil {
		mounted := hasReadOnlyMount(f, HostDiskMountPath)
		f.Close()
		if mounted {
			return readDisk(HostDiskMountPath, envelope.DiskScopeHostFilesystem, true)
		}
	}
	if !native || hasContainerMarker() {
		return nil
	}
	return readDisk("/", envelope.DiskScopeHostRoot, false)
}

func hasReadOnlyMount(r io.Reader, target string) bool {
	// target is an exact, fixed path with no mountinfo-escaped characters.
	s := bufio.NewScanner(r)
	found := false
	for s.Scan() {
		fields := strings.Fields(s.Text())
		if len(fields) < 10 || fields[4] != target {
			continue
		}
		if found || !strings.Contains(","+fields[5]+",", ",ro,") {
			return false
		}
		found = true
	}
	return found && s.Err() == nil
}

func hasContainerMarker() bool {
	if os.Getenv("container") != "" {
		return true
	}
	for _, path := range []string{"/.dockerenv", "/run/.containerenv", "/run/systemd/container"} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			return true
		}
	}
	return false
}

func readDisk(path, scope string, readOnly bool) *envelope.DiskUsage {
	// O_PATH needs search, but not read access to the directory. O_NOFOLLOW and
	// O_DIRECTORY together prevent sampling a final symlink in place of the mount.
	const oPath = 0x200000 // Linux O_PATH
	fd, err := syscall.Open(path, oPath|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil
	}
	defer syscall.Close(fd)
	var stat syscall.Statfs_t
	if err := syscall.Fstatfs(fd, &stat); err != nil || readOnly && stat.Flags&1 == 0 { // Linux ST_RDONLY
		return nil
	}
	return diskUsage(stat, scope)
}

func diskUsage(stat syscall.Statfs_t, scope string) *envelope.DiskUsage {
	// Reject container union layers, memory filesystems and pseudo filesystems.
	switch uint64(stat.Type) & 0xffffffff {
	case 0x794c7630, 0x61756673, 0x65735546, 0x01021994, 0x858458f6, 0x9fa0, 0x62656572:
		// overlay, aufs, FUSE (including fuse-overlayfs), tmpfs, ramfs, proc, sysfs
		return nil
	}
	blockSize := stat.Frsize
	if blockSize == 0 {
		blockSize = stat.Bsize
	}
	if blockSize <= 0 || stat.Blocks == 0 || stat.Bfree > stat.Blocks || stat.Blocks > math.MaxUint64/uint64(blockSize) {
		return nil
	}
	return &envelope.DiskUsage{
		Scope: scope,
		ResourceUsage: envelope.ResourceUsage{
			UsedBytes:  (stat.Blocks - stat.Bfree) * uint64(blockSize),
			TotalBytes: stat.Blocks * uint64(blockSize),
		},
	}
}
