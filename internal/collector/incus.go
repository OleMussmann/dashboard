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
	Status        string  `json:"status"`
	CPUSeconds    float64 `json:"cpu_seconds"`
	MemoryBytes   int64   `json:"memory_bytes"`
	DiskBytes     int64   `json:"disk_bytes"`
	NetworkRxBytes int64  `json:"network_rx_bytes"`
	NetworkTxBytes int64  `json:"network_tx_bytes"`
}

// IncusHost holds translated host-level metrics.
type IncusHost struct {
	CPUSeconds  float64 `json:"cpu_seconds"`
	MemoryBytes int64   `json:"memory_bytes"`
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
		Host:      &IncusHost{},
	}

	// Extract instance info (names and status).
	if fam, ok := families["incus_instance_info"]; ok {
		for _, m := range fam.GetMetric() {
			name := labelValue(m, "name")
			status := labelValue(m, "status")
			if name == "" {
				continue
			}
			result.Instances[name] = &IncusInstance{
				Status: status,
			}
		}
	}

	// Aggregate per-instance CPU time.
	accumulateInstanceMetric(families, "incus_cpu_seconds_total", result.Instances, func(inst *IncusInstance, v float64) {
		inst.CPUSeconds = math.Round(v*10) / 10
	})

	// Aggregate per-instance memory.
	accumulateInstanceMetric(families, "incus_memory_MemTotal_bytes", result.Instances, func(inst *IncusInstance, v float64) {
		inst.MemoryBytes = int64(v)
	})

	// Aggregate per-instance disk.
	accumulateInstanceMetric(families, "incus_disk_read_bytes_total", result.Instances, func(inst *IncusInstance, v float64) {
		inst.DiskBytes += int64(v)
	})
	accumulateInstanceMetric(families, "incus_disk_written_bytes_total", result.Instances, func(inst *IncusInstance, v float64) {
		inst.DiskBytes += int64(v)
	})

	// Aggregate per-instance network.
	accumulateInstanceMetric(families, "incus_network_receive_bytes_total", result.Instances, func(inst *IncusInstance, v float64) {
		inst.NetworkRxBytes += int64(v)
	})
	accumulateInstanceMetric(families, "incus_network_transmit_bytes_total", result.Instances, func(inst *IncusInstance, v float64) {
		inst.NetworkTxBytes += int64(v)
	})

	return result, nil
}

// accumulateInstanceMetric extracts a metric value for each instance by looking
// up the "name" label, and calls the setter function.
func accumulateInstanceMetric(
	families map[string]*dto.MetricFamily,
	metricName string,
	instances map[string]*IncusInstance,
	setter func(*IncusInstance, float64),
) {
	fam, ok := families[metricName]
	if !ok {
		return
	}
	for _, m := range fam.GetMetric() {
		name := labelValue(m, "name")
		if name == "" {
			continue
		}
		inst, ok := instances[name]
		if !ok {
			// Instance not in info, create a stub.
			inst = &IncusInstance{Status: "Unknown"}
			instances[name] = inst
		}
		var val float64
		if g := m.GetGauge(); g != nil {
			val = g.GetValue()
		} else if c := m.GetCounter(); c != nil {
			val = c.GetValue()
		}
		setter(inst, val)
	}
}
