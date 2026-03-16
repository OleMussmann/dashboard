package collector

import (
	"math"
	"strings"
	"testing"
)

// testMetrics is a trimmed-down version of real Incus /1.0/metrics output,
// containing exactly the metrics needed to exercise all parsing paths.
const testMetrics = `
# HELP incus_boot_time_seconds The unix epoch at the time of the instance start.
# TYPE incus_boot_time_seconds counter
incus_boot_time_seconds{name="homepage",project="default",type="container"} 1773643523
incus_boot_time_seconds{name="dashboard-api",project="default",type="container"} 1773643521
incus_boot_time_seconds{name="Home-Assistant-OS",project="default",type="virtual-machine"} 1773485340

# HELP incus_time_seconds The current unix epoch.
# TYPE incus_time_seconds counter
incus_time_seconds{name="homepage",project="default",type="container"} 1773651401
incus_time_seconds{name="dashboard-api",project="default",type="container"} 1773651401
incus_time_seconds{name="Home-Assistant-OS",project="default",type="virtual-machine"} 1773651401

# HELP incus_cpu_seconds_total The total number of CPU time used in seconds.
# TYPE incus_cpu_seconds_total counter
incus_cpu_seconds_total{cpu="0",mode="system",name="homepage",project="default",type="container"} 3.248833
incus_cpu_seconds_total{cpu="0",mode="user",name="homepage",project="default",type="container"} 18.213536
incus_cpu_seconds_total{cpu="0",mode="system",name="dashboard-api",project="default",type="container"} 0.530103
incus_cpu_seconds_total{cpu="0",mode="user",name="dashboard-api",project="default",type="container"} 2.583233
incus_cpu_seconds_total{cpu="0",mode="idle",name="Home-Assistant-OS",project="default",type="virtual-machine"} 160645.95
incus_cpu_seconds_total{cpu="0",mode="iowait",name="Home-Assistant-OS",project="default",type="virtual-machine"} 34.56
incus_cpu_seconds_total{cpu="0",mode="system",name="Home-Assistant-OS",project="default",type="virtual-machine"} 936.42
incus_cpu_seconds_total{cpu="0",mode="user",name="Home-Assistant-OS",project="default",type="virtual-machine"} 2911

# HELP incus_memory_MemTotal_bytes The amount of used memory.
# TYPE incus_memory_MemTotal_bytes gauge
incus_memory_MemTotal_bytes{name="homepage",project="default",type="container"} 16154060000
incus_memory_MemTotal_bytes{name="dashboard-api",project="default",type="container"} 16154060000
incus_memory_MemTotal_bytes{name="Home-Assistant-OS",project="default",type="virtual-machine"} 6148096000

# HELP incus_memory_MemAvailable_bytes The amount of available memory.
# TYPE incus_memory_MemAvailable_bytes gauge
incus_memory_MemAvailable_bytes{name="homepage",project="default",type="container"} 16048940256
incus_memory_MemAvailable_bytes{name="dashboard-api",project="default",type="container"} 16145655008
incus_memory_MemAvailable_bytes{name="Home-Assistant-OS",project="default",type="virtual-machine"} 4948766720

# HELP incus_memory_Swap_bytes The amount of used swap memory.
# TYPE incus_memory_Swap_bytes gauge
incus_memory_Swap_bytes{name="homepage",project="default",type="container"} 0
incus_memory_Swap_bytes{name="dashboard-api",project="default",type="container"} 0
incus_memory_Swap_bytes{name="Home-Assistant-OS",project="default",type="virtual-machine"} 1048576

# HELP incus_memory_OOM_kills_total The number of out of memory kills.
# TYPE incus_memory_OOM_kills_total counter
incus_memory_OOM_kills_total{name="homepage",project="default",type="container"} 0
incus_memory_OOM_kills_total{name="dashboard-api",project="default",type="container"} 0
incus_memory_OOM_kills_total{name="Home-Assistant-OS",project="default",type="virtual-machine"} 2

# HELP incus_procs_total The number of running processes.
# TYPE incus_procs_total gauge
incus_procs_total{name="homepage",project="default",type="container"} 12
incus_procs_total{name="dashboard-api",project="default",type="container"} 11
incus_procs_total{name="Home-Assistant-OS",project="default",type="virtual-machine"} 1

# HELP incus_filesystem_size_bytes The size of the filesystem in bytes.
# TYPE incus_filesystem_size_bytes gauge
incus_filesystem_size_bytes{device="local/incus/containers/homepage",fstype="zfs",mountpoint="/",name="homepage",project="default",type="container"} 446529536000
incus_filesystem_size_bytes{device="local/incus/containers/dashboard-api",fstype="zfs",mountpoint="/",name="dashboard-api",project="default",type="container"} 446345773056
incus_filesystem_size_bytes{device="/dev/root",fstype="0x-1f0a1e1e",mountpoint="/",name="Home-Assistant-OS",project="default",type="virtual-machine"} 237359104
incus_filesystem_size_bytes{device="/dev/sda8",fstype="ext4",mountpoint="/mnt/data",name="Home-Assistant-OS",project="default",type="virtual-machine"} 33058951168
incus_filesystem_size_bytes{device="tmpfs",fstype="tmpfs",mountpoint="/run",name="Home-Assistant-OS",project="default",type="virtual-machine"} 1229619200

# HELP incus_filesystem_avail_bytes The number of available space in bytes.
# TYPE incus_filesystem_avail_bytes gauge
incus_filesystem_avail_bytes{device="local/incus/containers/homepage",fstype="zfs",mountpoint="/",name="homepage",project="default",type="container"} 446338695168
incus_filesystem_avail_bytes{device="local/incus/containers/dashboard-api",fstype="zfs",mountpoint="/",name="dashboard-api",project="default",type="container"} 446338695168
incus_filesystem_avail_bytes{device="/dev/root",fstype="0x-1f0a1e1e",mountpoint="/",name="Home-Assistant-OS",project="default",type="virtual-machine"} 0
incus_filesystem_avail_bytes{device="/dev/sda8",fstype="ext4",mountpoint="/mnt/data",name="Home-Assistant-OS",project="default",type="virtual-machine"} 22596567040
incus_filesystem_avail_bytes{device="tmpfs",fstype="tmpfs",mountpoint="/run",name="Home-Assistant-OS",project="default",type="virtual-machine"} 1228308480

# HELP incus_network_receive_bytes_total The amount of received bytes on a given interface.
# TYPE incus_network_receive_bytes_total counter
incus_network_receive_bytes_total{device="lo",name="homepage",project="default",type="container"} 24981878
incus_network_receive_bytes_total{device="eth0",name="homepage",project="default",type="container"} 98778454
incus_network_receive_bytes_total{device="lo",name="dashboard-api",project="default",type="container"} 5000000
incus_network_receive_bytes_total{device="eth0",name="dashboard-api",project="default",type="container"} 12345678

# HELP incus_network_transmit_bytes_total The amount of transmitted bytes on a given interface.
# TYPE incus_network_transmit_bytes_total counter
incus_network_transmit_bytes_total{device="lo",name="homepage",project="default",type="container"} 24981878
incus_network_transmit_bytes_total{device="eth0",name="homepage",project="default",type="container"} 1558651
incus_network_transmit_bytes_total{device="lo",name="dashboard-api",project="default",type="container"} 5000000
incus_network_transmit_bytes_total{device="eth0",name="dashboard-api",project="default",type="container"} 654321

# HELP node_boot_time_seconds Node boot time, in unixtime.
# TYPE node_boot_time_seconds gauge
node_boot_time_seconds 1773485307

# HELP node_time_seconds System time in seconds since epoch (1970).
# TYPE node_time_seconds gauge
node_time_seconds 1773651401.20133

# HELP node_load1 1m load average.
# TYPE node_load1 gauge
node_load1 0.1

# HELP node_memory_MemTotal_bytes Memory information field MemTotal_bytes.
# TYPE node_memory_MemTotal_bytes gauge
node_memory_MemTotal_bytes 16541757440

# HELP node_memory_MemAvailable_bytes Memory information field MemAvailable_bytes.
# TYPE node_memory_MemAvailable_bytes gauge
node_memory_MemAvailable_bytes 11732811776

# HELP node_filesystem_size_bytes Filesystem size in bytes.
# TYPE node_filesystem_size_bytes gauge
node_filesystem_size_bytes{device="/dev/mapper/root",device_error="",fstype="ext4",mountpoint="/"} 26225119232
node_filesystem_size_bytes{device="local",device_error="",fstype="zfs",mountpoint="/var/lib/incus/storage-pools/local"} 480000000000
node_filesystem_size_bytes{device="tmpfs",device_error="",fstype="tmpfs",mountpoint="/run"} 8270876672

# HELP node_filesystem_avail_bytes Filesystem space available to non-root users in bytes.
# TYPE node_filesystem_avail_bytes gauge
node_filesystem_avail_bytes{device="/dev/mapper/root",device_error="",fstype="ext4",mountpoint="/"} 23281291264
node_filesystem_avail_bytes{device="local",device_error="",fstype="zfs",mountpoint="/var/lib/incus/storage-pools/local"} 446338695168
node_filesystem_avail_bytes{device="tmpfs",device_error="",fstype="tmpfs",mountpoint="/run"} 8270700544

# HELP node_hwmon_temp_celsius Hardware monitor for temperature (input)
# TYPE node_hwmon_temp_celsius gauge
node_hwmon_temp_celsius{chip="platform_coretemp_0",sensor="temp1"} 56
node_hwmon_temp_celsius{chip="platform_coretemp_0",sensor="temp2"} 54

# HELP node_zfs_zpool_state kstat.zfs.misc.state
# TYPE node_zfs_zpool_state gauge
node_zfs_zpool_state{state="degraded",zpool="local"} 0
node_zfs_zpool_state{state="faulted",zpool="local"} 0
node_zfs_zpool_state{state="offline",zpool="local"} 0
node_zfs_zpool_state{state="online",zpool="local"} 1
node_zfs_zpool_state{state="removed",zpool="local"} 0
node_zfs_zpool_state{state="suspended",zpool="local"} 0
node_zfs_zpool_state{state="unavail",zpool="local"} 0

# HELP node_vmstat_oom_kill /proc/vmstat information field oom_kill.
# TYPE node_vmstat_oom_kill untyped
node_vmstat_oom_kill 0
`

func mustParse(t *testing.T) *IncusMetrics {
	t.Helper()
	result, err := parseIncusMetrics(strings.NewReader(testMetrics))
	if err != nil {
		t.Fatalf("parseIncusMetrics failed: %v", err)
	}
	return result
}

func TestInstanceDiscovery(t *testing.T) {
	m := mustParse(t)

	if len(m.Instances) != 3 {
		t.Fatalf("expected 3 instances, got %d", len(m.Instances))
	}

	tests := []struct {
		name       string
		wantStatus string
		wantType   string
	}{
		{"homepage", "Running", "container"},
		{"dashboard-api", "Running", "container"},
		{"Home-Assistant-OS", "Running", "virtual-machine"},
	}

	for _, tc := range tests {
		inst, ok := m.Instances[tc.name]
		if !ok {
			t.Errorf("instance %q not found", tc.name)
			continue
		}
		if inst.Status != tc.wantStatus {
			t.Errorf("%s: status = %q, want %q", tc.name, inst.Status, tc.wantStatus)
		}
		if inst.Type != tc.wantType {
			t.Errorf("%s: type = %q, want %q", tc.name, inst.Type, tc.wantType)
		}
	}
}

func TestInstanceUptime(t *testing.T) {
	m := mustParse(t)

	// homepage: 1773651401 - 1773643523 = 7878s = 2.1883h ≈ 2.2h
	hp := m.Instances["homepage"]
	if hp.UptimeHours < 2.1 || hp.UptimeHours > 2.3 {
		t.Errorf("homepage uptime = %.1f, want ~2.2", hp.UptimeHours)
	}

	// Home-Assistant-OS: 1773651401 - 1773485340 = 166061s = 46.1h
	ha := m.Instances["Home-Assistant-OS"]
	if ha.UptimeHours < 46.0 || ha.UptimeHours > 46.2 {
		t.Errorf("Home-Assistant-OS uptime = %.1f, want ~46.1", ha.UptimeHours)
	}
}

func TestCPUSecondsFiltersModes(t *testing.T) {
	m := mustParse(t)

	// homepage: system(3.248833) + user(18.213536) = 21.462369 ≈ 21.5
	hp := m.Instances["homepage"]
	if hp.CPUSeconds < 21.4 || hp.CPUSeconds > 21.5 {
		t.Errorf("homepage CPU = %.1f, want ~21.5", hp.CPUSeconds)
	}

	// Home-Assistant-OS: system(936.42) + user(2911) = 3847.42
	// Must NOT include idle(160645.95) or iowait(34.56).
	ha := m.Instances["Home-Assistant-OS"]
	if ha.CPUSeconds < 3847.0 || ha.CPUSeconds > 3848.0 {
		t.Errorf("Home-Assistant-OS CPU = %.1f, want ~3847.4 (should exclude idle/iowait)", ha.CPUSeconds)
	}

	// Verify idle was excluded: if it were included, the value would be >164000.
	if ha.CPUSeconds > 10000 {
		t.Errorf("Home-Assistant-OS CPU = %.1f, idle time was NOT excluded", ha.CPUSeconds)
	}
}

func TestMemoryUsedNotTotal(t *testing.T) {
	m := mustParse(t)

	// homepage: Total=16154060000, Available=16048940256
	// Used = 16154060000 - 16048940256 = 105119744
	hp := m.Instances["homepage"]
	if hp.MemoryUsedBytes != 105119744 {
		t.Errorf("homepage memory used = %d, want 105119744", hp.MemoryUsedBytes)
	}
	if hp.MemoryTotalBytes != 16154060000 {
		t.Errorf("homepage memory total = %d, want 16154060000", hp.MemoryTotalBytes)
	}

	// Home-Assistant-OS: Total=6148096000, Available=4948766720
	// Used = 6148096000 - 4948766720 = 1199329280
	ha := m.Instances["Home-Assistant-OS"]
	if ha.MemoryUsedBytes != 1199329280 {
		t.Errorf("HA-OS memory used = %d, want 1199329280", ha.MemoryUsedBytes)
	}
}

func TestFilesystemUsage(t *testing.T) {
	m := mustParse(t)

	// homepage (ZFS container): root mount "/" on ZFS
	// size=446529536000, avail=446338695168, used=190840832
	hp := m.Instances["homepage"]
	if hp.DiskTotalBytes != 446529536000 {
		t.Errorf("homepage disk total = %d, want 446529536000", hp.DiskTotalBytes)
	}
	expectedUsed := int64(446529536000 - 446338695168)
	if hp.DiskUsedBytes != expectedUsed {
		t.Errorf("homepage disk used = %d, want %d", hp.DiskUsedBytes, expectedUsed)
	}

	// Home-Assistant-OS: has root "/" (fstype 0x-1f0a1e1e, size=237359104, avail=0)
	// and /mnt/data (ext4, size=33058951168, avail=22596567040), plus tmpfs.
	// Root is preferred since mount="/". This is HA-OS's read-only squashfs root
	// which is always 100% full by design — not ideal, but the parser correctly
	// prefers "/" over other mounts.
	ha := m.Instances["Home-Assistant-OS"]
	if ha.DiskTotalBytes != 237359104 {
		t.Errorf("HA-OS disk total = %d, want 237359104 (root mount preferred)", ha.DiskTotalBytes)
	}
}

func TestNetworkExcludesLoopback(t *testing.T) {
	m := mustParse(t)

	// homepage: eth0 only = rx:98778454, tx:1558651
	// Should NOT include lo (24981878 each).
	hp := m.Instances["homepage"]
	if hp.NetworkRxBytes != 98778454 {
		t.Errorf("homepage net rx = %d, want 98778454 (lo should be excluded)", hp.NetworkRxBytes)
	}
	if hp.NetworkTxBytes != 1558651 {
		t.Errorf("homepage net tx = %d, want 1558651 (lo should be excluded)", hp.NetworkTxBytes)
	}

	// dashboard-api: eth0 only = rx:12345678, tx:654321
	da := m.Instances["dashboard-api"]
	if da.NetworkRxBytes != 12345678 {
		t.Errorf("dashboard-api net rx = %d, want 12345678", da.NetworkRxBytes)
	}
	if da.NetworkTxBytes != 654321 {
		t.Errorf("dashboard-api net tx = %d, want 654321", da.NetworkTxBytes)
	}
}

func TestProcessCount(t *testing.T) {
	m := mustParse(t)

	if m.Instances["homepage"].Processes != 12 {
		t.Errorf("homepage procs = %d, want 12", m.Instances["homepage"].Processes)
	}
	if m.Instances["dashboard-api"].Processes != 11 {
		t.Errorf("dashboard-api procs = %d, want 11", m.Instances["dashboard-api"].Processes)
	}
}

func TestOOMKills(t *testing.T) {
	m := mustParse(t)

	if m.Instances["homepage"].OOMKills != 0 {
		t.Errorf("homepage OOM = %d, want 0", m.Instances["homepage"].OOMKills)
	}
	if m.Instances["Home-Assistant-OS"].OOMKills != 2 {
		t.Errorf("HA-OS OOM = %d, want 2", m.Instances["Home-Assistant-OS"].OOMKills)
	}
}

func TestSwapBytes(t *testing.T) {
	m := mustParse(t)

	if m.Instances["homepage"].SwapBytes != 0 {
		t.Errorf("homepage swap = %d, want 0", m.Instances["homepage"].SwapBytes)
	}
	if m.Instances["Home-Assistant-OS"].SwapBytes != 1048576 {
		t.Errorf("HA-OS swap = %d, want 1048576", m.Instances["Home-Assistant-OS"].SwapBytes)
	}
}

func TestHostUptime(t *testing.T) {
	m := mustParse(t)

	// node_time_seconds(1773651401.20133) - node_boot_time_seconds(1773485307)
	// = 166094.20133s = 46.1h
	if m.Host.UptimeHours < 46.0 || m.Host.UptimeHours > 46.2 {
		t.Errorf("host uptime = %.1f, want ~46.1", m.Host.UptimeHours)
	}
}

func TestHostCPULoad(t *testing.T) {
	m := mustParse(t)

	if m.Host.CPULoad1m != 0.1 {
		t.Errorf("host load = %f, want 0.1", m.Host.CPULoad1m)
	}
}

func TestHostMemory(t *testing.T) {
	m := mustParse(t)

	if m.Host.MemoryTotalBytes != 16541757440 {
		t.Errorf("host mem total = %d, want 16541757440", m.Host.MemoryTotalBytes)
	}

	// Used% = (16541757440 - 11732811776) / 16541757440 * 100 = 29.07...%
	if m.Host.MemoryUsedPercent < 29.0 || m.Host.MemoryUsedPercent > 29.2 {
		t.Errorf("host mem used%% = %.1f, want ~29.1", m.Host.MemoryUsedPercent)
	}
}

func TestHostDiskZFS(t *testing.T) {
	m := mustParse(t)

	// The ZFS pool (size=480000000000, avail=446338695168) should be picked
	// over the ext4 root and tmpfs.
	// used% = (480000000000 - 446338695168) / 480000000000 * 100 = 7.01%
	if m.Host.DiskUsedPercent < 6.9 || m.Host.DiskUsedPercent > 7.1 {
		t.Errorf("host disk used%% = %.1f, want ~7.0", m.Host.DiskUsedPercent)
	}
}

func TestHostTemperatures(t *testing.T) {
	m := mustParse(t)

	if len(m.Host.Temperatures) != 2 {
		t.Errorf("host temps count = %d, want 2", len(m.Host.Temperatures))
	}

	if v, ok := m.Host.Temperatures["platform_coretemp_0_temp1"]; !ok || v != 56 {
		t.Errorf("host temp1 = %v/%v, want 56/true", v, ok)
	}
	if v, ok := m.Host.Temperatures["platform_coretemp_0_temp2"]; !ok || v != 54 {
		t.Errorf("host temp2 = %v/%v, want 54/true", v, ok)
	}
}

func TestHostZFSPoolHealthy(t *testing.T) {
	m := mustParse(t)

	if !m.Host.ZFSPoolHealthy {
		t.Error("host ZFS pool should be healthy")
	}
	if m.Host.ZFSPoolState != "online" {
		t.Errorf("host ZFS state = %q, want 'online'", m.Host.ZFSPoolState)
	}
}

func TestHostZFSPoolDegraded(t *testing.T) {
	degradedMetrics := `
# HELP node_zfs_zpool_state kstat.zfs.misc.state
# TYPE node_zfs_zpool_state gauge
node_zfs_zpool_state{state="degraded",zpool="local"} 1
node_zfs_zpool_state{state="faulted",zpool="local"} 0
node_zfs_zpool_state{state="online",zpool="local"} 0
`
	result, err := parseIncusMetrics(strings.NewReader(degradedMetrics))
	if err != nil {
		t.Fatalf("parseIncusMetrics failed: %v", err)
	}
	if result.Host.ZFSPoolHealthy {
		t.Error("host ZFS pool should NOT be healthy when degraded")
	}
	if result.Host.ZFSPoolState != "degraded" {
		t.Errorf("host ZFS state = %q, want 'degraded'", result.Host.ZFSPoolState)
	}
}

func TestHostOOMKills(t *testing.T) {
	m := mustParse(t)

	if m.Host.OOMKills != 0 {
		t.Errorf("host OOM = %d, want 0", m.Host.OOMKills)
	}
}

func TestEmptyInput(t *testing.T) {
	result, err := parseIncusMetrics(strings.NewReader(""))
	if err != nil {
		t.Fatalf("parseIncusMetrics on empty input failed: %v", err)
	}
	if len(result.Instances) != 0 {
		t.Errorf("expected 0 instances, got %d", len(result.Instances))
	}
	if result.Host == nil {
		t.Fatal("host should not be nil")
	}
}

// approxEqual checks if two floats are within tolerance.
func approxEqual(a, b, tolerance float64) bool {
	return math.Abs(a-b) <= tolerance
}

func TestFilesystemVMPicksRoot(t *testing.T) {
	// Test that for a VM with both "/" and "/mnt/data", the root is preferred
	// even when it's smaller.
	metrics := `
# HELP incus_boot_time_seconds The unix epoch at the time of the instance start.
# TYPE incus_boot_time_seconds counter
incus_boot_time_seconds{name="myvm",project="default",type="virtual-machine"} 1773485340

# HELP incus_filesystem_size_bytes The size of the filesystem in bytes.
# TYPE incus_filesystem_size_bytes gauge
incus_filesystem_size_bytes{device="/dev/sda1",fstype="ext4",mountpoint="/",name="myvm",project="default",type="virtual-machine"} 10000000000
incus_filesystem_size_bytes{device="/dev/sda2",fstype="ext4",mountpoint="/data",name="myvm",project="default",type="virtual-machine"} 500000000000

# HELP incus_filesystem_avail_bytes The number of available space in bytes.
# TYPE incus_filesystem_avail_bytes gauge
incus_filesystem_avail_bytes{device="/dev/sda1",fstype="ext4",mountpoint="/",name="myvm",project="default",type="virtual-machine"} 5000000000
incus_filesystem_avail_bytes{device="/dev/sda2",fstype="ext4",mountpoint="/data",name="myvm",project="default",type="virtual-machine"} 400000000000
`
	result, err := parseIncusMetrics(strings.NewReader(metrics))
	if err != nil {
		t.Fatalf("parseIncusMetrics failed: %v", err)
	}

	inst := result.Instances["myvm"]
	if inst.DiskTotalBytes != 10000000000 {
		t.Errorf("VM disk total = %d, want 10000000000 (root should be preferred)", inst.DiskTotalBytes)
	}
}
