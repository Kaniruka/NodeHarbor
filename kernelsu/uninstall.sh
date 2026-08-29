#!/system/bin/sh

MODDIR=${0%/*}
. "$MODDIR/nodeharbor-lifecycle.sh"
nodeharbor_stop
