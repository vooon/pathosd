package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/vooon/pathosd/internal/config"
	"github.com/vooon/pathosd/internal/model"
)

// defaultAPIAddr is where a daemon with the default api.listen (":59179")
// answers on the local host.
const defaultAPIAddr = "127.0.0.1:59179"

// HealthcheckCmd asks a running daemon's /readyz whether it is ready and
// prints the peer summary. It is the container HEALTHCHECK: the image has
// no shell or curl.
type HealthcheckCmd struct {
	Addr    string        `help:"HTTP API address (host:port). Default: api.listen of --config, else 127.0.0.1:59179." short:"a"`
	Config  string        `help:"Config file to read api.listen from." short:"c" env:"PATHOSD_CONFIG"`
	Live    bool          `help:"Check liveness (/healthz, process up) instead of readiness (required peers established)."`
	Timeout time.Duration `help:"Request timeout." default:"5s"`
	Quiet   bool          `help:"Print nothing, only set the exit code." short:"q"`
}

func (c *HealthcheckCmd) Run() error {
	return c.run(os.Stdout)
}

// run exits 0 when the daemon is healthy, 1 when it is not or cannot be
// reached (both go to stderr via exitCodeError), and 200 when the command
// itself is misconfigured, like `pathosd check`.
func (c *HealthcheckCmd) run(out io.Writer) error {
	addr, err := c.resolveAddr()
	if err != nil {
		return &exitCodeError{code: exitInternal, msg: "pathosd: error: " + err.Error()}
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.Timeout)
	defer cancel()
	summary, healthy, err := c.probe(ctx, addr)
	if err != nil {
		summary = err.Error()
	}
	verdict := "ready"
	if c.Live {
		verdict = "alive"
	}
	if !healthy {
		msg := ""
		if !c.Quiet {
			msg = "not " + verdict + ": " + summary
		}
		return &exitCodeError{code: exitUnhealthy, msg: msg}
	}
	if !c.Quiet {
		_, _ = fmt.Fprintf(out, "%s: %s\n", verdict, summary)
	}
	return nil
}

// resolveAddr picks the address to dial: --addr, then api.listen of
// --config mapped to the local host, then the built-in default.
func (c *HealthcheckCmd) resolveAddr() (string, error) {
	listen := c.Addr
	if listen == "" && c.Config != "" {
		cfg, err := config.Load(c.Config)
		if err != nil {
			return "", fmt.Errorf("loading config: %w", err)
		}
		listen = cfg.API.Listen
	}
	if listen == "" {
		return defaultAPIAddr, nil
	}
	return loopbackFor(listen)
}

// loopbackFor maps a listen address to one a client on the same host can
// dial: an empty or unspecified host becomes the matching loopback address.
func loopbackFor(listen string) (string, error) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", fmt.Errorf("api address %q: %w", listen, err)
	}
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	}
	return net.JoinHostPort(host, port), nil
}

// probe fetches /healthz (--live) or /readyz. The verdict is the status
// code (200 ready, 503 not ready); the body only feeds the summary line. A
// transport error or any other status is returned as err with healthy=false.
func (c *HealthcheckCmd) probe(ctx context.Context, addr string) (summary string, healthy bool, err error) {
	path := "/readyz"
	if c.Live {
		path = "/healthz"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+path, nil)
	if err != nil {
		return "", false, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", false, fmt.Errorf("reading %s: %w", path, err)
	}
	if c.Live {
		if resp.StatusCode != http.StatusOK {
			return "", false, fmt.Errorf("GET %s: HTTP %d", path, resp.StatusCode)
		}
		return "HTTP API answers on " + addr, true, nil
	}
	var ready struct {
		Peers []model.PeerStatus `json:"peers"`
	}
	if err := json.Unmarshal(body, &ready); err != nil {
		summary = fmt.Sprintf("GET %s: HTTP %d", path, resp.StatusCode)
	} else {
		summary = model.SummarizePeers(ready.Peers)
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return summary, true, nil
	case http.StatusServiceUnavailable:
		return summary, false, nil
	default:
		return "", false, fmt.Errorf("GET %s: HTTP %d", path, resp.StatusCode)
	}
}

const userAgent = "pathosd-healthcheck"
