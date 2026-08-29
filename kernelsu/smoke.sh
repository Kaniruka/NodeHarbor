#!/system/bin/sh

MODDIR=${0%/*}
. "$MODDIR/nodeharbor-lifecycle.sh"

request() {
  url="$1"
  if command -v curl >/dev/null 2>&1; then
    curl --noproxy '*' --fail --silent --show-error "$url"
  else
    wget -qO- "$url"
  fi
}

was_running=0
if nodeharbor_pid_is_owned "$(cat "$PID_FILE" 2>/dev/null)"; then was_running=1; fi
nodeharbor_start || { echo 'NodeHarbor failed to start' >&2; exit 1; }
base_url="http://127.0.0.1:9876"
module_version=$(sed -n 's/^version=//p' "$MODDIR/module.prop")
nodeharbor_version=$("$NODEHARBOR_BIN" --version 2>/dev/null)
[ "$nodeharbor_version" = "$module_version" ] || { echo "NodeHarbor version mismatch: $nodeharbor_version" >&2; exit 1; }
grep -q '"version": "v1.19.30"' "$MODDIR/bin/nodeharbor-core.json" || { echo 'Mihomo version metadata is invalid' >&2; exit 1; }
health=$(request "$base_url/api/health") || { echo 'health endpoint failed' >&2; exit 1; }
case "$health" in *'"status":"healthy"'*) ;; *) echo "unhealthy response: $health" >&2; exit 1 ;; esac
request "$base_url/" | grep -q 'id="root"' || { echo 'WebUI endpoint failed' >&2; exit 1; }
request "$base_url/sub/clash.yaml" | grep -q 'proxy-groups' || { echo 'Published Subscription endpoint failed' >&2; exit 1; }
[ -s "$DATA_DIR/nodeharbor.db" ] || { echo 'SQLite state file is missing' >&2; exit 1; }

nodeharbor_stop || { echo 'owned stop failed' >&2; exit 1; }
nodeharbor_start || { echo 'owned restart failed' >&2; exit 1; }
request "$base_url/api/health" >/dev/null || { echo 'health endpoint failed after restart' >&2; exit 1; }
if [ "$was_running" -eq 0 ]; then nodeharbor_stop; fi
echo 'NodeHarbor KernelSU runtime smoke check passed'
