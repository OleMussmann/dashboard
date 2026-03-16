// Package scheduler runs periodic metric collection and maintains machine state.
package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/ole/dashboard-api/internal/alerter"
	"github.com/ole/dashboard-api/internal/collector"
	"github.com/ole/dashboard-api/internal/config"
)

// MachineStatus represents the online/offline state.
type MachineStatus string

const (
	StatusOnline      MachineStatus = "online"
	StatusUnreachable MachineStatus = "unreachable"
	StatusOffline     MachineStatus = "offline"
)

// MachineState holds the current state for one NixOS machine.
type MachineState struct {
	Type     string                   `json:"type"`
	Status   MachineStatus            `json:"status"`
	LastSeen time.Time                `json:"last_seen"`
	Metrics  *collector.NixOSMetrics  `json:"metrics,omitempty"`
}

// IncusState holds the current state for Incus.
type IncusState struct {
	Status   MachineStatus           `json:"status"`
	LastSeen time.Time               `json:"last_seen"`
	Metrics  *collector.IncusMetrics `json:"metrics,omitempty"`
}

// Snapshot represents the full current state served by the API.
type Snapshot struct {
	Machines map[string]*MachineState `json:"machines"`
	Incus    *IncusState              `json:"incus,omitempty"`
}

// Scheduler periodically polls all targets and maintains in-memory state.
type Scheduler struct {
	cfg         *config.Config
	alerter     *alerter.Alerter
	logger      *slog.Logger
	httpClient  *http.Client
	incusClient *http.Client

	mu       sync.RWMutex
	machines map[string]*machineTracker
	incus    *incusTracker

	// semaphore limits concurrent polls.
	sem chan struct{}
}

type machineTracker struct {
	cfg            config.NixOSConfig
	state          *MachineState
	failCount      int
	previousOOMKills int64
}

type incusTracker struct {
	state     *IncusState
	failCount int
}

// New creates a scheduler from the given config.
func New(cfg *config.Config, alert *alerter.Alerter, logger *slog.Logger) (*Scheduler, error) {
	s := &Scheduler{
		cfg:     cfg,
		alerter: alert,
		logger:  logger,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		machines: make(map[string]*machineTracker),
		sem:      make(chan struct{}, 5),
	}

	// Initialize machine trackers.
	for _, n := range cfg.NixOS {
		s.machines[n.Hostname] = &machineTracker{
			cfg: n,
			state: &MachineState{
				Type:   "nixos",
				Status: StatusOffline,
			},
		}
	}

	// Initialize Incus tracker if configured.
	if cfg.Incus.URL != "" {
		client, err := collector.NewIncusTLSClient(cfg.Incus.CertFile, cfg.Incus.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("create incus client: %w", err)
		}
		s.incusClient = client
		s.incus = &incusTracker{
			state: &IncusState{
				Status: StatusOffline,
			},
		}
	}

	return s, nil
}

// Run starts the scheduler. It performs an immediate poll, then ticks at the configured interval.
// It blocks until ctx is cancelled.
func (s *Scheduler) Run(ctx context.Context) {
	s.logger.Info("scheduler starting, performing initial poll")
	s.pollAll(ctx)

	interval := time.Duration(s.cfg.Server.PollIntervalMinutes) * time.Minute
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("scheduler stopping")
			return
		case <-ticker.C:
			s.pollAll(ctx)
		}
	}
}

// GetSnapshot returns a copy of the current state.
func (s *Scheduler) GetSnapshot() *Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	snap := &Snapshot{
		Machines: make(map[string]*MachineState, len(s.machines)),
	}
	for name, t := range s.machines {
		// Shallow copy is fine; metrics are replaced atomically.
		state := *t.state
		snap.Machines[name] = &state
	}
	if s.incus != nil {
		state := *s.incus.state
		snap.Incus = &state
	}
	return snap
}

// GetMachineState returns the state for a single machine, or nil.
func (s *Scheduler) GetMachineState(hostname string) *MachineState {
	s.mu.RLock()
	defer s.mu.RUnlock()

	t, ok := s.machines[hostname]
	if !ok {
		return nil
	}
	state := *t.state
	return &state
}

func (s *Scheduler) pollAll(ctx context.Context) {
	var wg sync.WaitGroup

	// Poll NixOS machines.
	for _, t := range s.machines {
		wg.Add(1)
		go func(tracker *machineTracker) {
			defer wg.Done()
			s.sem <- struct{}{}        // acquire
			defer func() { <-s.sem }() // release
			s.pollNixOS(ctx, tracker)
		}(t)
	}

	// Poll Incus.
	if s.incus != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.sem <- struct{}{}
			defer func() { <-s.sem }()
			s.pollIncus(ctx)
		}()
	}

	wg.Wait()
	s.logger.Info("poll cycle complete")
}

func (s *Scheduler) pollNixOS(ctx context.Context, t *machineTracker) {
	pollCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	metrics, err := collector.ScrapeNodeExporter(
		pollCtx, s.httpClient,
		t.cfg.URL, t.cfg.Username, t.cfg.Password(),
	)

	s.mu.Lock()
	defer s.mu.Unlock()

	if err != nil {
		t.failCount++
		s.logger.Warn("poll failed",
			"hostname", t.cfg.Hostname,
			"error", err,
			"fail_count", t.failCount,
		)

		oldStatus := t.state.Status
		switch {
		case t.failCount >= 3:
			t.state.Status = StatusOffline
		default:
			t.state.Status = StatusUnreachable
		}

		// Alert on transition to offline for critical machines.
		if oldStatus != StatusOffline && t.state.Status == StatusOffline && t.cfg.Critical {
			s.alerter.Send(ctx, t.cfg.Hostname, alerter.EventMachineOffline,
				fmt.Sprintf("%s is offline", t.cfg.Hostname),
				fmt.Sprintf("Machine %s has been unreachable for %d consecutive polls.", t.cfg.Hostname, t.failCount),
			)
		}
		return
	}

	// Success.
	t.failCount = 0
	t.state.Status = StatusOnline
	t.state.LastSeen = time.Now()

	// Check alert conditions before updating metrics.
	s.checkAlerts(ctx, t, metrics)

	t.state.Metrics = metrics
}

func (s *Scheduler) checkAlerts(ctx context.Context, t *machineTracker, m *collector.NixOSMetrics) {
	rules := s.cfg.Alerting.Rules
	hostname := t.cfg.Hostname

	// SMART failure (all machines).
	if rules.SMARTFailure && !m.SMARTHealthy {
		var failingDisks []string
		for disk, healthy := range m.SMARTDisks {
			if !healthy {
				failingDisks = append(failingDisks, disk)
			}
		}
		detail := fmt.Sprintf("One or more disks on %s are reporting SMART failures.", hostname)
		if len(failingDisks) > 0 {
			detail = fmt.Sprintf("SMART failures on %s: %v", hostname, failingDisks)
		}
		s.alerter.Send(ctx, hostname, alerter.EventSMARTFailure,
			fmt.Sprintf("SMART failure on %s", hostname),
			detail,
		)
	}

	// OOM kills (all machines, delta-based).
	if rules.OOMKill && m.OOMKills > t.previousOOMKills && t.previousOOMKills > 0 {
		s.alerter.Send(ctx, hostname, alerter.EventOOMKill,
			fmt.Sprintf("OOM kill on %s", hostname),
			fmt.Sprintf("OOM kills increased from %d to %d on %s.", t.previousOOMKills, m.OOMKills, hostname),
		)
	}
	t.previousOOMKills = m.OOMKills

	// High disk usage (all machines).
	if rules.HighDiskUsagePercent > 0 && m.DiskUsedPercent >= float64(rules.HighDiskUsagePercent) {
		s.alerter.Send(ctx, hostname, alerter.EventHighDisk,
			fmt.Sprintf("High disk on %s (%.0f%%)", hostname, m.DiskUsedPercent),
			fmt.Sprintf("Disk usage on %s is at %.1f%%.", hostname, m.DiskUsedPercent),
		)
	}

	// Failed systemd services (all machines).
	if rules.FailedServices && len(m.FailedServices) > 0 {
		s.alerter.Send(ctx, hostname, alerter.EventFailedServices,
			fmt.Sprintf("Failed services on %s", hostname),
			fmt.Sprintf("Failed units: %v", m.FailedServices),
		)
	}

	// Stale Borg backups (all machines).
	if rules.BorgStaleHours > 0 && m.BorgLastBackup != nil {
		age := time.Since(time.Unix(int64(*m.BorgLastBackup), 0))
		if age > time.Duration(rules.BorgStaleHours)*time.Hour {
			s.alerter.Send(ctx, hostname, alerter.EventBorgStale,
				fmt.Sprintf("Stale backup on %s", hostname),
				fmt.Sprintf("Last Borg backup on %s was %.0f hours ago.", hostname, age.Hours()),
			)
		}
	}
}

func (s *Scheduler) pollIncus(ctx context.Context) {
	pollCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	metrics, err := collector.ScrapeIncus(pollCtx, s.incusClient, s.cfg.Incus.URL)

	s.mu.Lock()
	defer s.mu.Unlock()

	if err != nil {
		s.incus.failCount++
		s.logger.Warn("incus poll failed",
			"error", err,
			"fail_count", s.incus.failCount,
		)
		switch {
		case s.incus.failCount >= 3:
			s.incus.state.Status = StatusOffline
		default:
			s.incus.state.Status = StatusUnreachable
		}
		return
	}

	s.incus.failCount = 0
	s.incus.state.Status = StatusOnline
	s.incus.state.LastSeen = time.Now()
	s.incus.state.Metrics = metrics
}
