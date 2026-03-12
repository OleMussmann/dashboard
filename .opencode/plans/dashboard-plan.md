# Dashboard Startpage — Project Plan

## 1. Overview

A self-hosted browser startpage showing health and status of all homelab machines
and services. Two containers run on IncusOS:

- **Homepage** (gethomepage.dev): Frontend startpage with built-in widgets for
  Nextcloud, Home Assistant, and weather. Uses `customapi` widgets for NixOS and
  Incus data from the Go backend.
- **dashboard-api**: A single statically-compiled Go binary (scratch container)
  that polls NixOS machines via Prometheus Node Exporter and scrapes the Incus
  metrics endpoint. Serves a JSON API for Homepage to consume.

No historical data, no database. Current-state only with offline detection.
All communication over Tailscale. No public internet exposure.

## 2. Architecture

```
 NixOS machines (6-15)
   prometheus-node-exporter (port 9100, Basic Auth over Tailscale)
   + systemd timer dumps custom metrics to textfile collector
        |
        v  Tailscale (Pull)
 IncusOS Host
  +-- Container: "homepage" -----------------------------------------+
  |  Homepage (gethomepage.dev)                                      |
  |  Directly polls: Nextcloud, Home Assistant, Open-Meteo           |
  |  customapi widgets → dashboard-api for NixOS + Incus data        |
  +------------------------------------------------------------------+
  +-- Container: "dashboard-api" (scratch, non-root) ----------------+
  |                                                                   |
  |  Single static Go binary (pure Go, no CGO)                       |
  |                                                                   |
  |  Server:                                                          |
  |    GET /api/v1/status            <- all NixOS + Incus metrics     |
  |    GET /api/v1/status/:hostname  <- single machine metrics        |
  |                                                                   |
  |  Collector (internal scheduler):                                  |
  |    -> NixOS Node Exporters (Basic Auth, every 5 min)              |
  |    -> Incus metrics endpoint (TLS cert, every 5 min)              |
  |                                                                   |
  |  In-memory state:                                                 |
  |    Latest metrics per machine                                     |
  |    Online/offline status + last_seen timestamp                    |
  |    Offline = 3 consecutive failed polls                           |
  |                                                                   |
  |  Mounts:                                                          |
  |    /secrets/  <- read-only (TLS cert/key, Basic Auth creds)       |
  +-------------------------------------------------------------------+
```

### Data Flow

| Source           | Method                    | Transport               | Credentials                          |
|------------------|---------------------------|-------------------------|--------------------------------------|
| NixOS machines   | Pull (node-exporter)      | Tailscale               | Basic Auth                           |
| Incus host       | Pull (metrics endpoint)   | HTTPS (localhost/TS)    | Metrics-only TLS cert                |
| Nextcloud        | Poll (Homepage direct)    | HTTPS (public)          | NC-Token (in Homepage config)        |
| Home Assistant   | Poll (Homepage direct)    | Tailscale               | Long-lived token (in Homepage config)|
| Open-Meteo       | Poll (Homepage direct)    | HTTPS (public)          | None                                 |

### Design Decisions & Rationale

- **Homepage over custom frontend**: Mature, actively maintained startpage with
  native widgets for Nextcloud, HA, and Open-Meteo. Eliminates the need to build
  and maintain a custom frontend. The `customapi` widget handles custom NixOS and
  Incus data.
- **Go over Rust**: Memory-safe, fast concurrency, static binary suitable for a
  scratch container. Significantly less boilerplate.
- **Pure Go (no CGO)**: No SQLite dependency means no CGO. Truly static binary,
  simpler Nix build, smaller image.
- **No historical data / no database**: Eliminates SQLite, schema management,
  retention pruning, and concurrent write concerns. Keeps the Go backend trivially
  simple. Dashboard shows current state only.
- **Pull model for NixOS machines**: Tailscale flattens the network.
  `prometheus-node-exporter` with the textfile collector is standard, reliable,
  and extensible without changing the dashboard code. New metrics from textfile
  collectors appear automatically in the API response.
- **Metrics-only TLS cert for Incus**: Using
  `incus config trust add-certificate metrics.crt --type=metrics` grants access
  to only `GET /1.0/metrics`. No admin access, no instance creation/deletion,
  no socket mount. The metrics endpoint returns per-instance CPU/mem/disk/net in
  Prometheus format.
- **Read-only security posture**: Nextcloud uses a scoped Serverinfo token. HA
  uses a token tied to a read-only user. Incus uses a metrics-only certificate.
  NixOS uses Basic Auth scoped to the exporter.
- **No heavy frameworks**: No Prometheus server, no Grafana, no InfluxDB.
  Homepage + a small Go binary handle exactly what is needed.

## 3. Monitored Systems & Metrics

### NixOS Machines (via Go backend polling Node Exporter)

| Metric                  | Source / Exporter Plugin                                      |
|-------------------------|---------------------------------------------------------------|
| Online/offline status   | Success/failure of HTTP scrape (offline after 3 failures)     |
| Uptime                  | `node_time_seconds` - `node_boot_time_seconds`                |
| Disk usage (all mounts) | `node_filesystem_avail_bytes` / `_size_bytes`                 |
| SMART disk health       | `textfile` (smartctl dumped via systemd timer)                |
| Borg backup status      | `textfile` (borgmatic output dumped via script)               |
| NixOS generation        | `textfile` (readlink /nix/var/nix/profiles/system)            |

The Go backend parses whatever Prometheus metrics each node-exporter returns and
serves them as structured JSON. No hardcoded metric names in Go code — new
metrics from textfile collectors appear automatically in the API response.

### Nextcloud (Homepage built-in widget, direct)

| Metric           | Widget                                    |
|------------------|-------------------------------------------|
| Online/reachable | Homepage `siteMonitor`                    |
| Free space       | `nextcloud` widget field `freespace`      |
| Active users     | `nextcloud` widget field `activeusers`    |
| File count       | `nextcloud` widget field `numfiles`       |

### Home Assistant (Homepage built-in widget, direct)

| Metric           | Widget                                       |
|------------------|----------------------------------------------|
| Online/reachable | Homepage `siteMonitor`                       |
| HA Core version  | `homeassistant` widget `custom` state        |
| Entity states    | `homeassistant` widget `custom` templates    |

### Incus & IncusOS (via Go backend scraping metrics endpoint)

| Metric                            | Source                                     |
|-----------------------------------|--------------------------------------------|
| Per-instance CPU/memory/disk/net  | `GET /1.0/metrics` (Prometheus format)     |
| Instance names and running state  | `incus_instance_info` metric labels        |
| Host resource usage               | Included in metrics response               |

Note: The metrics endpoint returns whatever Incus exposes. The Go backend parses
all returned metrics and serves them through the API. Exact available fields
depend on the Incus version.

### Weather (Homepage built-in widget, direct)

| Metric                         | Widget                              |
|--------------------------------|-------------------------------------|
| Current temperature + forecast | Homepage `openmeteo` info widget    |

## 4. API Endpoints (Go Backend)

| Method | Path                       | Description                           |
|--------|----------------------------|---------------------------------------|
| GET    | `/api/v1/status`           | All NixOS machines + Incus metrics    |
| GET    | `/api/v1/status/:hostname` | Single machine metrics                |

Response format for `/api/v1/status`:

```json
{
  "machines": {
    "server-01": {
      "type": "nixos",
      "status": "online",
      "last_seen": "2026-03-12T10:30:00Z",
      "metrics": {
        "node_time_seconds": 1741776600,
        "node_boot_time_seconds": 1741000000,
        "node_filesystem_avail_bytes{mountpoint=\"/\"}": 50000000000
      }
    },
    "server-02": {
      "type": "nixos",
      "status": "offline",
      "last_seen": "2026-03-12T09:15:00Z",
      "metrics": {}
    }
  },
  "incus": {
    "status": "online",
    "last_seen": "2026-03-12T10:30:00Z",
    "metrics": {
      "incus_instance_info{name=\"homepage\",status=\"Running\"}": 1
    }
  }
}
```

Homepage's `customapi` widget maps JSON paths to display fields
(e.g., `machines.server-01.status` -> "Status").

## 5. Technology Choices

| Component        | Choice                          | Rationale                                     |
|------------------|---------------------------------|-----------------------------------------------|
| Frontend         | Homepage (gethomepage.dev)      | Native NC/HA/weather widgets, customapi       |
| Backend language | Go                              | Memory safe, fast concurrency, static binary  |
| HTTP framework   | `net/http`                      | Standard library, zero deps                   |
| HTTP client      | `net/http` with timeouts        | Per-request 10s timeout, concurrency limit    |
| Prom parser      | `prometheus/common/expfmt`      | Standard Prometheus text format parser         |
| Container (API)  | scratch (OCI image)             | Static binary, ~10MB, nothing to attack       |
| Container (UI)   | Homepage official image         | Docker image, ~200MB                          |
| Build system     | Nix flake                       | Reproducible, handles static Go compilation   |
| NixOS agent      | `prometheus-node-exporter`      | Standard, reliable, extensible via textfile   |
| Logging          | `log/slog` (structured, stdout) | Standard library, container-friendly          |

## 6. Project Structure

```
dashboard/
├── flake.nix                     # Nix flake: Go binary + OCI image + NixOS module
├── flake.lock
├── go.mod
├── go.sum
│
├── cmd/
│   └── dashboard-api/
│       └── main.go               # Entry: start server + scheduler
├── internal/
│   ├── config/                   # Load TOML config
│   ├── server/                   # HTTP handlers (status endpoint)
│   ├── collector/
│   │   ├── nodeexporter.go       # Parse Prometheus text from node-exporters
│   │   └── incus.go              # Parse Prometheus text from Incus metrics
│   └── scheduler/                # Runs collectors on interval, tracks state
│
├── nix/
│   ├── package.nix               # Go binary derivation (CGO_ENABLED=0)
│   ├── api-image.nix             # OCI scratch container for Go backend
│   └── agent-module.nix          # NixOS module for node-exporter + textfile scripts
│
├── homepage/
│   ├── services.yaml             # Homepage service definitions
│   ├── widgets.yaml              # Homepage info widgets (weather, etc.)
│   └── settings.yaml             # Homepage settings
│
├── config.example.toml           # Example backend config
└── deploy.sh                     # Build + zero-downtime deploy script
```

## 7. Configuration

### Go Backend (TOML)

```toml
[server]
listen = "0.0.0.0:8080"

[incus]
url = "https://incus-host.tailnet-name.ts.net:8443"
cert_file = "/secrets/metrics.crt"
key_file = "/secrets/metrics.key"
interval_minutes = 5

[[nixos]]
hostname = "server-01"
url = "http://server-01.tailnet-name.ts.net:9100/metrics"
auth_file = "/secrets/node-exporter-auth"
interval_minutes = 5

[[nixos]]
hostname = "server-02"
url = "http://server-02.tailnet-name.ts.net:9100/metrics"
auth_file = "/secrets/node-exporter-auth"
interval_minutes = 5
```

### Homepage (services.yaml excerpt)

```yaml
- Infrastructure:
    - server-01:
        icon: si-nixos
        widget:
          type: customapi
          url: http://dashboard-api:8080/api/v1/status/server-01
          mappings:
            - field: status
              label: Status
            - field: metrics.uptime_hours
              label: Uptime
              format: number
              suffix: "h"
            - field: metrics.disk_used_percent
              label: Disk
              format: percent

    - Nextcloud:
        icon: nextcloud
        href: https://cloud.example.com
        siteMonitor: https://cloud.example.com
        widget:
          type: nextcloud
          url: https://cloud.example.com
          key: "{{HOMEPAGE_VAR_NC_TOKEN}}"
```

## 8. NixOS Agent Module

```nix
# hosts/server-01/default.nix
{
  imports = [ inputs.dashboard.nixosModules.agent ];

  services.dashboard-agent = {
    enable = true;
    basicAuthFile = config.sops.secrets."node-exporter-auth".path;
    customChecks = {
      smart = true;
      borgJobs = [ "default" ];
    };
  };
}
```

Secrets managed via `sops-nix` or `agenix`, referenced by path under
`/run/secrets/`.

## 9. Offline Detection

The Go backend tracks consecutive poll failures per machine:

- After **3 consecutive failures**: status changes to `"offline"`
- The `last_seen` timestamp preserves the time of the last successful poll
- Homepage displays this as "Offline" via the status field
- On next successful poll: immediately returns to `"online"`

## 10. HTTP Client Resilience

- **Per-request timeout**: 10 seconds (prevents one hanging machine from
  blocking the scheduler)
- **Concurrency limit**: Semaphore of 5 concurrent polls (prevents
  thundering-herd on startup)
- **No retry/backoff**: Failed polls increment the failure counter; the next
  scheduled poll is the retry

## 11. Logging

Structured JSON logging via Go's `log/slog` to stdout. Container runtime
captures logs.

Log levels:
- `INFO`: Successful polls, server startup
- `WARN`: Failed polls, offline transitions
- `ERROR`: Configuration errors, unrecoverable failures

## 12. Build & Deploy

### Zero-Downtime Deploy Script

```bash
nix build .#dashboard-api-image
incus image import ./result --alias dashboard-api-new

# Launch new container with a temporary name
incus launch dashboard-api-new dashboard-api-tmp \
  --device secrets,source=/path/to/secrets ...

# Health check (wait for API to respond)
for i in $(seq 1 30); do
  curl -sf http://dashboard-api-tmp:8080/api/v1/status && break
  sleep 1
done

# Swap
incus stop dashboard-api && incus rm dashboard-api
incus rename dashboard-api-tmp dashboard-api

# Clean up old image
incus image delete dashboard-api-old 2>/dev/null
incus image alias rename dashboard-api-new dashboard-api-old
```

Note: This deploy script is illustrative. Incus rename semantics and device
re-attachment will need validation against actual Incus behavior. The principle
(launch new, health check, swap) is sound.

### Agent Updates

Updates to `node-exporter` configuration or textfile scripts are handled via
standard `nixos-rebuild switch`.
