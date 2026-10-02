#!/bin/sh
set -eu

# Debian calls this with "upgrade"; RPM with 1 when another version remains.
case "${1:-}" in
    upgrade|1|failed-upgrade|abort-install|abort-upgrade) exit 0 ;;
esac

if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    systemctl disable --now donate.service >/dev/null 2>&1 || true
elif command -v rc-service >/dev/null 2>&1; then
    rc-service donate stop >/dev/null 2>&1 || true
    if command -v rc-update >/dev/null 2>&1; then
        rc-update del donate default >/dev/null 2>&1 || true
    fi
fi
