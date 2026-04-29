#!/bin/sh
set -e

# Reload systemd after the package files have been removed.
if command -v systemctl > /dev/null 2>&1; then
    systemctl daemon-reload || true
fi
