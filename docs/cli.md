# CLI

```
pathosd <command> [flags]

  run       --config FILE [--debug]     run the daemon
  validate  --config FILE               load and validate a config, exit
  check     --config FILE [...]         run the configured health checks once, no BGP
  jq-test   <expression> [--file FILE]  evaluate a jq expression against JSON input
  --version                             print version, commit and build date
```

`--config` accepts YAML (`.yaml`, `.yml`) or TOML (`.toml`); environment placeholders are expanded in both (see [Configuration](https://github.com/vooon/pathosd/blob/main/docs/configuration.md)). A malformed command line exits `80` on every subcommand.

## run

```bash
pathosd run --config /etc/pathosd/pathosd.yaml
pathosd run --debug --config /etc/pathosd/pathosd.yaml   # force debug logging regardless of config
```

Loads and validates the config, starts the embedded GoBGP speaker, the check schedulers and the HTTP API, and runs until `SIGINT`/`SIGTERM`. A startup failure (bad config, port in use, missing capability) exits non-zero with the reason. Under systemd the unit is `Type=notify`: `READY=1` is sent once BGP and the API are up and the status line is refreshed on every VIP or peer transition. Shutdown withdraws nothing explicitly: the BGP sessions close and the peers drop every route, which is the fail-closed design.

## validate

```bash
pathosd validate --config /etc/pathosd/pathosd.yaml
```

Parses the file, applies defaults, expands placeholders and runs every validation rule, then prints `configuration is valid` or the list of errors. The package's systemd unit runs it as `ExecStartPre`. See [Validation and JSON Schema](https://github.com/vooon/pathosd/blob/main/docs/configuration.md#validation-and-json-schema).

## check

```bash
pathosd check --config /etc/pathosd/pathosd.toml                     # every VIP
pathosd check --config pathosd.yaml --vip web --check http           # one check of one VIP
pathosd check --config pathosd.yaml --format json --timeout 5s       # JSON with type-specific details
pathosd check --config pathosd.yaml --skip-local-address             # probe even without the VIP address on this host
```

Builds the checkers from the config and runs them once, concurrently, with their configured timeouts. No BGP session is opened and no rise/fall hysteresis applies: the output is the raw result of each probe plus the VIP health combined through `checks_policy`.

```
VIP web (10.10.1.1/32): healthy [http || tcp]
  OK    http  http  12.4ms  HTTP 200 OK
  FAIL  tcp   tcp   1.1ms   dial tcp 10.10.1.1:443: connect: connection refused
```

| Flag | Effect |
|------|--------|
| `--vip NAME` (repeatable) | only these VIPs; default all |
| `--check NAME` (repeatable) | only checks with these names; the others count as failed in the combination, like `?check=` on the API |
| `--format text\|json` | `json` carries the type-specific `details` (status code, rows, brokers, ...) |
| `--timeout DURATION` | overrides every check's `timeout` |
| `--skip-local-address` | runs the probes even where `require_local_address` would hold them back |

Exit codes follow `test(1)`:

| Code | Meaning |
|------|---------|
| `0` | every selected VIP is healthy |
| `1` | at least one selected VIP is unhealthy (the summary line goes to stderr) |
| `200` | the command failed: missing or invalid config, unknown VIP or check name, output error |

`200` sits above the health verdicts and above the `128+signal` codes a shell reports for a killed process, so a script cannot confuse an error with a result. Unknown VIP or check names are errors rather than empty runs.

For a running daemon, the API's [ad-hoc trigger](https://github.com/vooon/pathosd/blob/main/docs/health-checks.md#running-checks-on-demand) runs the checks through the normal hysteresis instead.

## jq-test

```bash
echo '{"status":"ok","replicas":3}' | pathosd jq-test '.status == "ok" and .replicas >= 2'
pathosd jq-test --file rows.json '.[0].read_only == 0'
```

Evaluates a [gojq](https://github.com/itchyny/gojq) expression against JSON from stdin or `--file` and prints the result, so `response_jq` (http) and `result_jq` (sql) conditions can be tried on a captured body or result set before they go into the config.
