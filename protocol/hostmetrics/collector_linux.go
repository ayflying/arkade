package hostmetrics

import (
	"bufio"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

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
		readNet: func(at time.Time) *counterPair {
			f := openProcFile("/proc/net/dev")
			if f == nil {
				return nil
			}
			defer f.Close()
			return parseNetDev(f, at)
		},
		readDiskIO: func(at time.Time) *counterPair {
			// 先确定要统计的整块设备；读不到 /sys/block 时不给数据，而不是猜。
			disks := physicalDisks()
			if len(disks) == 0 {
				return nil
			}
			f := openProcFile("/proc/diskstats")
			if f == nil {
				return nil
			}
			defer f.Close()
			return parseDiskstats(f, disks, at)
		},
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

// parseNetDev 累加所有非回环网卡的收发字节累计值。
//
// /proc/net/dev 前两行是表头，之后每行形如
//
//	eth0: 12345 67 0 0 0 0 0 0 8910 11 0 0 0 0 0 0
//
// 冒号前是网卡名，冒号后第 1 个与第 9 个字段分别是累计收/发字节。
// 这里只取累计值，速率由 Collector 对两次采样求差。
func parseNetDev(r io.Reader, at time.Time) *counterPair {
	var rx, tx uint64
	found := false
	s := bufio.NewScanner(r)
	for s.Scan() {
		name, rest, ok := strings.Cut(strings.TrimSpace(s.Text()), ":")
		if !ok {
			continue // 表头行没有冒号
		}
		if name = strings.TrimSpace(name); name == "" || name == "lo" {
			continue // 回环流量不是「这台机器对外收发」的量
		}
		fields := strings.Fields(rest)
		if len(fields) < 9 {
			return nil
		}
		received, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			return nil
		}
		sent, err := strconv.ParseUint(fields[8], 10, 64)
		if err != nil {
			return nil
		}
		if math.MaxUint64-rx < received || math.MaxUint64-tx < sent {
			return nil
		}
		rx += received
		tx += sent
		found = true
	}
	if s.Err() != nil || !found {
		return nil
	}
	return &counterPair{at: at, a: rx, b: tx}
}

// procDiskstatsSectorBytes 是 /proc/diskstats 扇区计数的固定换算单位。
//
// Linux 文档规定该计数始终按 512 字节计，与设备真实扇区大小无关；
// 用设备自身的扇区大小去换算会得到偏大的读写量。
const procDiskstatsSectorBytes = 512

// parseDiskstats 累加 disks 集合内整块设备的读写字节累计值。
//
// 每行形如：
//
//	8       0 sda 1234 0 5678 90 2345 0 6789 100 0 0 0 0 0 0 0 0 0 0
//
// 第 3 个字段是设备名，第 6、10 个字段分别是已读/已写扇区数。
func parseDiskstats(r io.Reader, disks map[string]struct{}, at time.Time) *counterPair {
	var read, written uint64
	s := bufio.NewScanner(r)
	for s.Scan() {
		fields := strings.Fields(s.Text())
		if len(fields) < 10 {
			continue
		}
		if _, ok := disks[fields[2]]; !ok {
			continue
		}
		sectorsRead, err := strconv.ParseUint(fields[5], 10, 64)
		if err != nil {
			return nil
		}
		sectorsWritten, err := strconv.ParseUint(fields[9], 10, 64)
		if err != nil {
			return nil
		}
		if sectorsRead > math.MaxUint64/procDiskstatsSectorBytes || sectorsWritten > math.MaxUint64/procDiskstatsSectorBytes {
			return nil
		}
		readBytes := sectorsRead * procDiskstatsSectorBytes
		writtenBytes := sectorsWritten * procDiskstatsSectorBytes
		if math.MaxUint64-read < readBytes || math.MaxUint64-written < writtenBytes {
			return nil
		}
		read += readBytes
		written += writtenBytes
	}
	if s.Err() != nil {
		return nil
	}
	return &counterPair{at: at, a: read, b: written}
}

// virtualBlockPrefixes 是虚拟或可叠加的块设备名前缀。
//
// 它们要么没有真实介质（loop、ram、zram、nbd），要么与底层物理盘重复计数
// （dm-*、md* 建在物理盘之上），因此不计入磁盘 IO。
var virtualBlockPrefixes = []string{"loop", "ram", "fd", "sr", "dm-", "md", "zram", "nbd"}

func isVirtualBlockDevice(name string) bool {
	for _, prefix := range virtualBlockPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// physicalDisks 返回 /sys/block 下的整块设备名。
//
// 用 /sys/block 而不是解析设备名：分区（sda1、nvme0n1p1）不会出现在这里，
// 而名字带数字的整盘（nvme0n1、mmcblk0）会被「以数字结尾即分区」的启发式误伤。
// 读不到 /sys/block 时返回空集合，宁可没有磁盘 IO，也不给猜测出来的数。
func physicalDisks() map[string]struct{} {
	entries, err := os.ReadDir("/sys/block")
	if err != nil {
		return nil
	}
	disks := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if !isVirtualBlockDevice(name) {
			disks[name] = struct{}{}
		}
	}
	return disks
}
