# BGP Peering

Neighbors are explicit in `bgp.neighbors` (`address`, `peer_asn`, optional `local_address`, `passive`, `required`, `auth_password`, multihop, graceful restart); the [example configs](https://github.com/vooon/pathosd/blob/main/examples/) show every field. This page covers session security, a worked localhost peering with BIRD, and live inspection with the GoBGP CLI.

## Session authentication (TCP MD5)

BGP has no TLS. The deployed protections are TCP MD5 signatures (RFC 2385), its successor TCP-AO (RFC 5925, not yet supported by GoBGP) and GTSM/TTL security (RFC 5082). pathosd supports TCP MD5 per neighbor via `auth_password`: the kernel signs and verifies every segment, so a host that does not know the key cannot open, hijack or reset the session.

```yaml
bgp:
  neighbors:
    - name: frr
      address: 10.0.0.2
      peer_asn: 65000
      auth_password: "%{PATHOSD_BGP_MD5}"   # keep the secret in the environment
```

The same key goes on the peer: `neighbor 10.0.0.1 password ...` in FRR, or `password "...";` inside the `protocol bgp` block in BIRD. Keys are at most 80 bytes (Linux `TCP_MD5SIG_MAXKEYLEN`). Setting the socket option needs no capability.

Two consequences worth knowing:

- Once a port is MD5-protected, an unsigned TCP connect to it (a `tcpSocket` readiness probe, `nc -z`, a port scanner) is silently dropped by the kernel. Monitor the session through the daemon, not by poking port 179.
- The embedded GoBGP gRPC API (`bgp.gobgp_api`) returns the password as part of the peer configuration, so keep that API on a local address or unix socket.

## OpenWrt/BIRD localhost peering

On OpenWrt the mesh routers run BIRD, and pathosd lives on the same host. pathosd acts as a distinct ASN and announces anycast VIPs over a local eBGP session. The session is passive on pathosd's side: BIRD dials out to pathosd on a second IP that BIRD assigns to its own LAN interface, at port `1179`.

Corresponding pathosd config:

```yaml
router:
  local_address: "<pathosd-local-ip>"
bgp:
  listen_address: <pathosd-listen-ip>
  listen_port: 1179
  neighbors:
    - name: bird-local
      address: <bird-local-ip>
      peer_asn: <bird_as>
      passive: true
      required: true
```

BIRD-side config for the local pathosd peer:

```bird
protocol bgp peer_pathosd_local {
    local <bird-local-ip> as <bird_as>;
    neighbor <pathosd-listen-ip> port 1179 as <pathosd_as>;
    multihop;
    ipv4 {
        import filter {
            if net ~ [ <vip>/32, ... ] then {
                bgp_next_hop = net.ip;
                preference = 250;
                accept;
            }
            reject;
        };
        export none;
    };
}
```

The import filter above is the critical part. pathosd announces VIP routes with `router.local_address` (the router's own address) as next-hop. BIRD refuses to install a route whose next-hop is a local address ("Next hop address X is a local address of iface"), so a naive `import all` would leave the route unreachable and never selected. The fix is done entirely in the filter:

1. Restrict imports to the VIP prefixes only (`if net ~ [ <vip>/32, ... ]`).
2. Rewrite each VIP's next-hop to the VIP itself (`bgp_next_hop = net.ip`) so the route is resolvable via the connected `dummy_vip`.
3. Raise `preference = 250`, above the connected/device route's default of `240`, so BIRD selects pathosd's route over the connected VIP route.
4. Reject everything else.

BIRD still marks the route unreachable locally (the next-hop caveat), but it selects and exports it to eBGP mesh peers — so the VIP reaches the mesh.

Pessimization: pathosd's `fail_action: lower_priority` with `as_path_prepend` propagates through BIRD to the mesh — remote routers see a longer AS path and steer away. This works directly over eBGP, so no iBGP or communities are required.

## GoBGP CLI debugging

Enable the embedded GoBGP gRPC API when you want to inspect live state with the `gobgp` CLI:

```yaml
bgp:
  gobgp_api:
    enabled: true
    # Optional, defaults to 127.0.0.1:50051 when enabled.
    listen: 127.0.0.1:50051
```

Then query it with:

```bash
gobgp -u 127.0.0.1:50051 neighbor
gobgp -u 127.0.0.1:50051 global rib
```
