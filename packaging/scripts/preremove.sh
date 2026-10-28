#!/bin/sh
set -e

# Only stop and disable on a real removal. On upgrade deb passes "upgrade"
# and rpm passes "1" (one instance remains); the service is restarted by the
# new package's postinstall instead.
case "$1" in
    upgrade|1) exit 0 ;;
esac

if command -v systemctl > /dev/null 2>&1; then
    systemctl stop pathosd.service   || true
    systemctl disable pathosd.service || true
fi
