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

request_post() {
  url="$1"
  body="$2"
  if command -v curl >/dev/null 2>&1; then
    curl --noproxy '*' --fail --silent --show-error -X POST -H 'Content-Type: application/json' --data "$body" "$url"
  else
    wget -qO- --header='Content-Type: application/json' --post-data="$body" "$url"
  fi
}

was_running=0
if nodeharbor_pid_is_owned "$(cat "$PID_FILE" 2>/dev/null)"; then was_running=1; fi
nodeharbor_start || { echo 'NodeHarbor failed to start' >&2; exit 1; }
if [ -n "${NODEHARBOR_LISTEN:-}" ]; then
  base_url="http://$NODEHARBOR_LISTEN"
else
  base_url="http://127.0.0.1:9876"
fi
module_version=$(sed -n 's/^version=//p' "$MODDIR/module.prop")
nodeharbor_version=$("$NODEHARBOR_BIN" --version 2>/dev/null)
[ "$nodeharbor_version" = "$module_version" ] || { echo "NodeHarbor version mismatch: $nodeharbor_version" >&2; exit 1; }
nodeharbor_digest=$(sha256sum "$NODEHARBOR_BIN" | awk '{print toupper($1)}')
metadata_digest=$(sed -n 's/.*"executableSHA256": "\([A-Fa-f0-9]*\)".*/\1/p' "$MODDIR/bin/nodeharbor.json" | tr 'a-f' 'A-F')
[ "$nodeharbor_digest" = "$metadata_digest" ] || { echo 'NodeHarbor digest mismatch' >&2; exit 1; }
grep -q '"version": "v1.19.30"' "$MODDIR/bin/nodeharbor-core.json" || { echo 'Mihomo version metadata is invalid' >&2; exit 1; }
health=$(request "$base_url/api/health") || { echo 'health endpoint failed' >&2; exit 1; }
case "$health" in *'"status":"healthy"'*) ;; *) echo "unhealthy response: $health" >&2; exit 1 ;; esac
request "$base_url/" | grep -q 'id="root"' || { echo 'WebUI endpoint failed' >&2; exit 1; }
request "$base_url/sub/clash.yaml" | grep -q 'proxy-groups' || { echo 'Published Subscription endpoint failed' >&2; exit 1; }
[ -s "$DATA_DIR/nodeharbor.db" ] || { echo 'SQLite state file is missing' >&2; exit 1; }
settings_before=$(request "$base_url/api/settings") || { echo 'settings endpoint failed' >&2; exit 1; }
installation_id_before=$(printf '%s' "$settings_before" | sed -n 's/.*"installationId":"\([^"]*\)".*/\1/p')
[ -n "$installation_id_before" ] || { echo 'installation identity was not persisted' >&2; exit 1; }
publication_before=$(request "$base_url/sub/clash.yaml") || { echo 'Published Subscription fetch failed' >&2; exit 1; }
run_response=$(request_post "$base_url/api/evaluation-runs" '{}') || { echo 'evaluation path failed to start' >&2; exit 1; }
run_id=$(printf '%s' "$run_response" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
[ -n "$run_id" ] || { echo 'evaluation run id is missing' >&2; exit 1; }
run_status='pending'
for _ in 1 2 3 4 5 6 7 8 9 10; do
  run=$(request "$base_url/api/evaluation-runs/$run_id") || break
  run_status=$(printf '%s' "$run" | sed -n 's/.*"status":"\([^"]*\)".*/\1/p')
  case "$run_status" in completed|failed|paused) break ;; esac
  sleep 1
done
case "$run_status" in completed|paused) ;; *) echo "evaluation run did not complete safely: $run_status" >&2; exit 1 ;; esac
publication_after=$(request "$base_url/sub/clash.yaml") || { echo 'Published Subscription fetch failed after evaluation' >&2; exit 1; }
[ "$publication_before" = "$publication_after" ] || { echo 'failed evaluation replaced the previous Publication Snapshot' >&2; exit 1; }

nodeharbor_stop || { echo 'owned stop failed' >&2; exit 1; }
nodeharbor_start || { echo 'owned restart failed' >&2; exit 1; }
settings_after=$(request "$base_url/api/settings") || { echo 'health endpoint failed after restart' >&2; exit 1; }
installation_id_after=$(printf '%s' "$settings_after" | sed -n 's/.*"installationId":"\([^"]*\)".*/\1/p')
[ "$installation_id_before" = "$installation_id_after" ] || { echo 'SQLite state did not persist across restart' >&2; exit 1; }

"$MODDIR/action.sh" stop || { echo 'owned stop before foreign-process test failed' >&2; exit 1; }
sleep 60 &
foreign_pid=$!
printf '%s\n' "$foreign_pid" >"$PID_FILE"
"$MODDIR/action.sh" stop || { echo 'stop lifecycle failed'; kill "$foreign_pid" 2>/dev/null || true; exit 1; }
if ! kill -0 "$foreign_pid" 2>/dev/null; then
  echo 'stop lifecycle terminated a foreign process' >&2
  exit 1
fi
printf '%s\n' "$foreign_pid" >"$PID_FILE"
"$MODDIR/uninstall.sh" || { echo 'uninstall lifecycle failed' >&2; kill "$foreign_pid" 2>/dev/null || true; exit 1; }
if ! kill -0 "$foreign_pid" 2>/dev/null; then
  echo 'uninstall lifecycle terminated a foreign process' >&2
  exit 1
fi
kill "$foreign_pid" 2>/dev/null || true
if [ "$was_running" -eq 1 ]; then nodeharbor_start; fi
echo 'NodeHarbor KernelSU runtime smoke check passed'
