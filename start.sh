#!/bin/sh
set -e

#!/bin/sh
set -e

if [ -z "${RF4_GATEWAY_ADVERTISE_HOST:-}" ]; then
  echo "[ERROR] RF4_GATEWAY_ADVERTISE_HOST 未设置或为空，请在 .env 中填写宿主机 IP。" >&2
  exit 1
fi

if ! printf '%s\n' "$RF4_GATEWAY_ADVERTISE_HOST" | awk -F. '
  NF != 4 { exit 1 }
  { for (i = 1; i <= NF; i++) if ($i !~ /^(0|[1-9][0-9]{0,2})$/ || $i + 0 > 255) exit 1 }
'; then
  echo "[ERROR] RF4_GATEWAY_ADVERTISE_HOST='$RF4_GATEWAY_ADVERTISE_HOST' 不是合法 IPv4，请检查 .env。" >&2
  exit 1
fi

export RF4_GATEWAY_ADVERTISE_HOST



CONFIG=/app/config.yaml

echo "=== RF4 server starting ==="
echo "Working dir: $(pwd)"
if [ -f "$CONFIG" ]; then
  echo "Config file: $CONFIG"
else
  echo "ERROR: $CONFIG not found"
  exit 1
fi

# 可选：首次运行时初始化数据库
# if [ ! -f /app/data/rf4server.db ]; then
#   echo "Initializing database with seed..."
#   /app/seed --config "$CONFIG"
# fi

# gameserver 先启动，监听内部隧道 127.0.0.1:9401
echo "Starting gameserver..."
/app/gameserver --config "$CONFIG" &

# 等隧道端口就绪（可选，最多等 10 秒）
for i in $(seq 1 20); do
  if nc -z 127.0.0.1 9401 2>/dev/null; then
    echo "gameserver tunnel 9401 is ready"
    break
  fi
  sleep 0.5
done

# gateway 连接 gameserver 隧道，并对外监听 443 / 9341
echo "Starting gateway..."
/app/gateway --config "$CONFIG" &

# 管理后台监听 8080
echo "Starting admin..."
/app/admin --config "$CONFIG" &

echo "All services started, waiting..."
wait