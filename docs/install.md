# Installation and Running

pathosd is a single static binary. Pick the form that fits the host; every variant runs the same `pathosd run --config ...` (see [CLI](https://github.com/vooon/pathosd/blob/main/docs/cli.md)). Whatever the form, the host must own the VIP addresses, see [Owning the VIP address](#owning-the-vip-address).

## Binary

```bash
pathosd run --config /etc/pathosd/pathosd.yaml
```

Force debug logging regardless of config:

```bash
pathosd run --debug --config /etc/pathosd/pathosd.yaml
```

### Verifying a release

Every release ships `checksums.txt` with a keyless [Sigstore](https://www.sigstore.dev/) signature, an SPDX SBOM per archive and package, and signed container images. Verify the checksum file, then the archive against it:

```bash
cosign verify-blob checksums.txt \
  --certificate checksums.txt.pem --signature checksums.txt.sig \
  --certificate-identity-regexp 'https://github.com/vooon/pathosd/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
sha256sum --check --ignore-missing checksums.txt
cosign verify ghcr.io/vooon/pathosd:<version> \
  --certificate-identity-regexp 'https://github.com/vooon/pathosd/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

## Debian and RPM packages

Releases include a `.deb` (Debian/Ubuntu) and an `.rpm` (EL9, EL10 and compatible: Rocky, Alma, RHEL, Fedora) for amd64 and arm64. Both install `/usr/bin/pathosd`, a `pathosd` system user, and a systemd unit that validates and then runs `/etc/pathosd/pathosd.toml` (kept on upgrade). To use YAML instead, or to change `GOMAXPROCS` (the unit sets `1`), uncomment the line in `/etc/default/pathosd` (Debian) or `/etc/sysconfig/pathosd` (EL) and restart; a `systemctl edit pathosd` drop-in with `Environment=` works too. The unit grants only `CAP_NET_RAW` (ICMP ping) and `CAP_NET_BIND_SERVICE` (port 179) and runs with `ProtectSystem=strict`. Upgrades restart a running service and leave a stopped one alone; the service is never enabled by the package.

The unit is `Type=notify`: `systemctl start` returns once the BGP server and HTTP API are up, and `systemctl status pathosd` shows a live summary line such as `VIPs: 2 announced, 0 pessimized, 1 withdrawn; peers: 2/2 established`, refreshed on every VIP or peer transition. Under journald pathosd omits its own log timestamps, since the journal records them; `logging.timestamps` overrides the detection either way.

```bash
sudo apt install ./pathosd_<version>_linux_amd64.deb      # Debian / Ubuntu
sudo dnf install ./pathosd_<version>_linux_amd64.rpm      # EL9 / EL10 / Fedora
sudoedit /etc/pathosd/pathosd.toml
sudo systemctl enable --now pathosd
```

## Owning the VIP address

A route for an address the host does not own black-holes traffic, so the VIP addresses have to be configured locally. [contrib/systemd/dummy-vip](https://github.com/vooon/pathosd/blob/main/contrib/systemd/dummy-vip/) holds them on a systemd-networkd `dummy` interface, one drop-in per VIP, and ships a unit drop-in so pathosd starts only once the interface is configured. Set `require_local_address: true` on the VIP to make pathosd verify this itself (see [Local Address Guard](https://github.com/vooon/pathosd/blob/main/docs/health-checks.md#local-address-guard)).

## Container

The image is built `FROM scratch`: it holds the static binary, the current Mozilla CA bundle (taken from Alpine's signed `ca-certificates` package at build time, so distrusted roots are gone and every rebuild refreshes it) and the `passwd`/`group` lines for its user. There is no shell, package manager or `curl`. It runs as the unprivileged user `pathosd` (uid 100). The binary carries file capabilities for `CAP_NET_BIND_SERVICE` (BGP port 179) and `CAP_NET_RAW` (raw ICMP for `check.ping.privileged: true`), which the daemon raises at startup. Both are in Docker's default capability set, so the plain form works:

```bash
docker run -d \
  --name pathosd \
  --network host \
  -v /etc/pathosd:/etc/pathosd:ro \
  ghcr.io/vooon/pathosd
```

The image sets `PATHOSD_CONFIG=/etc/pathosd/pathosd.yaml`, which every subcommand reads as its `--config` default; `-e PATHOSD_CONFIG=/etc/pathosd/pathosd.toml` switches the daemon and the health check together.

- The config directory must be readable by uid 100 (`chmod 0644` the files, or mount it with `-v ...:ro` from a world-readable path).
- With `--cap-drop ALL`, add `NET_BIND_SERVICE` back unless `bgp.listen_port: -1` (pathosd dials every peer and never listens), and `NET_RAW` only for privileged ping. A capability the container lacks is simply not raised; the daemon then fails with a clear `bind: permission denied` or `socket: operation not permitted` instead of refusing to start.
- Podman drops `NET_RAW` by default: add `--cap-add NET_RAW` for privileged ping.
- Unprivileged ping (`privileged: false`, the default) uses ICMP datagram sockets, which need the container user's gid inside `net.ipv4.ping_group_range`. Container runtimes set that sysctl to `0 0` or leave it at the host value. With a private network namespace pass `--sysctl net.ipv4.ping_group_range="0 2147483647"`; with `--network host` the host's sysctl applies, and it cannot be set per container. In containers, `privileged: true` plus `NET_RAW` is the simpler choice.
- The `listen` check reads the socket table of the container's network namespace. With `--network host` that is the host; otherwise mount `-v /proc:/host/proc:ro`, set `paths.procfs: /host/proc` and `listen.netns_pid: 1` (see [Listening Socket Check](https://github.com/vooon/pathosd/blob/main/docs/checks/listen.md)).
- Kubernetes: `allowPrivilegeEscalation: false` makes the kernel ignore file capabilities. Either leave it unset and keep `NET_BIND_SERVICE` / `NET_RAW` in `capabilities.add`, or run the container as root (`runAsUser: 0`) with those capabilities added.
- The image sets `GOMAXPROCS=1`, which covers a health checker with a few BGP sessions; pass `-e GOMAXPROCS=N` for many checks with short intervals.
- `tls_ca_cert` files and anything else the config refers to must be mounted into the container; the only file the image ships is the system bundle at `/etc/ssl/certs/ca-certificates.crt`, which a mount at the same path replaces.

### Health check

The image declares a `HEALTHCHECK` that runs [`pathosd healthcheck`](https://github.com/vooon/pathosd/blob/main/docs/cli.md#healthcheck) every 30 s: the container is `healthy` once every `required: true` peer is established, and `docker inspect` shows the peer summary line in the health log. The probe takes the address from `api.listen` of `$PATHOSD_CONFIG`, so a custom port needs no extra setup. Use `--live` for a probe that only needs the process to answer:

```bash
docker run -d --health-cmd "/usr/local/bin/pathosd healthcheck --live" ...
```

Podman stores the health check only for `--format docker` builds; an OCI-format image drops it.

Kubernetes ignores the image's `HEALTHCHECK` and does not need the subcommand: point `livenessProbe` and `readinessProbe` at `/healthz` and `/readyz` with `httpGet`.

Without a shell, `docker exec` and `kubectl exec` can only run the binary itself, which covers the usual diagnostics:

```bash
docker exec pathosd /usr/local/bin/pathosd healthcheck
docker exec pathosd /usr/local/bin/pathosd check
```

## Version

```bash
pathosd --version
```

Build version, commit, and date are injected via ldflags at build time.
