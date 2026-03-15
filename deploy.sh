#!/usr/bin/env bash
# Build and deploy the dashboard-api container and push config files to Incus.
# Usage: ./deploy.sh [--config-only]
#
# By default, rebuilds the dashboard-api container AND pushes all config files.
# With --config-only, skips the container rebuild and only pushes config files.
set -euo pipefail

CONFIG_ONLY=false
if [[ "${1:-}" == "--config-only" ]]; then
  CONFIG_ONLY=true
fi

# --- Dashboard API container rebuild ---
if [[ "$CONFIG_ONLY" == false ]]; then
  echo "==> Building dashboard-api image..."
  nix build .#dashboard-api-image

  echo "==> Importing image into Incus..."
  incus image import ./result --alias dashboard-api-new

  echo "==> Stopping dashboard-api container..."
  incus stop dashboard-api --force || true

  echo "==> Rebuilding container from new image..."
  incus rebuild dashboard-api-new dashboard-api

  echo "==> Starting dashboard-api container..."
  incus start dashboard-api

  echo "==> Cleaning up old image alias..."
  incus image delete dashboard-api-new || true
fi

# --- Push config.toml to dashboard-config volume ---
if [[ -f config.toml ]]; then
  echo "==> Pushing config.toml to dashboard-api container..."
  incus file push config.toml dashboard-api/config/config.toml
else
  echo "==> Skipping config.toml (file not found locally)"
fi

# --- Push homepage config files to homepage container ---
HOMEPAGE_FILES=(services.yaml settings.yaml widgets.yaml bookmarks.yaml docker.yaml)
HOMEPAGE_CHANGED=false

for f in "${HOMEPAGE_FILES[@]}"; do
  if [[ -f "homepage/$f" ]]; then
    echo "==> Pushing homepage/$f..."
    incus file push "homepage/$f" "homepage/app/config/$f"
    HOMEPAGE_CHANGED=true
  fi
done

if [[ "$HOMEPAGE_CHANGED" == true ]]; then
  echo "==> Restarting homepage to pick up config changes..."
  incus restart homepage
fi

echo "==> Done."
