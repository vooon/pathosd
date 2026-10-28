# Announce Policy and Standby Ordering

How a healthy, failed or drained VIP is announced: `fail_action`, the drain file, AS-path prepends and communities. The VIP state machine behind it is in [Health checks](https://github.com/vooon/pathosd/blob/main/docs/health-checks.md#from-check-result-to-bgp-update).

Each VIP has two attribute sets under `policy`:

| Block | When applied | `as_path_prepend` | `communities` |
|-------|--------------|-------------------|---------------|
| `announce` | every announcement (healthy and pessimized) | 0–16, default 0 | attached always |
| `lower_priority` | only while pessimized (check failed with `fail_action: lower_priority`, or `lower_priority_file` present) | 1–16, default 6, **added** to the `announce` value | **merged** with the `announce` list |

Use `announce.as_path_prepend` to rank nodes announcing the same VIP: a primary with `0`, a standby with `4`, and so on. Because the pessimized prepend is the sum of both blocks, a degraded primary (`0 + 6 = 6`) still ranks below a healthy standby (`4`), and a degraded standby (`4 + 6 = 10`) ranks below a degraded primary — the standby order never inverts.

```yaml
vips:
  - name: web
    prefix: 10.10.1.1/32
    check: { type: http }
    policy:
      fail_action: lower_priority
      announce:
        as_path_prepend: 4          # this node is the standby
        communities: ["65001:200"]  # tag every announcement
      lower_priority:
        as_path_prepend: 6          # pessimized: 4 + 6 = 10 prepends
        communities: ["65001:666"]  # pessimized: 65001:200 + 65001:666
```

Prepends only work over eBGP: for iBGP peers pathosd never prepends the local ASN (a warning is logged when a prepend would have applied). Communities are sent on both.

`pathosd_vip_priority_status` reports the prepend count currently applied to the VIP (`0` means full priority), and `/status` exposes `as_path_prepend_base`, `as_path_prepend` (effective) and `communities` per VIP.
