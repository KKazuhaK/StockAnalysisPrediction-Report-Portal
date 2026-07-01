#!/bin/sh
# 以 root 启动：首启种下默认配置、修正挂载属主，再 gosu 降权到 PUID/PGID 运行。
set -e
PUID="${PUID:-10001}"
PGID="${PGID:-10001}"

mkdir -p /app/config /app/data

# 首次运行：把镜像内置的示例配置种到 ./config，供宿主机直接编辑。
if [ ! -f /app/config/config.yaml ] && [ -f /app/config.example.yaml ]; then
  cp /app/config.example.yaml /app/config/config.yaml
  echo "[entrypoint] 已生成默认 /app/config/config.yaml —— 请编辑(secret_key/账号/旧门户) 后 docker compose restart"
fi

# Docker 不会 chown bind mount，这里自愈，避免非 root 进程读写 config/data 失败。
chown -R "$PUID:$PGID" /app/config /app/data 2>/dev/null || true

exec gosu "$PUID:$PGID" /app/report-portal
