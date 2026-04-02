# pathosd Unit Test Guidelines

## Project Context
- Module: `github.com/vooon/pathosd`, Go 1.26
- Config model is finalized; checkers need tests before further feature work
- Use Go 1.26 features freely (e.g. `new(123)`)

## Key Packages and Interfaces

### internal/checks/check.go
```go
type Result struct {
    Success  bool
    Detail   string
    Duration time.Duration
    Err      error
    TimedOut bool
}

type Checker interface {
    Check(ctx context.Context) Result
    Type() string
}
```

### Checkers to test (in priority order)

#### 1. HTTP Checker (`internal/checks/http.go`)
- Constructor: `NewHTTPChecker(cfg *config.HTTPCheckConfig) *HTTPChecker`
- Test with `httptest.NewServer` — no real network
- Test cases needed:
  - Success: status 200, body matches response_text
  - Status code mismatch (e.g. 503 when expecting 200)
  - Multiple acceptable response_codes (e.g. [200, 204])
  - ResponseText present / missing in body
  - ResponseRegex match / no match
  - ResponseJQ true / false / invalid JSON response
  - Context cancellation → Result.TimedOut = true
  - Custom headers are sent (verify via handler)
  - Host header is set correctly
  - Method (GET vs HEAD)
  - HTTPS with TLSInsecure=true (use httptest.NewTLSServer)
- Not yet implemented: TLSCACert (has TODO)
- The checker builds URL as: `{proto}://{host}:{port}{url_path}`

#### 2. DNS Checker (`internal/checks/dns.go`)
- Constructor: `NewDNSChecker(cfg *config.DNSCheckConfig) *DNSChecker`
- Uses `github.com/miekg/dns` client
- Test with a local DNS server (`dns.Server` from miekg/dns listening on localhost UDP)
- Test cases:
  - Success: query resolves, has answers
  - NXDOMAIN → check fails
  - Empty answer section → check fails
  - Multiple names: all must succeed; first failure short-circuits
  - Query type mapping (A, AAAA, CNAME, etc. via parseQueryType)
  - Context cancellation → TimedOut
  - Resolver defaults to "" → falls back to /etc/resolv.conf (hard to unit test, maybe skip)

#### 3. Ping Checker (`internal/checks/ping.go`)
- Constructor: `NewPingChecker(cfg *config.PingCheckConfig) *PingChecker`
- Uses `github.com/prometheus-community/pro-bing`
- Harder to unit test (needs ICMP privileges or mock)
- Test cases (if feasible):
  - Empty DstIP → returns failure "dst_ip is required"
  - Context cancellation → TimedOut
  - MaxLossRatio logic (mock stats if possible)
- Consider integration test or skip if mocking pro-bing is impractical

#### 4. Scheduler (`internal/checks/scheduler.go`)
- Constructor: `NewScheduler(cfg SchedulerConfig)`
- Pure logic, very testable with a fake Checker
- Test cases:
  - Rise threshold: starts unhealthy, N successes → transition to healthy
  - Fall threshold: healthy, N failures → transition to unhealthy
  - Mixed results reset opposite counter
  - Callbacks (onTransition, onCheckResult) are called correctly
  - Initial state is unhealthy (fail-closed)
  - TriggerCheck forces immediate check
  - IsHealthy(), LastResult(), ConsecutiveOK(), ConsecutiveFail() accessors

#### 5. Factory (`internal/checks/factory.go`)
- `NewChecker(cfg *config.CheckConfig) (Checker, error)`
- Test: returns correct type for "http"/"dns"/"ping", error for unknown type, error when sub-config is nil

## Testing Conventions
- Test files go next to source: `internal/checks/http_test.go`, etc.
- Use `github.com/stretchr/testify` — `assert` for non-fatal, `require` for fatal checks
- Table-driven tests with `t.Run()` subtests
- Use `context.Background()` for normal tests, `context.WithTimeout`/cancel for timeout tests
- Fake checker for scheduler tests: implement `Checker` interface with configurable results
- For HTTP: use `net/http/httptest`
- For DNS: use `github.com/miekg/dns` server on localhost
- Test file naming: `{source}_test.go`

## Config Structs (key fields for test setup)

### HTTPCheckConfig
```go
URL, Proto, Host string; Port uint16; Method string
ResponseCodes []int; ResponseText, ResponseRegex, ResponseJQ string
TLSInsecure bool; TLSCACert string; Headers map[string]string
```

### DNSCheckConfig
```go
Names []string; Resolver string; Port uint16; QueryType string
```

### PingCheckConfig
```go
DstIP, SrcIP string; Count int; Timeout, Interval *Duration
MaxLossRatio float64
```

## Build & Test Commands
- After creating/editing Go files: verify with `go build ./...`
- Run checker tests: `go test ./internal/checks/... -v`
- Run all tests: `go test ./...`
- Static checks: `go vet ./...` and `golangci-lint run`
- Conventional Commits for commit messages
