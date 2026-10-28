#!/bin/sh
# Create the pathosd system user and group before the package files are laid
# down, so /etc/pathosd can be owned by the group on both deb and rpm. The
# sysusers.d file installed by the package keeps them in sync afterwards.
set -e

if ! getent group pathosd > /dev/null 2>&1; then
    groupadd --system pathosd
fi
if ! getent passwd pathosd > /dev/null 2>&1; then
    useradd --system --gid pathosd --home-dir / --no-create-home \
        --shell /usr/sbin/nologin --comment "pathosd BGP VIP announcer" pathosd
fi
