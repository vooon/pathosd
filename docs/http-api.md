# HTTP API

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/` | GET | HTML status page: VIPs with state, health, every check with its health and last detail, peers with session state, a trigger-check button per VIP |
| `/healthz` | GET | Liveness — 200 if process is running |
| `/readyz` | GET | Readiness — 200 if all required BGP peers are established |
| `/status` | GET | Full daemon state: peers, VIPs, last check results, the running config's generation and last reload |
| `/metrics` | GET | Prometheus metrics exposition |
| `/api/v1/vips/{name}/check` | POST | Trigger ad-hoc health check(s); `?check=<name>` runs a single check of the VIP |
| `/api/v1/reload` | POST | Re-read the config file and apply the difference; 200 `applied`/`unchanged`, 422 `rejected`, 500 `failed` |

## Config reload

`POST /api/v1/reload` makes the daemon re-read its config file. The body is the same object on every outcome:

```json
{"at":"2026-10-29T15:04:05Z","outcome":"applied","added":["api"],"changed":["web"],"pending":["web"]}
{"at":"2026-10-29T15:05:10Z","outcome":"rejected","error":"restart required: bgp.neighbors changed"}
```

`pending` lists VIPs whose new checks still run in shadow; `error` (rejected) or `errors` (failed) say why the file is not what runs. The `config` block of `/status` keeps the last outcome together with `path`, `loaded_at`, `generation` (incremented per applied reload), `file_changed` (the file on disk differs from the running config) and `pending_vips`. What can change live is described in [Hot reload](https://github.com/vooon/pathosd/blob/main/docs/configuration.md#hot-reload); [`pathosd reload`](https://github.com/vooon/pathosd/blob/main/docs/cli.md#reload) is the CLI wrapper.

## Readiness vs liveness

- **`/healthz`** returns 200 as long as the process is up and config is loaded. It does NOT depend on BGP peer state.
- **`/readyz`** returns 200 only when all `required: true` BGP peers have established sessions. Returns 503 with a JSON body listing unready peers otherwise. [`pathosd healthcheck`](https://github.com/vooon/pathosd/blob/main/docs/cli.md#healthcheck) calls it from inside the container image, which has no `curl`.

## Security

The HTTP API has no authentication. Bind it to a loopback or host-local address (`api.listen`; the examples use `127.0.0.1:59179`, the built-in default `:59179` listens on every interface) and let Prometheus scrape it from there. If it must be reachable from other hosts, front it with a reverse proxy or a firewall rule.

What an unauthenticated client can do:

- **Read state**: `/status`, `/readyz` and `/metrics` expose peer addresses, ASNs, VIP prefixes and the last check result of every VIP. Treat that as internal topology information.
- **Reload the config**: `POST /api/v1/reload` re-reads the file the daemon was started with. The client cannot supply config; it can only make the daemon apply what root already wrote to disk, and the attempt is logged with the caller's address.
- **Trigger a check**: `POST /api/v1/vips/{name}/check` runs the configured check once. It cannot change the target, the request, or the route: the result goes through the normal rise/fall hysteresis, and checks for one VIP are serialized, so at most one ad-hoc check per VIP is in flight. The only effect of hammering the endpoint is that `fall` (or `rise`) is reached as fast as the backend answers instead of once per `interval`. Each trigger is logged with the caller's address.

The server applies `api.read_header_timeout`, `read_timeout`, `write_timeout` and `idle_timeout` (defaults 10s / 30s / 60s / 120s, `0` disables) so slow or idle clients cannot pin connections. `write_timeout` must exceed the longest `check.timeout` for the trigger endpoint to return a result.
