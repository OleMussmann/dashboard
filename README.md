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
- The IncusOS host configured as an Incus remote on your local machine
  (see `incus remote add`)

## Initial Setup (Step by Step)

Follow these steps in order when setting up the project from scratch.

### 1. Create an ntfy.sh topic

Go to <https://ntfy.sh> and pick a private topic name (e.g.,
`homelab-dashboard-alerts`). Note the full URL
(`https://ntfy.sh/your-topic-name`) -- you will need it for `config.toml`.

### 2. Switch to the IncusOS remote and create storage volumes

IncusOS has an immutable root filesystem -- you cannot SSH in or write files
to the host directly. All interaction happens via the Incus API. Switch your
default remote to the IncusOS host so that all subsequent `incus` commands
target it automatically:

```bash
incus remote switch incus-host
```

Create custom storage volumes to hold secrets, config, and Homepage config.
These volumes persist across container rebuilds.

```bash
incus storage volume create local dashboard-secrets
incus storage volume create local dashboard-config
incus storage volume create local homepage-config
```

### 3. Generate Incus metrics TLS certificate

Generate a metrics-only TLS client certificate on your local machine and push
it into the `dashboard-secrets` storage volume. To push files into a volume
we use a temporary helper container.

**Generate the certificate locally:**

```bash
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:secp384r1 \
  -sha384 -keyout metrics.key -out metrics.crt -nodes -days 3650 \
  -subj "/CN=dashboard-metrics"
```

**Trust the certificate in Incus:**

The command reads the local file and sends it to the (now default) remote
server via the API -- no need to copy the cert to the host:

```bash
incus config trust add-certificate metrics.crt --type=metrics
```

**Push secrets into the storage volume using a helper container:**

To write files into a storage volume, launch a temporary helper container,
attach the volume, push the files via the Incus API, then stop the helper
(ephemeral containers are deleted on stop):

```bash
incus launch images:alpine/edge helper --ephemeral
incus storage volume attach local dashboard-secrets helper /mnt
incus file push metrics.crt helper/mnt/metrics.crt
incus file push metrics.key helper/mnt/metrics.key
incus stop helper
```

### 4. Create the node-exporter Basic Auth password

Generate a password and its bcrypt hash. The plaintext password goes into the
`dashboard-secrets` volume (so the dashboard-api container can authenticate
against node-exporter). The bcrypt hash goes into your NixOS `agenix` secrets
(so node-exporter can verify incoming requests).

```bash
# Generate a random password
PASS=$(openssl rand -base64 24)
echo -n "$PASS" > node-exporter-pass

# Generate bcrypt hash for agenix (needs htpasswd or similar)
htpasswd -nbBC 10 "" "$PASS" | tr -d ':\n'
```

Push the plaintext password into the secrets volume using a helper container
(same approach as step 3):

```bash
incus launch images:alpine/edge helper --ephemeral
incus storage volume attach local dashboard-secrets helper /mnt
incus file push node-exporter-pass helper/mnt/node-exporter-pass
incus stop helper
```

Store the bcrypt hash via `agenix` in your NixOS configurations. Every
monitored machine needs access to this secret so that node-exporter can be
configured with Basic Auth.

### 5. Add the dashboard flake input to your NixOS configs

In your NixOS flake (the one managing your machines), add this repository as
an input:

```nix
inputs.dashboard.url = "github:ole/dashboard"; # or a local path
```

Then import the agent module on each machine you want to monitor:

```nix
# hosts/server-01/default.nix
{
  imports = [ inputs.dashboard.nixosModules.agent ];

  services.dashboard-agent = {
    enable = true;
    basicAuthPasswordFile = config.age.secrets."node-exporter-pass".path;
    customChecks = {
      smart = true;        # if the machine has physical disks
      borgJobs = [ "default" ];  # if the machine runs borgmatic
    };
  };
}
```

This enables `prometheus-node-exporter` with Basic Auth, the `systemd`
collector, and textfile scripts for SMART, Borg, NixOS generation, and reboot
detection.

Apply on each machine:

```bash
nixos-rebuild switch
```

### 6. Create config.toml

```bash
cp config.example.toml config.toml
```

Fill in:
- Your actual Tailscale hostnames (e.g., `server-01.tailnet-name.ts.net`)
- The Incus host URL
- The ntfy.sh topic URL from step 1
- Paths to the TLS cert/key from step 3 (inside the container these are
  `/secrets/metrics.crt` and `/secrets/metrics.key`)
- Path to the node-exporter password file (`/secrets/node-exporter-pass`)
- Set `critical = true/false` for each machine

See `config.example.toml` for full documentation of all options.

Push `config.toml` into the `dashboard-config` storage volume:

```bash
incus launch images:alpine/edge helper --ephemeral
incus storage volume attach local dashboard-config helper /mnt
incus file push config.toml helper/mnt/config.toml
incus stop helper
```

### 7. Update Homepage config templates

Edit `homepage/services.yaml`:
- Replace placeholder hostnames with your real machine names
- Replace `cloud.example.com` with your real Nextcloud URL
- Replace `homeassistant.tailnet-name.ts.net` with your real Home Assistant URL

Edit `homepage/widgets.yaml`:
- Set your latitude/longitude and timezone

You will also need:
- A Nextcloud Serverinfo API token (from NC admin settings)
- A Home Assistant long-lived access token (from your HA profile page)

Push the homepage config files into the `homepage-config` storage volume:

```bash
incus launch images:alpine/edge helper --ephemeral
incus storage volume attach local homepage-config helper /mnt
incus file push -r homepage/ helper/mnt/
incus stop helper
```

### 8. First-time Incus container setup

Build the dashboard-api image locally, import it into the remote, then create
both containers with the storage volumes attached.

```bash
# Build the OCI image locally
nix build .#dashboard-api-image

# Import the image into the (default) remote Incus server
incus image import ./result --alias dashboard-api

# Dashboard API container
incus launch dashboard-api dashboard-api
incus storage volume attach local dashboard-secrets dashboard-api /secrets
incus storage volume attach local dashboard-config dashboard-api /config

# Homepage container
incus launch docker:ghcr.io/gethomepage/homepage:latest homepage
incus storage volume attach local homepage-config homepage /app/config
```

Verify the API is running:

```bash
curl http://dashboard-api.incus:8080/healthz
```

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

### Update Homepage

```bash
incus stop homepage
incus rebuild docker:ghcr.io/gethomepage/homepage:latest homepage
incus start homepage
```

The config is attached as a storage volume, so it survives rebuilds.

### Update NixOS agents

On each monitored machine:

```bash
nixos-rebuild switch
```

## Secrets

Secrets are stored in the `dashboard-secrets` Incus storage volume and
attached to the dashboard-api container at `/secrets/`. The node-exporter
bcrypt hash is managed via `agenix` in your NixOS configurations.

| Secret                  | Location                       | Purpose                                     |
|-------------------------|--------------------------------|---------------------------------------------|
| `metrics.crt`           | `dashboard-secrets` volume     | TLS client cert for Incus metrics endpoint  |
| `metrics.key`           | `dashboard-secrets` volume     | TLS client key for Incus metrics endpoint   |
| `node-exporter-pass`    | `dashboard-secrets` volume     | Basic Auth password for node-exporter       |

The `config.toml` file is stored in the `dashboard-config` Incus storage
volume. Do not commit `config.toml` to version control (it is in
`.gitignore`).

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
