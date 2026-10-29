# Build helpers only. The runtime image below is FROM scratch and holds the
# static binary, the CA bundle and the passwd/group lines for its user.
FROM alpine:3.24 AS rootfs

# Current Mozilla CA bundle from the signed distribution package: roots that
# Mozilla distrusted are already dropped upstream, and every rebuild picks
# up the newest bundle. libcap provides setcap.
RUN apk add --no-cache ca-certificates libcap

# Non-root runtime user: uid 100, gid 101 (Alpine system account defaults).
# Only the two accounts the image needs go into passwd/group.
RUN set -eu \
 && install -d -m 0755 /rootfs/etc/ssl/certs /rootfs/usr/local/bin \
 && cp /etc/ssl/certs/ca-certificates.crt /rootfs/etc/ssl/certs/ca-certificates.crt \
 && printf 'root:x:0:0:root:/root:/sbin/nologin\npathosd:x:100:101:pathosd:/nonexistent:/sbin/nologin\n' > /rootfs/etc/passwd \
 && printf 'root:x:0:\npathosd:x:101:\n' > /rootfs/etc/group \
 && install -d -o 0 -g 101 -m 0750 /rootfs/etc/pathosd

COPY pathosd /rootfs/usr/local/bin/pathosd

# File capabilities so the daemon can bind BGP port 179 and open raw ICMP
# sockets (check.ping.privileged) without running as root. Permitted only
# (+p): the daemon raises them itself, and exec still succeeds when the
# container's bounding set lacks one (e.g. NET_RAW under --cap-drop ALL).
RUN setcap cap_net_bind_service,cap_net_raw=+p /rootfs/usr/local/bin/pathosd

FROM scratch

# Static OCI annotations; version, created and revision come from the
# GoReleaser build flags (see .goreleaser.yaml).
LABEL org.opencontainers.image.title="pathosd" \
      org.opencontainers.image.description="pathosd — health-aware BGP VIP announcer" \
      org.opencontainers.image.url="https://github.com/vooon/pathosd" \
      org.opencontainers.image.source="https://github.com/vooon/pathosd" \
      org.opencontainers.image.documentation="https://github.com/vooon/pathosd/blob/main/docs/README.md" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.vendor="vooon" \
      org.opencontainers.image.base.name="scratch"

# A single COPY keeps ownership, modes and the capability xattr of every file.
COPY --from=rootfs /rootfs/ /

USER pathosd:pathosd

# No shell or curl in the image: the binary calls its own /readyz, which is
# 200 only when every required BGP peer is established. Override with
# `--addr host:port` when api.listen is not the default :59179, or `--live`
# for a liveness-only probe (/healthz).
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s \
  CMD ["/usr/local/bin/pathosd", "healthcheck"]

ENTRYPOINT ["/usr/local/bin/pathosd"]
CMD ["run", "--config", "/etc/pathosd/pathosd.yaml"]
