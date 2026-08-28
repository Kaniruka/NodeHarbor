#!/system/bin/sh

MODDIR=${0%/*}
DATA_DIR="$MODDIR/data"
LOG_DIR="$MODDIR/logs"
PID_FILE="$MODDIR/nodeharbor.pid"
mkdir -p "$DATA_DIR" "$LOG_DIR"

# NodeHarbor owns only this PID and its sibling nodeharbor-core. It does not
# inspect, stop, or modify Surfing, firewall rules, routes, DNS, or TUN state.
if [ -f "$PID_FILE" ] && kill -0 "$(cat "$PID_FILE")" 2>/dev/null; then
  exit 0
fi
nohup "$MODDIR/bin/nodeharbor" --listen 127.0.0.1:9876 --data "$DATA_DIR" --open-browser=false >"$LOG_DIR/nodeharbor.log" 2>&1 &
echo $! >"$PID_FILE"
