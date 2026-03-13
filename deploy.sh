#!/usr/bin/env bash
# Build and deploy the dashboard-api container to Incus.
# Usage: ./deploy.sh
set -euo pipefail

echo "==> Building dashboard-api OCI image..."
nix build .#dashboard-api-image

echo "==> Importing image into Incus..."
incus image import ./result --alias dashboard-api-new

echo "==> Stopping dashboard-api container..."
incus stop dashboard-api || true

echo "==> Rebuilding container from new image..."
incus rebuild dashboard-api-new dashboard-api

echo "==> Starting dashboard-api container..."
incus start dashboard-api

echo "==> Cleaning up old image alias..."
incus image delete dashboard-api-new || true

echo "==> Done. Dashboard API is running."
