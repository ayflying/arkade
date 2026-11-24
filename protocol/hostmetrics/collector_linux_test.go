package hostmetrics

import (
	"strings"
	"syscall"
	"testing"
	"time"

	"arkade/protocol/envelope"
)

func TestCPUDeltas(t *testing.T) {
	a := parseCPU(strings.NewReader("cpu  100 0 100 100 100 0 0 0 100 0\ncpu0 0 0 0 0"))
	b := parseCPU(strings.NewReader("cpu  110 0 120 130 110 0 0 0 110 0\n"))
	if a == nil || b == nil {
		t.Fatal("could not parse aggregate CPU counters")
	}
	if got := cpuPercent(*a, *b); got == nil || *got < 42.85 || *got > 42.86 {
		t.Fatalf("CPU delta = %v", got)
	}
	if cpuPercent(*a, *a) != nil || cpuPercent(*b, *a) != nil {
		t.Fatal("unchanged or reset counters produced a sample")
	}
	if parseCPU(strings.NewReader("cpu0 1 2 3 4 5 6 7 8\n")) != nil || parseCPU(strings.NewReader("cpu 1 2 3 invalid 5 6 7 8\n")) != nil {
		t.Fatal("invalid aggregate counter line was accepted")
	}
}

func TestMemory(t *testing.T) {
	if got := parseMemory(strings.NewReader("MemTotal: 100 kB\nMemFree: 5 kB\nMemAvailable: 75 kB\n")); got == nil || got.UsedBytes != 25*1024 || got.TotalBytes != 100*1024 {
		t.Fatalf("memory = %+v", got)
	}
	for _, input := range []string{
		"MemAvailable: 50 kB\n", "MemTotal: 100 kB\nMemFree: 2 kB\n",
		"MemTotal: 100 kB\nMemAvailable: 150 kB\n", "MemTotal: 0 kB\nMemAvailable: 0 kB\n",
		"MemTotal: 18446744073709551615 kB\nMemAvailable: 0 kB\n", "MemTotal: 1 B\nMemAvailable: 0 kB\n",
	} {
		if got := parseMemory(strings.NewReader(input)); got != nil {
			t.Errorf("invalid memory was accepted: %q -> %+v", input, got)
		}
	}
}

func TestDiskMountAndUsage(t *testing.T) {
	const line = "22 21 8:0 /directory /run/arkade/host-disk ro,nosuid - ext4 /dev/sda rw\n"
	for _, tc := range []struct {
		info string
		want bool
	}{
		{line, true}, {"", false}, {line + line, false},
		{strings.Replace(line, "ro,nosuid", "rw,nosuid", 1), false},
		{strings.Replace(line, HostDiskMountPath, HostDiskMountPath+"2", 1), false},
	} {
		if got := hasReadOnlyMount(strings.NewReader(tc.info), HostDiskMountPath); got != tc.want {
			t.Errorf("mountinfo %q: got %t, want %t", tc.info, got, tc.want)
		}
	}
	stat := syscall.Statfs_t{Type: 0xef53, Bsize: 4096, Frsize: 1024, Blocks: 100, Bfree: 35, Bavail: 30}
	got := diskUsage(stat, envelope.DiskScopeHostFilesystem)
	if got == nil || got.Scope != envelope.DiskScopeHostFilesystem || got.UsedBytes != 65*1024 || got.TotalBytes != 100*1024 {
		t.Fatalf("disk = %+v", got)
	}
	for _, excluded := range []syscall.Statfs_t{
		{Type: 0x794c7630, Frsize: 1024, Blocks: 100, Bfree: 35},
		{Type: 0x65735546, Frsize: 1024, Blocks: 100, Bfree: 35},
		{Type: 0x01021994, Frsize: 1024, Blocks: 100, Bfree: 35},
	} {
		if diskUsage(excluded, envelope.DiskScopeHostRoot) != nil {
			t.Errorf("pseudo filesystem %#x was accepted", excluded.Type)
		}
	}
	stat.Bfree = 101
	if diskUsage(stat, envelope.DiskScopeHostRoot) != nil {
		t.Fatal("out-of-range disk counters were accepted")
	}
}

func TestLinuxProcSampling(t *testing.T) {
	c := newCollector(false)
	if f := openProcFile("/proc/meminfo"); f != nil {
		f.Close()
		if got := c.readMemory(); got == nil || got.UsedBytes > got.TotalBytes {
			t.Fatalf("unexpected live procfs memory: %+v", got)
		}
	}
	if f := openProcFile("/proc/stat"); f != nil {
		f.Close()
		c.Sample()
		time.Sleep(100 * time.Millisecond)
		got := c.Sample()
		if got == nil || got.CPUPercent == nil || *got.CPUPercent < 0 || *got.CPUPercent > 100 {
			t.Fatalf("unexpected live procfs CPU: %+v", got)
		}
	}

	// 网络与磁盘 IO 是真实宿主上最容易「看起来正常、其实算错」的部分：
	// 首轮必须没有速率，长期运行后速率必须是非负有限值。
	live := newCollector(false)
	if first := live.Sample(); first != nil && (first.Network != nil || first.DiskIO != nil) {
		t.Fatalf("first sample must not report rates without a baseline: %+v", first)
	}
	time.Sleep(200 * time.Millisecond)
	if second := live.Sample(); second != nil {
		if second.Network != nil && second.Network.RxBytesPerSec > 1<<40 {
			t.Errorf("implausible network rate: %+v", second.Network)
		}
		if second.DiskIO != nil && second.DiskIO.ReadBytesPerSec > 1<<40 {
			t.Errorf("implausible disk IO rate: %+v", second.DiskIO)
		}
	}
}

func TestNetworkAndDiskRateParsing(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	const dev = "Inter-|   Receive                                                |  Transmit\n" +
		" face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed\n" +
		"    lo: 900 0 0 0 0 0 0 0 900 0 0 0 0 0 0 0\n" +
		"  eth0: 1000 0 0 0 0 0 0 0 500 0 0 0 0 0 0 0\n" +
		"  eth1: 200 0 0 0 0 0 0 0 100 0 0 0 0 0 0 0\n"
	first := parseNetDev(strings.NewReader(dev), now)
	// 回环不计入；两台网卡累加。
	if first == nil || first.a != 1200 || first.b != 600 {
		t.Fatalf("netdev = %+v", first)
	}
	if parseNetDev(strings.NewReader("Inter-|\n face |\n"), now) != nil {
		t.Fatal("headers without interfaces must not produce a zero rate")
	}
	if parseNetDev(strings.NewReader("  eth0: notanumber 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0\n"), now) != nil {
		t.Fatal("invalid counters were accepted")
	}

	const stats = "   8       0 sda 1 0 8 0 0 0 16 0 0 0 0\n" +
		"   8       1 sda1 1 0 999 0 0 0 999 0 0 0 0\n" + // 分区，不能重复计入
		" 253       0 dm-0 1 0 999 0 0 0 999 0 0 0 0\n" + // 叠层设备，不能重复计入
		"   7       0 loop0 1 0 999 0 0 0 999 0 0 0 0\n"
	disks := map[string]struct{}{"sda": {}, "dm-0": {}}
	io := parseDiskstats(strings.NewReader(stats), disks, now)
	// 扇区固定按 512 字节换算，与设备真实扇区大小无关。
	// sda 与 dm-0 各计一次，分区 sda1 与 loop0 被跳过。
	if io == nil || io.a != (8+999)*512 || io.b != (16+999)*512 {
		t.Fatalf("diskstats = %+v", io)
	}
	// 只统计传入集合里的设备：单要 loop0 时不能带上 sda 的量。
	if got := parseDiskstats(strings.NewReader(stats), map[string]struct{}{"loop0": {}}, now); got == nil || got.a != 999*512 {
		t.Fatalf("only requested devices should be counted: %+v", got)
	}
	if got := parseDiskstats(strings.NewReader(stats), map[string]struct{}{"sda1": {}}, now); got.a != 999*512 {
		t.Fatalf("partition counters should only count when explicitly requested: %+v", got)
	}
	if _, ok := physicalDisks()["loop0"]; ok {
		t.Error("loop devices leaked into physical disks")
	}
	if isVirtualBlockDevice("nvme0n1") || !isVirtualBlockDevice("dm-0") {
		t.Error("virtual device classification is wrong")
	}
}

func TestRatesIgnoreResetsAndLongGaps(t *testing.T) {
	start := time.Unix(1_800_000_000, 0)
	previous := counterPair{at: start, a: 1000, b: 2000}
	if a, b, ok := rate(previous, counterPair{at: start.Add(time.Second), a: 3000, b: 5000}); !ok || a != 2000 || b != 3000 {
		t.Fatalf("rate = %d/%d ok=%t", a, b, ok)
	}
	for name, current := range map[string]counterPair{
		"counter reset": {at: start.Add(time.Second), a: 10, b: 5000},
		"same instant":  {at: start, a: 3000, b: 5000},
		"clock back":    {at: start.Add(-time.Second), a: 3000, b: 5000},
		"long gap":      {at: start.Add(2 * maxRateInterval), a: 3000, b: 5000},
	} {
		if _, _, ok := rate(previous, current); ok {
			t.Errorf("%s produced a rate", name)
		}
	}
}

func TestCollectorOmitsMissingRates(t *testing.T) {
	// 只有一次网络/IO 读数时，Sample 不能把它们报成 0 B/s。
	c := &Collector{
		readNet: func(at time.Time) *counterPair { return &counterPair{at: at, a: 10, b: 20} },
		readDiskIO: func(at time.Time) *counterPair {
			return &counterPair{at: at, a: 30, b: 40}
		},
	}
	if got := c.Sample(); got != nil {
		t.Fatalf("first sample = %+v", got)
	}
	time.Sleep(time.Millisecond)
	got := c.Sample()
	if got == nil || got.Network == nil || got.DiskIO == nil {
		t.Fatalf("second sample = %+v", got)
	}
	if got.CPUPercent != nil || got.Memory != nil || got.Disk != nil {
		t.Fatalf("unsampled fields must stay nil: %+v", got)
	}
}
