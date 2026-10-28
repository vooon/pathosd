#!/bin/sh
set -e

# Keep user/group and directories in sync with the declarative files.
if command -v systemd-sysusers > /dev/null 2>&1; then
    systemd-sysusers /usr/lib/sysusers.d/pathosd.conf || true
fi
if command -v systemd-tmpfiles > /dev/null 2>&1; then
    systemd-tmpfiles --create /usr/lib/tmpfiles.d/pathosd.conf || true
fi

if command -v systemctl > /dev/null 2>&1; then
    systemctl daemon-reload || true

    # Upgrade: deb passes "configure <old-version>", rpm passes the number of
    # installed instances (2 or more). Restart a running service so the new
    # binary takes over; a stopped or disabled service stays that way.
    upgrade=0
    case "$1" in
        configure) [ -n "$2" ] && upgrade=1 ;;
        [2-9]|[1-9][0-9]*) upgrade=1 ;;
    esac
    if [ "$upgrade" = 1 ]; then
        systemctl try-restart pathosd.service || true
    fi
fi

# The service is NOT enabled or started on first install.
# Configure /etc/pathosd/pathosd.toml first, then run:
#   systemctl enable --now pathosd
