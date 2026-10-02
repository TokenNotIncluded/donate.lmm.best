#!/bin/sh
set -eu

mkdir -p /var/lib/donate /etc/donate
chown donate:donate /var/lib/donate
chmod 0700 /var/lib/donate
chown root:root /etc/donate
chmod 0750 /etc/donate
chown root:root /etc/donate/donate.env
chmod 0640 /etc/donate/donate.env

if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    systemctl daemon-reload
fi
# No automatic start: configure the final public URL before enabling the service.
