#!/bin/sh
set -eu

group_exists() {
    if command -v getent >/dev/null 2>&1; then
        getent group donate >/dev/null 2>&1
    else
        grep -q '^donate:' /etc/group
    fi
}

if id -u donate >/dev/null 2>&1; then
    if [ "$(id -u donate)" -eq 0 ]; then
        echo "The donate service user must not have UID 0." >&2
        exit 1
    fi
    if ! group_exists; then
        if command -v groupadd >/dev/null 2>&1; then
            groupadd --system donate
        else
            addgroup -S donate
        fi
    fi
    exit 0
fi

if command -v useradd >/dev/null 2>&1; then
    if ! group_exists; then
        groupadd --system donate
    fi
    useradd --system --gid donate --home-dir /var/lib/donate --no-create-home --shell /sbin/nologin donate
elif command -v adduser >/dev/null 2>&1; then
    if ! group_exists; then
        addgroup -S donate
    fi
    adduser -S -D -H -h /var/lib/donate -s /sbin/nologin -G donate donate
else
    echo "Cannot create donate service user: install your distribution's user-management tools." >&2
    exit 1
fi
