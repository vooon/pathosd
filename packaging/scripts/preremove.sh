#!/bin/sh
set -e

# Stop and disable the service before the package files are removed.
if command -v systemctl > /dev/null 2>&1; then
    systemctl stop pathosd.service   || true
    systemctl disable pathosd.service || true
fi
