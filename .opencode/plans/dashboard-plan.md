# Dashboard Startpage — Project Plan

## 1. Overview

A self-hosted browser startpage that displays the health and status of all machines
and services in a homelab. A single statically-compiled Go binary runs on IncusOS 
as a scratch container. It uses a hybrid pull-based architecture to poll NixOS 
machines via Prometheus Node Exporter, query external systems (Nextcloud, Home 
Assistant, Open-Meteo), and read the local Incus socket. It stores 30 days of 
history in SQLite and serves a static frontend.

All communication happens over Tailscale. No public internet exposure.

## 2. Architecture

```
 NixOS machines (6-15)
   prometheus-node-exporter (port 9100, Basic Auth over Tailscale)
   + systemd timer dumps custom metrics to textfile collector
        |
        v  Tailscale (Pull)
 IncusOS Host
  +-- Container: "dashboard" (scratch/distroless, non-root user) -+
  |                                                               |
  |  Single static Go binary                                      |
  |                                                               |
  |  Server Module:                                               |
  |    GET  /api/status     <- latest state, all systems          |
  |    GET  /api/history/:h <- 30-day trends for sparklines       |
  |    GET  /               <- serves static frontend             |
  |    SQLite database (persistent volume)                        |
  |                                                               |
  |  Collector Module (internal scheduler, every 5-15 min):       |
  |    -> NixOS Node Exporters (Basic Auth)                       |
  |    -> Nextcloud Serverinfo API (read-only token)              |
  |    -> Home Assistant REST API (read-only user token)          |
  |    -> Incus & IncusOS REST APIs (local Unix socket)           |
  |    -> Open-Meteo (no creds)                                   |
  |    Feeds results into SQLite database                         |
  |                                                               |
  |  Mounts:                                                      |
  |    /data/          <- persistent volume (SQLite DB)           |
  |    /run/incus/     <- Incus Unix socket from host             |
  |    /secrets/       <- read-only (NC/HA tokens, Basic Auth)    |
  +---------------------------------------------------------------+
```

### Data Flow

| Source              | Method | Transport          | Credentials                      |
|---------------------|--------|--------------------|----------------------------------|
| NixOS machines      | Pull   | Tailscale          | Basic Auth (Tailscale isolates)  |
| Incus host          | Pull   | Unix socket        | None (Socket permissions)        |
| Nextcloud (hosted)  | Pull   | HTTPS (public)     | Read-only Serverinfo Token       |
| Home Assistant OS   | Pull   | Tailscale          | Long-lived token (RO User)       |
| Open-Meteo          | Pull   | HTTPS (public)     | None                             |

### Design Decisions & Rationale

- **Go over Rust**: Go is memory-safe, produces a single static binary suitable for a scratch container, and is heavily optimized for concurrent API polling and JSON parsing. It significantly reduces boilerplate compared to Rust.
- **Pull model for NixOS machines**: Tailscale flattens the network, allowing the dashboard to reach all nodes securely. Using `prometheus-node-exporter` with the textfile collector eliminates the need to write custom, brittle JSON-generating Bash scripts.
- **Read-Only Security Posture**: Nextcloud uses a scoped Serverinfo token. Home Assistant uses a token tied to a locked-down, read-only user. 
- **Incus Socket in Scratch Container**: Mounting the Incus socket gives admin access to the host. We mitigate this by using a memory-safe Go binary, running as a non-root user (in the `incus` group), inside an empty `scratch` container where no shell or external utilities exist to abuse the socket if a vulnerability were found.
- **Flattened SQLite Schema**: Instead of parsing massive JSON blobs on the fly to generate 30-day sparklines, the Go backend will extract key metrics (e.g., `cpu_percent`, `disk_used_gb`) into dedicated numeric columns at ingest time, keeping the database extremely fast.
- **No Heavy Frameworks**: Bypassing Prometheus/Grafana/InfluxDB servers keeps maintenance at zero. The custom Go backend and SQLite database handle exactly what is needed and nothing more.

## 3. Monitored Systems & Metrics

### NixOS Machines (Prometheus Node Exporter)

| Metric                           | Source / Exporter Plugin                                   |
|----------------------------------|------------------------------------------------------------|
| Online status                    | Success/Failure of the HTTP scrape                         |
| Uptime                           | `node_time_seconds` - `node_boot_time_seconds`             |
| Disk usage (all mounts)          | `node_filesystem_avail_bytes` / `_size_bytes`              |
| SMART disk health                | `textfile` (smartctl dumped via systemd timer script)      |
| Borg backup status               | `textfile` (borgmatic/borg output dumped via script)       |
| NixOS generation                 | `textfile` (readlink /nix/var/nix/profiles/system)         |

### Nextcloud (built-in Serverinfo API)

| Metric               | API Endpoint (GET /ocs/v2.php/apps/serverinfo/api/v1/info)|
|-----------------------|--------------------------------------------------------|
| Online/reachable      | Status Code 200                                        |
| Version               | `nextcloud.system.version`                             |
| Maintenance mode      | `nextcloud.storage.num_users` (Proxy for up/down)      |
| Storage free          | `nextcloud.system.freespace`                           |

### Home Assistant OS (REST API)

| Metric             | API Endpoint                                          |
|--------------------|-------------------------------------------------------|
| Online/reachable   | `GET /api/`                                           |
| HA Core version    | `GET /api/config` -> `version`                        |
| Running state      | `GET /api/config` -> `state`                          |

### Incus & IncusOS Host (Local Socket)

| Metric                     | API Endpoint                                         |
|----------------------------|------------------------------------------------------|
| Server info (OS, kernel)   | `GET /1.0` (Incus) / `GET /os/1.0` (IncusOS)         |
| CPU, RAM usage             | `GET /1.0/resources`                                 |
| Storage pool usage         | `GET /1.0/storage-pools/{name}/resources`            |
| Container/VM list + status | `GET /1.0/instances` + `GET /1.0/instances/{n}/state`|

### Weather (Open-Meteo)

| Metric                      | API                                                                       |
|-----------------------------|---------------------------------------------------------------------------|
| Current temperature/weather | `GET https://api.open-meteo.com/v1/forecast?...&current_weather=true`     |

## 4. Data Storage

SQLite with WAL mode. Three tables:

| Table      | Contents                                               | Retention |
|------------|--------------------------------------------------------|-----------|
| `machines` | Hostname, type (nixos/nextcloud/ha/incus), first_seen  | Forever   |
| `metrics`  | Machine ID, timestamp, cpu, mem, disk, raw_json_blob   | 30 days   |
| `weather`  | Timestamp, JSON weather data                           | 7 days    |

Migrations are embedded in the Go binary and run on startup.
Retention pruning runs daily via the internal scheduler.

## 5. API Endpoints

| Method | Path                     | Description                          |
|--------|--------------------------|--------------------------------------|
| GET    | `/api/status`            | Latest snapshot of all systems       |
| GET    | `/api/history/:hostname` | 30-day history for a specific host   |
| GET    | `/`                      | Static frontend (HTML/JS/CSS)        |

All endpoints are Tailscale-only (no auth layer on the dashboard itself, network provides trust).

## 6. Technology Choices

| Component        | Choice                    | Rationale                                     |
|------------------|---------------------------|-----------------------------------------------|
| Server language  | Go                        | Memory safe, fast concurrency, static binary  |
| HTTP framework   | `net/http`                | Standard library is sufficient, zero deps     |
| SQLite library   | `mattn/go-sqlite3`        | Stable, requires CGO (built via Nix)          |
| HTTP client      | `net/http`                | Standard library, handles polling easily      |
| Unix socket      | `net.Dial("unix", ...)`   | Native support in Go's HTTP client            |
| Frontend         | HTML + CSS + JS           | Vanilla, Alpine.js, Chart.js embedded in Go   |
| Container        | scratch (OCI image)       | Static binary, ~15MB, nothing to update       |
| Build system     | Nix flake                 | Reproducible, handles CGO SQLite compilation  |
| Agent            | `prometheus-node-exporter`| Standard, reliable, extensible via textfile   |

## 7. Project Structure

```
dashboard/
├── flake.nix                     # Nix flake: Go binary + OCI image + NixOS module
├── flake.lock
├── go.mod
├── go.sum
│
├── cmd/
│   └── dashboard/
│       └── main.go               # Entry: start server + scheduler
├── internal/
│   ├── config/                   # Load TOML config
│   ├── server/                   # HTTP Handlers (status, history)
│   ├── collector/                # Pollers (NodeExporter, HA, Nextcloud, Incus)
│   ├── db/                       # Connection pool, migrations, queries
│   └── scheduler/                # Runs collectors on interval
│
├── static/                       # Frontend (embedded via //go:embed)
│   ├── index.html
│   ├── style.css
│   └── dashboard.js
│
├── nix/
│   ├── package.nix               # Go binary derivation (CGO_ENABLED=1 for sqlite)
│   ├── image.nix                 # OCI scratch container image
│   └── agent-module.nix          # NixOS module for configuring node-exporter
│
├── config.example.toml           # Example server config
└── deploy-dashboard.sh           # Build + deploy script
```

## 8. Configuration

Server config is a TOML file on the persistent volume:

```toml
[server]
listen = "0.0.0.0:8080"
data_dir = "/data"

[weather]
latitude = 52.52
longitude = 13.405
interval_minutes = 30

[probes.nextcloud]
url = "https://cloud.example.com"
token_file = "/secrets/nextcloud-serverinfo-token"
interval_minutes = 15

[probes.homeassistant]
url = "http://homeassistant.tailnet-name.ts.net:8123"
token_file = "/secrets/ha-ro-token"
interval_minutes = 15

[probes.incus]
socket = "/run/incus/incus.sock"
interval_minutes = 15

[[probes.nixos]]
hostname = "server-01"
url = "http://server-01.tailnet-name.ts.net:9100/metrics"
auth_file = "/secrets/node-exporter-auth"
interval_minutes = 5
```

## 9. NixOS Agent Module

In the NixOS flake, add the dashboard flake as an input and import the agent
module in each host config. The module configures `prometheus-node-exporter` and sets up the textfile scripts.

```nix
# hosts/server-01/default.nix
{
  imports = [ inputs.dashboard.nixosModules.agent ];

  services.dashboard-agent = {
    enable = true;
    basicAuthFile = "/root/secrets/node-exporter-auth";
    customChecks = {
      smart = true;
      borgJobs = [ "default" ];
    };
  };
}
```

## 10. Build & Deploy Pipeline

### Dashboard (Go binary -> scratch container -> IncusOS)

```bash
# On any NixOS machine with the repo:
nix build .#dashboard-image     # Produces OCI image tarball
./deploy-dashboard.sh           # Imports image, recreates container on IncusOS
```

`deploy-dashboard.sh` performs:
1. `nix build .#dashboard-image`
2. `incus image import ./result --alias dashboard-new`
3. `incus stop dashboard && incus rm dashboard`
4. `incus launch dashboard-new dashboard` with volume, socket, and secret mounts (running as non-root user).
5. Clean up old image

### Agent Updates

Updates to the `node-exporter` configuration or textfile scripts are handled via standard `nixos-rebuild switch`.
