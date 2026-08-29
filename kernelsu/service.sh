#!/system/bin/sh

MODDIR=${0%/*}
. "$MODDIR/nodeharbor-lifecycle.sh"

# The daemon reads its persisted listener address and port. The initial
# default is loopback-only; NODEHARBOR_LISTEN is an explicit local override.
nodeharbor_start
