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

No historical data, no database. The backend acts as a translation layer, performing math on raw metrics before serving them. Current-state only with 3-tier offline detection.
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
  |    Latest translated metrics per machine                          |
  |    Online/unreachable/offline status + last_seen timestamp        |
  |    Immediate asynchronous poll on startup (fixes cold start)      |
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

The Go backend acts as a smart translator. Instead of passing raw Prometheus strings,
Go will parse the Prometheus text format and calculate exact frontend-ready values
(e.g., `disk_used_percent`, `uptime_hours`). The JSON output will be clean and nested.

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
        "uptime_hours": 215.4,
        "disk_used_percent": 45.2
      }
    },
    "server-02": {
      "type": "nixos",
      "status": "unreachable",
      "last_seen": "2026-03-12T09:15:00Z",
      "metrics": {
        "uptime_hours": 42.1,
        "disk_used_percent": 88.0
      }
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
username = "metrics"
password_file = "/secrets/node-exporter-pass"
interval_minutes = 5

[[nixos]]
hostname = "server-02"
url = "http://server-02.tailnet-name.ts.net:9100/metrics"
username = "metrics"
password_file = "/secrets/node-exporter-pass"
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
    basicAuthPasswordFile = config.age.secrets."node-exporter-pass".path;
    customChecks = {
      smart = true;
      borgJobs = [ "default" ];
    };
  };
}
```

Secrets managed via `agenix`, templating the `web.config.file` for node-exporter basic auth.

## 9. Offline Detection & State Management

The Go backend maintains a state machine and tracks consecutive poll failures per machine:

- **`online`**: The last poll was successful.
- **`unreachable`**: 1 or 2 consecutive polls have failed.
- **`offline`**: 3 consecutive polls have failed.

**Stale Data Handling:** 
If a host transitions to `unreachable` or `offline`, the API will *keep* serving the last known `metrics` object but will update the `status` field and maintain the `last_seen` timestamp so Homepage users know the data is old.

**Cold Start:** 
When the `dashboard-api` container starts, it executes an immediate asynchronous poll of all targets before falling back to the 5-minute ticker, ensuring data is populated immediately.

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

### Deploy Script

```bash
nix build .#dashboard-api-image
incus image import ./result --alias dashboard-api-new

incus stop dashboard-api
# Rebuild/Launch container from new image here
incus start dashboard-api
```

Note: This deploy script is a simplified approach, embracing the fact that the Go backend boots in milliseconds. Homepage will gracefully handle a 1-2 second API outage.

### Agent Updates

Updates to `node-exporter` configuration or textfile scripts are handled via
standard `nixos-rebuild switch`.
