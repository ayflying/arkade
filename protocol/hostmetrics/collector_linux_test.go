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
}
