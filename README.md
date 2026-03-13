# Dashboard Startpage

A self-hosted browser startpage for homelab monitoring. Two containers run on
IncusOS:

- **Homepage** ([gethomepage.dev](https://gethomepage.dev)): Frontend startpage
  with built-in widgets for Nextcloud, Home Assistant, and weather. Uses
  `customapi` widgets for NixOS and Incus data from the Go backend.
- **dashboard-api**: A statically-compiled Go binary (scratch container) that
  polls NixOS machines via Prometheus Node Exporter and scrapes the Incus metrics
  endpoint. Serves a JSON API for Homepage to consume.

No database. No historical data. Current state only. All communication over
Tailscale.

## Prerequisites

- NixOS host (for development and agent deployment)
- Incus on the target host (IncusOS)
- Tailscale network connecting all machines
- `agenix` for secrets management in your NixOS configurations

## Development

Enter the dev shell (provides Go, gopls, and other tools):

```bash
nix develop
```

Build the Go binary locally:

```bash
go build -o dashboard-api ./cmd/dashboard-api
```

Run with a config file:

```bash
./dashboard-api -config config.toml
```

## Configuration

Copy the example config and edit it for your environment:

```bash
cp config.example.toml config.toml
```

Key configuration sections:

- `[server]` -- Listen address and global poll interval.
- `[incus]` -- Incus metrics endpoint URL and TLS certificate paths.
- `[alerting]` -- ntfy.sh URL, cooldown, and alert rules.
- `[[nixos]]` -- One entry per monitored NixOS machine with hostname, URL,
  credentials, and criticality flag.

See `config.example.toml` for full documentation of all options.

## NixOS Agent Setup

On each monitored NixOS machine, import the agent module in your configuration:

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

This enables `prometheus-node-exporter` with Basic Auth, the `systemd`
collector, and textfile scripts for SMART, Borg, NixOS generation, and reboot
detection.

After adding the module, apply with `nixos-rebuild switch`.

## Build & Deploy

### Build the OCI image

```bash
nix build .#dashboard-api-image
```

### Deploy the Go API container

```bash
# Import the image into Incus
incus image import ./result --alias dashboard-api-new

# Recreate the container
incus stop dashboard-api
incus rebuild dashboard-api-new dashboard-api
incus start dashboard-api
```

The Go binary boots in milliseconds. Homepage handles the brief API outage
gracefully.

### First-time container setup

Create the dashboard-api container and mount secrets:

```bash
incus launch dashboard-api-new dashboard-api
incus config device add dashboard-api secrets disk \
  source=/path/to/secrets path=/secrets readonly=true
```

Create the Homepage container and mount config:

```bash
incus launch docker:ghcr.io/gethomepage/homepage:latest homepage
incus config device add homepage config disk \
  source=/absolute/path/to/dashboard/homepage path=/app/config
```

### Update Homepage

```bash
incus stop homepage
incus rebuild docker:ghcr.io/gethomepage/homepage:latest homepage
incus start homepage
```

The config directory is mounted as a disk device, so it survives rebuilds.

### Update NixOS agents

On each monitored machine:

```bash
nixos-rebuild switch
```

## Secrets

All secrets are managed via `agenix` in your NixOS configurations and mounted
read-only into the dashboard-api container at `/secrets/`:

| Secret                  | Purpose                                     |
|-------------------------|---------------------------------------------|
| `metrics.crt`           | TLS client cert for Incus metrics endpoint  |
| `metrics.key`           | TLS client key for Incus metrics endpoint   |
| `node-exporter-pass`    | Basic Auth password for node-exporter       |

The `config.toml` file also contains the ntfy.sh topic URL. Do not commit
`config.toml` to version control (it is in `.gitignore`).

## API Endpoints

| Method | Path                       | Description                           |
|--------|----------------------------|---------------------------------------|
| GET    | `/api/v1/status`           | All NixOS machines + Incus metrics    |
| GET    | `/api/v1/status/:hostname` | Single machine metrics                |
| GET    | `/healthz`                 | Health check (returns 200 if running) |

## Alerting

The Go backend sends push notifications via [ntfy.sh](https://ntfy.sh) for
critical events. Configure alert rules and per-machine criticality in
`config.toml`.

Machines marked `critical = false` (e.g., laptops, desktops) will not trigger
offline alerts but will still trigger hardware/service alerts (SMART failures,
OOM kills, failed systemd units).

Alert cooldown prevents notification spam: the same (host, event) pair will not
fire again within the configured cooldown period (default: 15 minutes).

## Architecture

See `.opencode/plans/dashboard-plan.md` for the full implementation plan,
including architecture diagrams, data flow, design decisions, and detailed
metric tables.
