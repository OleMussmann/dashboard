# Dashboard Startpage — Project Plan

## 1. Overview

A self-hosted browser startpage that displays the health and status of all machines
and services in a homelab. A single Rust binary runs on IncusOS as a scratch
container, accepts status reports from NixOS agents, polls external systems
(Nextcloud, Home Assistant, Incus, weather), stores 30 days of history, and serves
a static frontend.

All communication happens over Tailscale. No public internet exposure.

## 2. Architecture

```
 NixOS machines (6-15)
   systemd timer (every 15 min)
   -> agent script collects local metrics
   -> POST /api/checkin to dashboard over Tailscale
        |
        v  Tailscale
 IncusOS Host
  +-- Container: "dashboard" (scratch/distroless) ----------------+
  |                                                                |
  |  Single static Rust binary                                     |
  |                                                                |
  |  Server Module:                                                |
  |    POST /api/checkin    <- accepts agent data                  |
  |    GET  /api/status     <- latest state, all systems           |
  |    GET  /api/history/:h <- 30-day trends for sparklines        |
  |    GET  /               <- serves static frontend              |
  |    SQLite database (persistent volume)                         |
  |                                                                |
  |  Collector Module (internal scheduler, every 15 min):          |
  |    -> Nextcloud OCS API (app password)                         |
  |    -> Home Assistant REST + Supervisor API (token)             |
  |    -> Incus REST API (local Unix socket, no creds)             |
  |    -> Open-Meteo (no creds)                                    |
  |    Feeds results into server ingest (internal call)            |
  |                                                                |
  |  Mounts:                                                       |
  |    /data/          <- persistent volume (SQLite DB)            |
  |    /run/incus/     <- Incus Unix socket from host              |
  |    /secrets/       <- read-only (NC app password, HA token)    |
  +----------------------------------------------------------------+
```

### Data Flow

| Source              | Method | Transport          | Credentials                      |
|---------------------|--------|--------------------|----------------------------------|
| NixOS machines      | Push   | Tailscale          | None (Tailscale = network auth)  |
| Incus host          | Pull   | Unix socket        | None                             |
| Nextcloud (hosted)  | Pull   | HTTPS (public)     | App password (in /secrets/)      |
| Home Assistant OS   | Pull   | Tailscale          | Long-lived token (in /secrets/)  |
| Open-Meteo          | Pull   | HTTPS (public)     | None                             |

### Design Decisions & Rationale

- **Push model for NixOS machines**: Agents POST to the server. No inbound firewall
  rules needed on clients, machines behind NAT/VPN work, "last seen" is trivially
  derived from the last checkin timestamp.
- **Pull model for Nextcloud/HA/Incus**: These systems can't run custom agents
  (hosted service, appliance OS, immutable OS). The dashboard server polls their
  APIs directly.
- **Single binary, logically split**: The collector module and server module are
  separate Rust modules compiled into one binary. This avoids needing cron or an
  init system in the scratch container while keeping the code cleanly separated.
- **Tailscale-only access**: The dashboard is only reachable via Tailscale. No
  Funnel, no public exposure, no auth layer needed. The threat model is minimal:
  an attacker would need to compromise a Tailscale node first.
- **Credentials on the dashboard server are acceptable**: Given Tailscale-only
  access, storing read-only Nextcloud/HA credentials on the dashboard is low risk.
  Incus uses a local socket (no credentials at all).
- **Dashboard runs on IncusOS**: Gets free Incus monitoring via local socket mount.
  Deployed as a scratch container with a persistent volume for SQLite.
- **Nix flake for builds**: Reproducible builds on any NixOS machine. The deploy
  step is a simple script.
- **No existing framework fits**: Evaluated Homepage, Dashy, Uptime Kuma, Glances,
  Netdata, Homer, and Checkmk. None cover the combination of push-based ingest +
  arbitrary structured data + 30-day historical storage + custom probes. Building
  from scratch (~1500-2500 lines) is less effort than adapting any framework.

## 3. Monitored Systems & Metrics

### NixOS Machines (agent-reported via push)

| Metric                           | Source on machine                                          |
|----------------------------------|------------------------------------------------------------|
| Online status                    | Derived from last checkin timestamp                        |
| NixOS version, kernel            | `nixos-version`, `uname -r`                               |
| NixOS generation (current+total) | `readlink /nix/var/nix/profiles/system`, profile listing   |
| Last rebuild                     | `stat /nix/var/nix/profiles/system` or journal             |
| Uptime                           | `/proc/uptime`                                             |
| Disk usage (all mounts)          | `df`                                                       |
| SMART disk health                | `smartctl --json` (health, temp, reallocated sectors, hrs) |
| Borg backup status               | `systemctl show borgbackup-job-<name>` (time, exit status) |
| Failed systemd services          | `systemctl list-units --failed`                            |
| Docker/Podman containers         | `docker ps --format json` / `podman ps --format json`      |
| OS warnings                      | Journal warnings, failed units, degraded state             |

### Nextcloud (hosted, collector-polled)

| Metric               | API Endpoint                                           |
|-----------------------|--------------------------------------------------------|
| Online/reachable      | `GET /status.php`                                      |
| Version               | `GET /status.php` -> `version` field                   |
| Maintenance mode      | `GET /status.php` -> `maintenance` field               |
| Storage quota/free    | `GET /ocs/v1.php/cloud/users/{user}` -> `quota` object |

### Home Assistant OS (collector-polled)

| Metric             | API Endpoint                                          |
|--------------------|-------------------------------------------------------|
| Online/reachable   | `GET /api/`                                           |
| HA Core version    | `GET /api/hassio/core/info`                           |
| HAOS version       | `GET /api/hassio/host/info`                           |
| Disk usage         | `GET /api/hassio/host/info` -> `disk_total/used/free` |
| Running state      | `GET /api/hassio/core/info` -> `state`                |
| Add-on status      | `GET /api/hassio/addons` -> name + state per add-on   |

### Incus Host (collector-polled via local socket)

| Metric                     | API Endpoint                                         |
|----------------------------|------------------------------------------------------|
| Server info (OS, kernel)   | `GET /1.0`                                           |
| CPU, RAM usage             | `GET /1.0/resources`                                 |
| Storage pool usage         | `GET /1.0/storage-pools/{name}/resources`            |
| Container/VM list + status | `GET /1.0/instances` + `GET /1.0/instances/{n}/state`|

### Weather (Open-Meteo)

| Metric                      | API                                                                       |
|-----------------------------|---------------------------------------------------------------------------|
| Current temperature/weather | `GET https://api.open-meteo.com/v1/forecast?...&current_weather=true`     |

## 4. Agent Checkin Payload

```json
{
  "hostname": "server-01",
  "timestamp": "2026-03-11T14:00:00Z",
  "os": {
    "nixos_version": "24.11",
    "nixos_generation": 187,
    "total_generations": 42,
    "last_rebuild": "2026-03-10T09:15:00Z",
    "kernel": "6.12.8",
    "uptime_seconds": 432000,
    "warnings": []
  },
  "disks": [
    { "mount": "/", "total_gb": 500, "used_gb": 210, "fs": "btrfs" }
  ],
  "smart": [
    {
      "device": "/dev/sda",
      "model": "Samsung 870 EVO",
      "health": "PASSED",
      "temperature_c": 34,
      "reallocated_sectors": 0,
      "power_on_hours": 12450
    }
  ],
  "backups": {
    "last_successful": "2026-03-11T03:00:00Z",
    "backend": "borg",
    "status": "ok"
  },
  "services": {
    "systemd_failed": [],
    "systemd_degraded": false
  },
  "containers": {
    "docker": [
      { "name": "nextcloud", "status": "running", "image": "nextcloud:28" }
    ],
    "podman": []
  }
}
```

External systems (Nextcloud, HA, Incus) produce similar JSON structures,
normalized by the collector before ingest.

## 5. Data Storage

SQLite with WAL mode. Three tables:

| Table      | Contents                                               | Retention |
|------------|--------------------------------------------------------|-----------|
| `machines` | Hostname, type (nixos/nextcloud/ha/incus), first_seen  | Forever   |
| `checkins` | Machine ID, timestamp, full JSON payload               | 30 days   |
| `weather`  | Timestamp, JSON weather data                           | 7 days    |

Migrations are embedded in the Rust binary and run on startup.
Retention pruning runs daily via the internal scheduler.

## 6. API Endpoints

| Method | Path                     | Description                          |
|--------|--------------------------|--------------------------------------|
| POST   | `/api/checkin`           | Accept agent/collector data          |
| GET    | `/api/status`            | Latest snapshot of all systems       |
| GET    | `/api/history/:hostname` | 30-day history for a specific host   |
| GET    | `/`                      | Static frontend (HTML/JS/CSS)        |

All endpoints are Tailscale-only (no auth layer).

## 7. Technology Choices

| Component        | Choice                    | Rationale                                     |
|------------------|---------------------------|-----------------------------------------------|
| Server language  | Rust                      | Single static binary, low resources, musl     |
| HTTP framework   | axum or actix-web         | Mature, async, well-supported                 |
| SQLite library   | rusqlite or sqlx (SQLite) | Embedded, no external deps                    |
| HTTP client      | reqwest                   | For polling NC, HA, Open-Meteo APIs           |
| Unix socket      | hyper with Unix connector | For Incus local socket API                    |
| Scheduler        | tokio interval timer      | Built into async runtime                      |
| Frontend         | Plain HTML + JS + CSS     | No build step, embedded in binary             |
| Container        | scratch (OCI image)       | Static binary, ~5-10MB, nothing to update     |
| Build system     | Nix flake                 | Reproducible, cross-compile to musl           |
| Agent            | Bash script               | Simple, uses standard NixOS tools             |

## 8. Project Structure

```
dashboard/
├── flake.nix                     # Nix flake: binary + OCI image + agent module
├── flake.lock
├── Cargo.toml
├── Cargo.lock
│
├── src/                          # Rust source
│   ├── main.rs                   # Entry: start server + scheduler
│   ├── config.rs                 # Load TOML config
│   ├── server/
│   │   ├── mod.rs
│   │   ├── checkin.rs            # POST /api/checkin handler
│   │   ├── status.rs             # GET /api/status handler
│   │   └── history.rs            # GET /api/history/:host handler
│   ├── collector/
│   │   ├── mod.rs
│   │   ├── nextcloud.rs          # Nextcloud OCS API poller
│   │   ├── homeassistant.rs      # HA REST API poller
│   │   ├── incus.rs              # Incus socket API poller
│   │   └── weather.rs            # Open-Meteo poller
│   ├── db/
│   │   ├── mod.rs                # Connection pool, migrations
│   │   ├── migrations/           # SQL migration files
│   │   └── models.rs             # Data structures
│   └── scheduler.rs              # Runs collectors on interval
│
├── static/                       # Frontend (embedded in binary at compile time)
│   ├── index.html
│   ├── style.css
│   └── dashboard.js
│
├── nix/
│   ├── package.nix               # Rust binary derivation (musl, static)
│   ├── image.nix                 # OCI scratch container image
│   └── agent-module.nix          # NixOS module for agent
│
├── agent/
│   └── checkin.sh                # Agent bash script
│
├── config.example.toml           # Example server config
└── deploy-dashboard.sh           # Build + deploy script
```

## 9. Configuration

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
user = "admin"
password_file = "/secrets/nextcloud-app-password"
interval_minutes = 15

[probes.homeassistant]
url = "http://homeassistant.tailnet-name.ts.net:8123"
token_file = "/secrets/ha-token"
interval_minutes = 15

[probes.incus]
socket = "/run/incus/incus.sock"
interval_minutes = 15
```

## 10. NixOS Agent Module

In the NixOS flake, add the dashboard flake as an input and import the agent
module in each host config:

```nix
# hosts/server-01/default.nix
{
  imports = [ inputs.dashboard.nixosModules.agent ];

  services.dashboard-agent = {
    enable = true;
    serverUrl = "http://dashboard.tailnet-name.ts.net:8080";
    interval = "15min";
    checks = {
      smart = true;
      docker = true;
      borgJobs = [ "default" ];
    };
  };
}
```

All NixOS machines are managed from a single flake. Agent updates are part of the
normal `nixos-rebuild switch` cycle.

## 11. Build & Deploy Pipeline

### Dashboard (Rust binary -> scratch container -> IncusOS)

```bash
# On any NixOS machine with the repo:
nix build .#dashboard-image     # Produces OCI image tarball
./deploy-dashboard.sh           # Imports image, recreates container on IncusOS
```

`deploy-dashboard.sh` performs:
1. `nix build .#dashboard-image`
2. `incus image import ./result --alias dashboard-new`
3. `incus stop dashboard && incus rm dashboard`
4. `incus launch dashboard-new dashboard` with volume/socket/secrets mounts
5. Clean up old image

### Agent (NixOS module -> nixos-rebuild)

```bash
nixos-rebuild switch --flake .#server-01
```

### Optional: GitHub Actions CI

```
on push to main:
  -> nix build .#dashboard        (check it compiles)
  -> nix build .#dashboard-image  (produce artifact)
  -> optionally: deploy to IncusOS via Tailscale SSH
```

The project repo is hosted on GitHub. CI can be added later; manual
`deploy-dashboard.sh` is sufficient to start.

### Upgrade & Rollback

| Scenario           | How to handle                                                  |
|--------------------|----------------------------------------------------------------|
| Dashboard update   | Run deploy-dashboard.sh. Incus snapshots for rollback.         |
| Agent update       | nixos-rebuild switch. Rollback: boot previous NixOS generation.|
| Database migration | Rust binary runs migrations on startup. Backup .db first.      |
| Config change      | Edit config.toml on persistent volume, restart container.      |

## 12. Secrets Required

| Secret                  | Location                        | How to obtain                          |
|-------------------------|---------------------------------|----------------------------------------|
| Nextcloud app password  | /secrets/nextcloud-app-password | NC -> Settings -> Security -> App Pwds |
| Home Assistant token    | /secrets/ha-token               | HA -> Profile -> Long-Lived Tokens     |
| Incus API               | N/A                             | Local Unix socket (mounted)            |
| Open-Meteo              | N/A                             | Free public API                        |

## 13. Frontend / UI Wireframe

```
+------------------------------------------------------------------+
|  DASHBOARD                                    Sun 12C Berlin      |
+------------------------------------------------------------------+
|                                                                   |
|  +- server-01 (NixOS) ---------------------- * ONLINE ----------+|
|  | NixOS 24.11 gen 187 | Up 5d | Rebuilt 1d ago                 ||
|  | Disks: / 42% ====--  /data 72% =======- (sparkline)          ||
|  | SMART: sda OK 34C | sdb OK 31C                               ||
|  | Backup: borg OK 11h ago | Services: all OK                   ||
|  | Docker: nextcloud ok  postgres ok  redis ok                   ||
|  +---------------------------------------------------------------+|
|                                                                   |
|  +- desktop-02 (NixOS) --------------------- * ONLINE ----------+|
|  | NixOS 24.11 gen 93 | Up 2d | Rebuilt 3d ago                  ||
|  | Disks: / 31% ===-  | SMART: nvme0 OK 38C                    ||
|  | Backup: borg OK 8h ago | Services: all OK                    ||
|  +---------------------------------------------------------------+|
|                                                                   |
|  +- laptop-03 (NixOS) -------------- o OFFLINE (2h ago) --------+|
|  | NixOS 24.11 gen 64 | Backup: 2h ago | Disks: / 55%          ||
|  +---------------------------------------------------------------+|
|                                                                   |
|  +- Nextcloud (hosted) --------------------- * REACHABLE -------+|
|  | Version 28.0.4 | Maintenance: off                            ||
|  | Storage: 145 GB free of 500 GB (71% used) (sparkline)        ||
|  +---------------------------------------------------------------+|
|                                                                   |
|  +- Home Assistant ----------------------- * RUNNING -----------+|
|  | HA Core 2026.3.1 | HAOS 14.1 | Disk: 12 / 32 GB             ||
|  | Add-ons: Mosquitto ok  Z-Wave ok  File Editor ok              ||
|  +---------------------------------------------------------------+|
|                                                                   |
|  +- Incus Host (IncusOS) ------------------- * ONLINE ----------+|
|  | IncusOS 0.5 | Kernel 6.12.8                                  ||
|  | CPU: 12% | RAM: 18/64 GB | Pool: 400/1000 GB                ||
|  | Instances: vm-dev ok  ct-build ok  ct-dns ok  vm-win off      ||
|  +---------------------------------------------------------------+|
|                                                                   |
+------------------------------------------------------------------+
```

Cards show:
- Colored status indicator (green = online, yellow = warning, red = offline/error)
- Disk usage as bar charts with 30-day sparklines
- Compact layout: most important info at a glance
- Responsive: usable on phone and desktop

## 14. Implementation Order

1.  Rust project scaffold: Cargo workspace, axum server, SQLite setup, embedded static files
2.  Checkin API: POST /api/checkin + SQLite storage + GET /api/status
3.  Agent script: Bash script collecting NixOS metrics, POST to server
4.  NixOS agent module: systemd timer + service wrapping the script
5.  Collector: Incus (local socket, easiest probe for testing)
6.  Collector: Nextcloud (OCS API poller)
7.  Collector: Home Assistant (REST API poller)
8.  Collector: Weather (Open-Meteo poller)
9.  History API: GET /api/history/:host + retention pruning
10. Frontend: HTML/JS/CSS dashboard with cards, bars, sparklines
11. Nix packaging: flake with binary derivation + OCI image + agent module
12. Deploy script: deploy-dashboard.sh
13. Polish: error handling, logging, config validation
