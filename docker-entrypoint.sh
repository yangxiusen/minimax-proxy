#!/bin/sh
set -eu

LOG_DIR="${MINIMAX_LOG_DIR:-/var/log/minimax-proxy}"
mkdir -p /data "$LOG_DIR"
chown -R app:app /data "$LOG_DIR"

exec su-exec app "$@"
