#!/bin/sh
set -eu

if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    systemctl daemon-reload
fi
# Preserve the service user and all donation data on removal/purge.
