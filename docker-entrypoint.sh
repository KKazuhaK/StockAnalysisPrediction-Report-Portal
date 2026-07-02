#!/bin/sh
# Start as root: seed the default config on first run, fix bind-mount ownership,
# then drop privileges to PUID/PGID via gosu.
set -e
PUID="${PUID:-10001}"
PGID="${PGID:-10001}"

mkdir -p /app/config /app/data

# First run: seed the image's example config into ./config for host-side editing.
if [ ! -f /app/config/config.yaml ] && [ -f /app/config.example.yaml ]; then
  cp /app/config.example.yaml /app/config/config.yaml
  echo "[entrypoint] generated default /app/config/config.yaml — edit it (secret_key/accounts/legacy portal), then docker compose restart"
fi

# Docker does not chown bind mounts; self-heal here so the non-root process can read/write config/data.
chown -R "$PUID:$PGID" /app/config /app/data 2>/dev/null || true

exec gosu "$PUID:$PGID" /app/report-portal
