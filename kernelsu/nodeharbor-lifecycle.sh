#!/system/bin/sh

MODDIR=${0%/*}
DATA_DIR="$MODDIR/data"
LOG_DIR="$MODDIR/logs"
TMP_DIR="$MODDIR/tmp"
PID_FILE="$DATA_DIR/nodeharbor.pid"
NODEHARBOR_BIN="$MODDIR/bin/nodeharbor"

nodeharbor_pid_is_owned() {
  pid="$1"
  [ -n "$pid" ] || return 1
  [ -r "/proc/$pid/exe" ] || return 1
  process_exe=$(readlink "/proc/$pid/exe" 2>/dev/null)
  case "$process_exe" in
    "$NODEHARBOR_BIN"|"$NODEHARBOR_BIN (deleted)") ;;
    *) return 1 ;;
  esac
  [ -r "/proc/$pid/cmdline" ] || return 1
  set -- $(tr '\000' ' ' <"/proc/$pid/cmdline" 2>/dev/null)
  [ "$1" = "$NODEHARBOR_BIN" ] || return 1
  shift
  while [ "$#" -gt 0 ]; do
    if [ "$1" = '--data' ]; then
      shift
      [ "$1" = "$DATA_DIR" ] && return 0
      return 1
    fi
    shift
  done
  return 1
}

nodeharbor_stop() {
  [ -r "$PID_FILE" ] || return 0
  pid=$(cat "$PID_FILE" 2>/dev/null)
  if nodeharbor_pid_is_owned "$pid"; then
    kill "$pid" 2>/dev/null || true
    for _ in 1 2 3 4 5 6 7 8 9 10; do
      nodeharbor_pid_is_owned "$pid" || break
      sleep 1
    done
    if nodeharbor_pid_is_owned "$pid"; then
      kill -9 "$pid" 2>/dev/null || true
      sleep 1
      if nodeharbor_pid_is_owned "$pid"; then
        return 1
      fi
    fi
  fi
  rm -f "$PID_FILE"
}

nodeharbor_start() {
  mkdir -p "$DATA_DIR" "$LOG_DIR" "$TMP_DIR" || return 1
  export TMPDIR="$TMP_DIR"
  if [ -r "$PID_FILE" ]; then
    pid=$(cat "$PID_FILE" 2>/dev/null)
    if nodeharbor_pid_is_owned "$pid"; then
      process_exe=$(readlink "/proc/$pid/exe" 2>/dev/null)
      case "$process_exe" in
        "$NODEHARBOR_BIN (deleted)") nodeharbor_stop ;;
        *) return 0 ;;
      esac
    fi
    rm -f "$PID_FILE"
  fi
  if [ -n "${NODEHARBOR_LISTEN:-}" ]; then
    nohup "$NODEHARBOR_BIN" --listen "$NODEHARBOR_LISTEN" --listener-file "$DATA_DIR/listener.url" --data "$DATA_DIR" --open-browser=false >"$LOG_DIR/nodeharbor.log" 2>&1 &
  else
    nohup "$NODEHARBOR_BIN" --listener-file "$DATA_DIR/listener.url" --data "$DATA_DIR" --open-browser=false >"$LOG_DIR/nodeharbor.log" 2>&1 &
  fi
  echo $! >"$PID_FILE"
  sleep 1
  if nodeharbor_pid_is_owned "$(cat "$PID_FILE" 2>/dev/null)"; then
    return 0
  fi
  rm -f "$PID_FILE"
  return 1
}
