#!/bin/sh
set -e

# Create/update the pathosd system user and group.
if command -v systemd-sysusers > /dev/null 2>&1; then
    systemd-sysusers /usr/lib/sysusers.d/pathosd.conf || true
fi

# Create runtime and config directories with correct ownership/permissions.
if command -v systemd-tmpfiles > /dev/null 2>&1; then
    systemd-tmpfiles --create /usr/lib/tmpfiles.d/pathosd.conf || true
fi

# Reload systemd to pick up the new/updated unit file.
if command -v systemctl > /dev/null 2>&1; then
    systemctl daemon-reload || true
fi

# Note: the service is NOT enabled or started automatically.
# Configure /etc/pathosd/pathosd.toml first, then run:
#   systemctl enable --now pathosd
