// Package alerter sends push notifications via ntfy.sh for critical events.
package alerter

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// Event represents a type of alertable condition.
type Event string

const (
	EventMachineOffline Event = "machine_offline"
	EventSMARTFailure   Event = "smart_failure"
	EventOOMKill        Event = "oom_kill"
	EventHighDisk       Event = "high_disk"
	EventFailedServices Event = "failed_services"
	EventBorgStale      Event = "borg_stale"
)

// Alerter manages sending and rate-limiting push notifications.
type Alerter struct {
	ntfyURL  string
	cooldown time.Duration
	client   *http.Client
	logger   *slog.Logger

	mu       sync.Mutex
	lastSent map[string]time.Time // key: "hostname:event"
}

// New creates a new Alerter. If ntfyURL is empty, alerts are disabled (no-op).
func New(ntfyURL string, cooldownMinutes int, logger *slog.Logger) *Alerter {
	return &Alerter{
		ntfyURL:  ntfyURL,
		cooldown: time.Duration(cooldownMinutes) * time.Minute,
		client:   &http.Client{Timeout: 10 * time.Second},
		logger:   logger,
		lastSent: make(map[string]time.Time),
	}
}

// Enabled returns true if alerting is configured.
func (a *Alerter) Enabled() bool {
	return a.ntfyURL != ""
}

// Send sends a push notification if the cooldown for this (hostname, event)
// pair has elapsed. Returns true if the notification was actually sent.
func (a *Alerter) Send(ctx context.Context, hostname string, event Event, title, message string) bool {
	if !a.Enabled() {
		return false
	}

	key := fmt.Sprintf("%s:%s", hostname, event)

	a.mu.Lock()
	if last, ok := a.lastSent[key]; ok && time.Since(last) < a.cooldown {
		a.mu.Unlock()
		a.logger.Debug("alert cooldown active", "key", key)
		return false
	}
	a.lastSent[key] = time.Now()
	a.mu.Unlock()

	if err := a.post(ctx, title, message); err != nil {
		a.logger.Error("failed to send alert", "key", key, "error", err)
		return false
	}

	a.logger.Warn("alert sent", "key", key, "title", title)
	return true
}

func (a *Alerter) post(ctx context.Context, title, message string) error {
	body := bytes.NewBufferString(message)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.ntfyURL, body)
	if err != nil {
		return fmt.Errorf("create ntfy request: %w", err)
	}
	req.Header.Set("Title", title)
	req.Header.Set("Priority", "high")
	req.Header.Set("Tags", "warning")

	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("post to ntfy: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("ntfy returned status %d", resp.StatusCode)
	}
	return nil
}
