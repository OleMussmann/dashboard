package collector

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"math"
	"net/http"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

// IncusInstance holds translated metrics for a single Incus instance.
type IncusInstance struct {
	Type             string  `json:"type"`               // "container" or "virtual-machine"
	Status           string  `json:"status"`             // "Running", "Stopped", etc.
	UptimeHours      float64 `json:"uptime_hours"`       // hours since boot
	MemoryUsedBytes  int64   `json:"memory_used_bytes"`  // MemTotal - MemAvailable
	MemoryTotalBytes int64   `json:"memory_total_bytes"` // MemTotal
	DiskUsedBytes    int64   `json:"disk_used_bytes"`    // filesystem used
	DiskTotalBytes   int64   `json:"disk_total_bytes"`   // filesystem size
	CPUSeconds       float64 `json:"cpu_seconds"`        // user + system only
	Processes        int64   `json:"processes"`
	OOMKills         int64   `json:"oom_kills"`
	SwapBytes        int64   `json:"swap_bytes"`
	NetworkRxBytes   int64   `json:"network_rx_bytes"` // excludes loopback
	NetworkTxBytes   int64   `json:"network_tx_bytes"` // excludes loopback
}

// IncusHost holds translated host-level metrics from the IncusOS node_* metrics.
type IncusHost struct {
	UptimeHours       float64            `json:"uptime_hours"`
	CPULoad1m         float64            `json:"cpu_load_1m"`
	MemoryUsedPercent float64            `json:"memory_used_percent"`
	MemoryTotalBytes  int64              `json:"memory_total_bytes"`
	DiskUsedPercent   float64            `json:"disk_used_percent"`
	Temperatures      map[string]float64 `json:"temperatures"`
	ZFSPoolHealthy    bool               `json:"zfs_pool_healthy"`
	ZFSPoolState      string             `json:"zfs_pool_state"`
	OOMKills          int64              `json:"oom_kills"`
}

// IncusMetrics holds the complete translated Incus metrics.
type IncusMetrics struct {
	Instances map[string]*IncusInstance `json:"instances"`
	Host      *IncusHost               `json:"host"`
}

// NewIncusTLSClient creates an HTTP client configured with a metrics-only TLS
// certificate for the Incus endpoint.
func NewIncusTLSClient(certFile, keyFile string) (*http.Client, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load incus TLS cert: %w", err)
	}
	tlsCfg := &tls.Config{
		Certificates:       []tls.Certificate{cert},
		InsecureSkipVerify: true, // Incus uses self-signed certs
	}
	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: tlsCfg,
		},
	}, nil
}

// ScrapeIncus fetches and parses metrics from the Incus metrics endpoint.
func ScrapeIncus(ctx context.Context, client *http.Client, url string) (*IncusMetrics, error) {
	metricsURL := url + "/1.0/metrics"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metricsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("scrape incus: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("incus metrics returned status %d", resp.StatusCode)
	}

	return parseIncusMetrics(resp.Body)
}

func parseIncusMetrics(r io.Reader) (*IncusMetrics, error) {
	parser := expfmt.NewTextParser(model.LegacyValidation)
	families, err := parser.TextToMetricFamilies(r)
	if err != nil {
		return nil, fmt.Errorf("parse incus metrics: %w", err)
	}

	result := &IncusMetrics{
		Instances: make(map[string]*IncusInstance),
		Host: &IncusHost{
			Temperatures: make(map[string]float64),
		},
	}

	// --- Per-instance metrics ---

	// Extract instance info (names, type).
	// Since incus_instance_info was removed in recent Incus versions,
	// we extract running instances from incus_boot_time_seconds.
	if fam, ok := families["incus_boot_time_seconds"]; ok {
		for _, m := range fam.GetMetric() {
			name := labelValue(m, "name")
			if name == "" {
				continue
			}
			result.Instances[name] = &IncusInstance{
				Status: "Running",
				Type:   labelValue(m, "type"),
			}
		}
	}

	// Uptime: incus_time_seconds - incus_boot_time_seconds per instance.
	instanceTimes := instanceGaugeOrCounter(families, "incus_time_seconds")
	instanceBoots := instanceGaugeOrCounter(families, "incus_boot_time_seconds")
	for name, inst := range result.Instances {
		now, haveNow := instanceTimes[name]
		boot, haveBoot := instanceBoots[name]
		if haveNow && haveBoot && boot > 0 {
			hours := (now - boot) / 3600
			inst.UptimeHours = math.Round(hours*10) / 10
		}
	}

	// CPU seconds: sum only user + system modes per instance.
	parseCPUSeconds(families, result.Instances)

	// Memory used: MemTotal - MemAvailable.
	memTotal := instanceGaugeOrCounter(families, "incus_memory_MemTotal_bytes")
	memAvail := instanceGaugeOrCounter(families, "incus_memory_MemAvailable_bytes")
	for name, inst := range result.Instances {
		if total, ok := memTotal[name]; ok {
			inst.MemoryTotalBytes = int64(total)
			if avail, ok := memAvail[name]; ok {
				used := total - avail
				if used < 0 {
					used = 0
				}
				inst.MemoryUsedBytes = int64(used)
			}
		}
	}

	// Filesystem usage: pick root mountpoint per instance.
	parseFilesystemUsage(families, result.Instances)

	// Process count.
	procs := instanceGaugeOrCounter(families, "incus_procs_total")
	for name, inst := range result.Instances {
		if v, ok := procs[name]; ok {
			inst.Processes = int64(v)
		}
	}

	// OOM kills.
	oomKills := instanceGaugeOrCounter(families, "incus_memory_OOM_kills_total")
	for name, inst := range result.Instances {
		if v, ok := oomKills[name]; ok {
			inst.OOMKills = int64(v)
		}
	}

	// Swap usage.
	swapBytes := instanceGaugeOrCounter(families, "incus_memory_Swap_bytes")
	for name, inst := range result.Instances {
		if v, ok := swapBytes[name]; ok {
			inst.SwapBytes = int64(v)
		}
	}

	// Network RX/TX: exclude loopback.
	parseNetworkBytes(families, result.Instances)

	// --- Host-level metrics (node_* from IncusOS) ---
	parseHostMetrics(families, result.Host)

	return result, nil
}

// instanceGaugeOrCounter extracts a single value per instance name from a
// metric family. For metrics with multiple samples per instance (e.g.
// multiple CPUs), this returns the last value seen — use dedicated parsers
// for metrics that need aggregation.
func instanceGaugeOrCounter(families map[string]*dto.MetricFamily, metricName string) map[string]float64 {
	fam, ok := families[metricName]
	if !ok {
		return nil
	}
	result := make(map[string]float64)
	for _, m := range fam.GetMetric() {
		name := labelValue(m, "name")
		if name == "" {
			continue
		}
		result[name] = metricValue(m)
	}
	return result
}

// parseCPUSeconds sums incus_cpu_seconds_total per instance, counting only
// "user" and "system" modes. This avoids including "idle" time which is
// reported for VMs and would make the number meaningless.
func parseCPUSeconds(families map[string]*dto.MetricFamily, instances map[string]*IncusInstance) {
	fam, ok := families["incus_cpu_seconds_total"]
	if !ok {
		return
	}
	// Accumulate per instance.
	totals := make(map[string]float64)
	for _, m := range fam.GetMetric() {
		mode := labelValue(m, "mode")
		if mode != "user" && mode != "system" {
			continue
		}
		name := labelValue(m, "name")
		if name == "" {
			continue
		}
		totals[name] += metricValue(m)
	}
	for name, total := range totals {
		inst, ok := instances[name]
		if !ok {
			continue
		}
		inst.CPUSeconds = math.Round(total*10) / 10
	}
}

// parseFilesystemUsage computes disk used/total per instance from
// incus_filesystem_size_bytes and incus_filesystem_avail_bytes.
// For containers on ZFS, picks the root mountpoint ("/").
// For VMs, picks the largest real (non-tmpfs) filesystem.
func parseFilesystemUsage(families map[string]*dto.MetricFamily, instances map[string]*IncusInstance) {
	type fsEntry struct {
		size   float64
		avail  float64
		fstype string
		mount  string
	}

	// Collect all filesystem entries keyed by (name, device, mountpoint).
	type fsKey struct {
		name, device, mount string
	}
	entries := make(map[fsKey]*fsEntry)

	if fam, ok := families["incus_filesystem_size_bytes"]; ok {
		for _, m := range fam.GetMetric() {
			name := labelValue(m, "name")
			device := labelValue(m, "device")
			mount := labelValue(m, "mountpoint")
			fstype := labelValue(m, "fstype")
			if name == "" || mount == "" {
				continue
			}
			key := fsKey{name, device, mount}
			entries[key] = &fsEntry{
				size:   metricValue(m),
				fstype: fstype,
				mount:  mount,
			}
		}
	}

	if fam, ok := families["incus_filesystem_avail_bytes"]; ok {
		for _, m := range fam.GetMetric() {
			name := labelValue(m, "name")
			device := labelValue(m, "device")
			mount := labelValue(m, "mountpoint")
			if name == "" || mount == "" {
				continue
			}
			key := fsKey{name, device, mount}
			if e, ok := entries[key]; ok {
				e.avail = metricValue(m)
			}
		}
	}

	// For each instance, pick the best filesystem.
	type bestFS struct {
		size, avail float64
		isRoot      bool
	}
	best := make(map[string]*bestFS)

	for key, e := range entries {
		// Skip pseudo-filesystems.
		if e.fstype == "tmpfs" || e.fstype == "devtmpfs" || e.fstype == "ramfs" ||
			e.fstype == "fuse.lxcfs" {
			continue
		}
		// Skip zero-size filesystems.
		if e.size == 0 {
			continue
		}

		isRoot := e.mount == "/"
		cur, exists := best[key.name]
		if !exists {
			best[key.name] = &bestFS{size: e.size, avail: e.avail, isRoot: isRoot}
			continue
		}

		// Always prefer root mountpoint over non-root.
		if isRoot && !cur.isRoot {
			best[key.name] = &bestFS{size: e.size, avail: e.avail, isRoot: true}
			continue
		}

		// Don't replace root with a non-root filesystem.
		if cur.isRoot && !isRoot {
			continue
		}

		// Both same priority: pick the largest.
		if e.size > cur.size {
			best[key.name] = &bestFS{size: e.size, avail: e.avail, isRoot: isRoot}
		}
	}

	for name, fs := range best {
		inst, ok := instances[name]
		if !ok {
			continue
		}
		inst.DiskTotalBytes = int64(fs.size)
		used := fs.size - fs.avail
		if used < 0 {
			used = 0
		}
		inst.DiskUsedBytes = int64(used)
	}
}

// parseNetworkBytes sums network RX/TX per instance, excluding loopback.
func parseNetworkBytes(families map[string]*dto.MetricFamily, instances map[string]*IncusInstance) {
	accumNet := func(metricName string, setter func(*IncusInstance, int64)) {
		fam, ok := families[metricName]
		if !ok {
			return
		}
		for _, m := range fam.GetMetric() {
			device := labelValue(m, "device")
			if device == "lo" {
				continue
			}
			name := labelValue(m, "name")
			if name == "" {
				continue
			}
			inst, ok := instances[name]
			if !ok {
				continue
			}
			setter(inst, int64(metricValue(m)))
		}
	}

	accumNet("incus_network_receive_bytes_total", func(inst *IncusInstance, v int64) {
		inst.NetworkRxBytes += v
	})
	accumNet("incus_network_transmit_bytes_total", func(inst *IncusInstance, v int64) {
		inst.NetworkTxBytes += v
	})
}

// parseHostMetrics extracts IncusOS host-level stats from the node_* metrics.
func parseHostMetrics(families map[string]*dto.MetricFamily, host *IncusHost) {
	// Uptime.
	now := hostGaugeValue(families, "node_time_seconds")
	boot := hostGaugeValue(families, "node_boot_time_seconds")
	if now > 0 && boot > 0 {
		hours := (now - boot) / 3600
		host.UptimeHours = math.Round(hours*10) / 10
	}

	// CPU load.
	host.CPULoad1m = hostGaugeValue(families, "node_load1")

	// Memory.
	memTotal := hostGaugeValue(families, "node_memory_MemTotal_bytes")
	memAvail := hostGaugeValue(families, "node_memory_MemAvailable_bytes")
	if memTotal > 0 {
		host.MemoryTotalBytes = int64(memTotal)
		pct := ((memTotal - memAvail) / memTotal) * 100
		host.MemoryUsedPercent = math.Round(pct*10) / 10
	}

	// Disk usage (ZFS-aware, same logic as nodeexporter.go).
	host.DiskUsedPercent = calcHostDiskPercent(families)

	// Temperatures.
	if fam, ok := families["node_hwmon_temp_celsius"]; ok {
		for _, m := range fam.GetMetric() {
			if m.GetGauge() == nil {
				continue
			}
			chip := labelValue(m, "chip")
			sensor := labelValue(m, "sensor")
			key := chip
			if sensor != "" {
				key = chip + "_" + sensor
			}
			if key == "" {
				continue
			}
			host.Temperatures[key] = math.Round(m.GetGauge().GetValue()*10) / 10
		}
	}

	// ZFS pool health.
	host.ZFSPoolHealthy = true // default if no ZFS data
	host.ZFSPoolState = "unknown"
	if fam, ok := families["node_zfs_zpool_state"]; ok {
		for _, m := range fam.GetMetric() {
			if m.GetGauge() == nil {
				continue
			}
			state := labelValue(m, "state")
			if m.GetGauge().GetValue() == 1 {
				host.ZFSPoolState = state
				host.ZFSPoolHealthy = (state == "online")
				break
			}
		}
	}

	// OOM kills.
	host.OOMKills = int64(hostGaugeValue(families, "node_vmstat_oom_kill"))
}

// hostGaugeValue reads a single gauge value from a host-level metric
// (no "name" label — these are node_* metrics from the IncusOS host).
func hostGaugeValue(families map[string]*dto.MetricFamily, name string) float64 {
	fam, ok := families[name]
	if !ok || len(fam.GetMetric()) == 0 {
		return 0
	}
	m := fam.GetMetric()[0]
	if g := m.GetGauge(); g != nil {
		return g.GetValue()
	}
	if u := m.GetUntyped(); u != nil {
		return u.GetValue()
	}
	if c := m.GetCounter(); c != nil {
		return c.GetValue()
	}
	return 0
}

// calcHostDiskPercent computes root filesystem usage for the IncusOS host.
// Uses the same ZFS-aware logic as the NixOS node exporter parser.
func calcHostDiskPercent(families map[string]*dto.MetricFamily) float64 {
	sizeFam := families["node_filesystem_size_bytes"]
	availFam := families["node_filesystem_avail_bytes"]
	if sizeFam == nil || availFam == nil {
		return 0
	}

	type fsInfo struct {
		size, avail float64
		fstype      string
	}
	filesystems := make(map[string]fsInfo)

	for _, m := range sizeFam.GetMetric() {
		mp := labelValue(m, "mountpoint")
		fstype := labelValue(m, "fstype")
		if m.GetGauge() == nil || mp == "" {
			continue
		}
		if fstype == "tmpfs" || fstype == "devtmpfs" || fstype == "overlay" ||
			fstype == "ramfs" || fstype == "fuse.lxcfs" {
			continue
		}
		filesystems[mp] = fsInfo{size: m.GetGauge().GetValue(), fstype: fstype}
	}
	for _, m := range availFam.GetMetric() {
		mp := labelValue(m, "mountpoint")
		if m.GetGauge() == nil || mp == "" {
			continue
		}
		if fs, ok := filesystems[mp]; ok {
			fs.avail = m.GetGauge().GetValue()
			filesystems[mp] = fs
		}
	}

	var bestSize, bestAvail float64
	isZFS := false
	for _, fs := range filesystems {
		if fs.fstype == "zfs" {
			isZFS = true
			break
		}
	}

	if isZFS {
		for _, fs := range filesystems {
			if fs.fstype == "zfs" && fs.size > bestSize {
				bestSize = fs.size
				bestAvail = fs.avail
			}
		}
	} else {
		for mp, fs := range filesystems {
			if mp == "/" {
				bestSize = fs.size
				bestAvail = fs.avail
				break
			}
			if fs.size > bestSize {
				bestSize = fs.size
				bestAvail = fs.avail
			}
		}
	}

	if bestSize == 0 {
		return 0
	}
	pct := ((bestSize - bestAvail) / bestSize) * 100
	return math.Round(pct*10) / 10
}

// metricValue extracts the numeric value from a Prometheus metric,
// regardless of whether it's a gauge, counter, or untyped.
func metricValue(m *dto.Metric) float64 {
	if g := m.GetGauge(); g != nil {
		return g.GetValue()
	}
	if c := m.GetCounter(); c != nil {
		return c.GetValue()
	}
	if u := m.GetUntyped(); u != nil {
		return u.GetValue()
	}
	return 0
}
