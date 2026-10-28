# Dummy VIP interface (systemd-networkd)

pathosd announces a VIP, but the host still has to own the address for the
kernel to accept traffic to it and for the service to bind it. A `dummy`
interface is the usual place: it is always up, has no link state to lose, and
keeps the VIPs separate from the physical NIC configuration.

This directory is the systemd-networkd variant. Install it next to the
Debian package:

```bash
sudo install -m 0644 50-dummy-vip.netdev 50-dummy-vip.network /etc/systemd/network/
sudo install -d /etc/systemd/network/50-dummy-vip.network.d
sudo install -m 0644 50-dummy-vip.network.d/web.conf /etc/systemd/network/50-dummy-vip.network.d/
sudo install -d /etc/systemd/system/pathosd.service.d
sudo install -m 0644 pathosd.service.d/dummy-vip.conf /etc/systemd/system/pathosd.service.d/
sudo systemctl daemon-reload
sudo networkctl reload
sudo systemctl restart pathosd
```

- `50-dummy-vip.netdev` creates the interface, `50-dummy-vip.network`
  configures it. One drop-in per VIP under `50-dummy-vip.network.d/` carries
  the address; its prefix must match `vips[].prefix` in the pathosd config.
- `pathosd.service.d/dummy-vip.conf` is the optional dependency on the
  interface: the packaged unit already orders itself after
  `systemd-networkd-wait-online@dummy-vip.service`, and this drop-in pulls
  that unit in so pathosd waits until the addresses are configured. It is a
  `Wants=`, so a missing interface delays startup by the wait-online timeout
  instead of failing it.
- Rename `dummy-vip` consistently in all four files if you need another
  interface name; the packaged unit's `After=` then no longer matches, but
  the drop-in carries its own `After=`.

systemd-networkd has to be the network manager for this to apply (it is on
Ubuntu Server via netplan, and on Debian when enabled). With ifupdown, declare
the interface in `/etc/network/interfaces.d/` instead and drop the
`pathosd.service.d` part.

If the VIP lies inside the subnet of a physical interface, replies sourced
from the VIP leave through that NIC directly instead of via the router that
holds the BGP route, and peers on the same L2 segment see an asymmetric path.
A source-based policy route (`ip rule add from <vip>/32 table 100` plus a
default route in that table via the router) fixes this; pick a VIP outside the
LAN subnets to avoid it altogether.
