// Package collector implements metric scrapers for Node Exporter and Incus.
package collector

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
)

// NixOSMetrics holds the translated, frontend-ready metrics for one NixOS machine.
type NixOSMetrics struct {
	UptimeHours      float64           `json:"uptime_hours"`
	CPULoad1m        float64           `json:"cpu_load_1m"`
	RAMUsedPercent   float64           `json:"ram_used_percent"`
	DiskUsedPercent  float64           `json:"disk_used_percent"`
	SMARTHealthy     bool              `json:"smart_healthy"`
	FailedServices   []string          `json:"failed_services"`
	OOMKills         int64             `json:"oom_kills"`
	RebootRequired   bool              `json:"reboot_required"`
	NixOSGeneration  int64             `json:"nixos_generation"`
	Temperatures     map[string]float64 `json:"temperatures"`
	BorgLastBackup   *float64          `json:"borg_last_backup,omitempty"`
}

// ScrapeNodeExporter fetches and parses metrics from a Prometheus Node Exporter endpoint.
func ScrapeNodeExporter(ctx context.Context, client *http.Client, url, username, password string) (*NixOSMetrics, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	if username != "" {
		req.SetBasicAuth(username, password)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("scrape: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("scrape returned status %d", resp.StatusCode)
	}

	return parseNodeExporterMetrics(resp.Body, resp.Header.Get("Content-Type"))
}

func parseNodeExporterMetrics(r io.Reader, contentType string) (*NixOSMetrics, error) {
	parser := &expfmt.TextParser{}
	families, err := parser.TextToMetricFamilies(r)
	if err != nil {
		return nil, fmt.Errorf("parse prometheus text: %w", err)
	}

	m := &NixOSMetrics{
		SMARTHealthy: true, // default healthy until proven otherwise
		Temperatures: make(map[string]float64),
	}

	m.UptimeHours = calcUptime(families)
	m.CPULoad1m = gaugeValue(families, "node_load1")
	m.RAMUsedPercent = calcRAMPercent(families)
	m.DiskUsedPercent = calcRootDiskPercent(families)
	m.SMARTHealthy = calcSMARTHealth(families)
	m.FailedServices = calcFailedServices(families)
	m.OOMKills = int64(gaugeValue(families, "node_vmstat_oom_kill"))
	m.RebootRequired = calcRebootRequired(families)
	m.NixOSGeneration = int64(gaugeValue(families, "node_nixos_generation"))
	m.Temperatures = calcTemperatures(families)
	m.BorgLastBackup = calcBorgLastBackup(families)

	return m, nil
}

func gaugeValue(families map[string]*dto.MetricFamily, name string) float64 {
	fam, ok := families[name]
	if !ok || len(fam.GetMetric()) == 0 {
		return 0
	}
	metric := fam.GetMetric()[0]
	if g := metric.GetGauge(); g != nil {
		return g.GetValue()
	}
	if u := metric.GetUntyped(); u != nil {
		return u.GetValue()
	}
	return 0
}

func calcUptime(families map[string]*dto.MetricFamily) float64 {
	now := gaugeValue(families, "node_time_seconds")
	boot := gaugeValue(families, "node_boot_time_seconds")
	if now == 0 || boot == 0 {
		return 0
	}
	hours := (now - boot) / 3600
	return math.Round(hours*10) / 10
}

func calcRAMPercent(families map[string]*dto.MetricFamily) float64 {
	total := gaugeValue(families, "node_memory_MemTotal_bytes")
	avail := gaugeValue(families, "node_memory_MemAvailable_bytes")
	if total == 0 {
		return 0
	}
	pct := ((total - avail) / total) * 100
	return math.Round(pct*10) / 10
}

func calcRootDiskPercent(families map[string]*dto.MetricFamily) float64 {
	// Find the root filesystem mount.
	sizeFam := families["node_filesystem_size_bytes"]
	availFam := families["node_filesystem_avail_bytes"]
	if sizeFam == nil || availFam == nil {
		return 0
	}

	// Build a map of mountpoint -> avail bytes.
	availMap := make(map[string]float64)
	for _, m := range availFam.GetMetric() {
		mp := labelValue(m, "mountpoint")
		if mp != "" && m.GetGauge() != nil {
			availMap[mp] = m.GetGauge().GetValue()
		}
	}

	// Find root mount or the largest filesystem as fallback.
	var bestSize, bestAvail float64
	foundRoot := false
	for _, m := range sizeFam.GetMetric() {
		mp := labelValue(m, "mountpoint")
		fstype := labelValue(m, "fstype")
		if m.GetGauge() == nil || mp == "" {
			continue
		}
		// Skip pseudo filesystems.
		if fstype == "tmpfs" || fstype == "devtmpfs" || fstype == "overlay" {
			continue
		}
		size := m.GetGauge().GetValue()
		avail, ok := availMap[mp]
		if !ok {
			continue
		}
		if mp == "/" {
			bestSize = size
			bestAvail = avail
			foundRoot = true
		} else if !foundRoot && size > bestSize {
			bestSize = size
			bestAvail = avail
		}
	}

	if bestSize == 0 {
		return 0
	}
	pct := ((bestSize - bestAvail) / bestSize) * 100
	return math.Round(pct*10) / 10
}

func calcSMARTHealth(families map[string]*dto.MetricFamily) bool {
	// textfile metric: node_smart_healthy 1 = healthy, 0 = failing
	fam, ok := families["node_smart_healthy"]
	if !ok {
		return true // no SMART data means we assume healthy
	}
	for _, m := range fam.GetMetric() {
		if m.GetGauge() != nil && m.GetGauge().GetValue() == 0 {
			return false
		}
	}
	return true
}

func calcFailedServices(families map[string]*dto.MetricFamily) []string {
	// systemd collector: node_systemd_unit_state{name="...",state="failed"} 1
	fam, ok := families["node_systemd_unit_state"]
	if !ok {
		return []string{}
	}
	var failed []string
	for _, m := range fam.GetMetric() {
		state := labelValue(m, "state")
		if state == "failed" && m.GetGauge() != nil && m.GetGauge().GetValue() == 1 {
			name := labelValue(m, "name")
			if name != "" {
				failed = append(failed, name)
			}
		}
	}
	if failed == nil {
		return []string{}
	}
	return failed
}

func calcRebootRequired(families map[string]*dto.MetricFamily) bool {
	// textfile metric: node_reboot_required 1 = reboot needed
	v := gaugeValue(families, "node_reboot_required")
	return v == 1
}

func calcTemperatures(families map[string]*dto.MetricFamily) map[string]float64 {
	temps := make(map[string]float64)
	fam, ok := families["node_hwmon_temp_celsius"]
	if !ok {
		return temps
	}
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
		temps[key] = math.Round(m.GetGauge().GetValue()*10) / 10
	}
	return temps
}

func calcBorgLastBackup(families map[string]*dto.MetricFamily) *float64 {
	// textfile metric: node_borg_last_backup_timestamp_seconds
	fam, ok := families["node_borg_last_backup_timestamp_seconds"]
	if !ok {
		return nil
	}
	if len(fam.GetMetric()) == 0 {
		return nil
	}
	v := gaugeValue(families, "node_borg_last_backup_timestamp_seconds")
	return &v
}

func labelValue(m *dto.Metric, name string) string {
	for _, l := range m.GetLabel() {
		if l.GetName() == name {
			return l.GetValue()
		}
	}
	return ""
}


