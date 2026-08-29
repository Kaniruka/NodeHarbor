#!/system/bin/sh

MODDIR=${0%/*}
. "$MODDIR/nodeharbor-lifecycle.sh"
case "${1:-status}" in
  start) nodeharbor_start ;;
  stop) nodeharbor_stop ;;
  restart) nodeharbor_stop && nodeharbor_start ;;
  smoke) "$MODDIR/smoke.sh" ;;
  status)
    pid=$(cat "$PID_FILE" 2>/dev/null)
    if nodeharbor_pid_is_owned "$pid"; then
      echo "running ($pid)"
    else
      echo 'stopped'
      exit 1
    fi
    ;;
  *) echo "usage: $0 {start|stop|restart|status|smoke}" >&2; exit 2 ;;
esac
