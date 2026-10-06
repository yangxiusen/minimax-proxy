#!/bin/sh
set -eu

LOG_DIR="${MINIMAX_LOG_DIR:-/app/logs}"
mkdir -p /data "$LOG_DIR"
chown -R app:app /data "$LOG_DIR"

exec su-exec app "$@"
