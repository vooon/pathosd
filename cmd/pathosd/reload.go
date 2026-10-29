package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/vooon/pathosd/internal/model"
)

// ReloadCmd asks a running daemon to re-read its config file and prints the
// outcome. It is what `systemctl reload pathosd` runs, so a rejected reload
// fails the systemctl call instead of only leaving a log line.
type ReloadCmd struct {
	Addr    string        `help:"HTTP API address (host:port). Default: api.listen of --config, else 127.0.0.1:59179." short:"a"`
	Config  string        `help:"Config file to read api.listen from." short:"c" env:"PATHOSD_CONFIG"`
	Timeout time.Duration `help:"Request timeout; a reload waits for in-flight checks of removed VIPs." default:"30s"`
}

func (c *ReloadCmd) Run() error {
	return c.run(os.Stdout)
}

// run exits 0 when the daemon now runs the file (applied or unchanged), 1
// when it does not (rejected, failed, or no answer) and 200 on bad flags.
func (c *ReloadCmd) run(out io.Writer) error {
	addr, err := resolveAPIAddr(c.Addr, c.Config)
	if err != nil {
		return &exitCodeError{code: exitInternal, msg: "pathosd: error: " + err.Error()}
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.Timeout)
	defer cancel()
	res, err := postReload(ctx, addr)
	if err != nil {
		return &exitCodeError{code: exitUnhealthy, msg: "reload not applied: " + err.Error()}
	}
	line := describeReload(res)
	if !res.OK() {
		return &exitCodeError{code: exitUnhealthy, msg: line}
	}
	_, _ = fmt.Fprintln(out, line)
	return nil
}

func postReload(ctx context.Context, addr string) (*model.ReloadStatus, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+"/api/v1/reload", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "pathosd-reload")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	var res model.ReloadStatus
	if err := json.Unmarshal(body, &res); err != nil || res.Outcome == "" {
		return nil, fmt.Errorf("POST /api/v1/reload: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(bytes.ToValidUTF8(body, nil))))
	}
	return &res, nil
}

// describeReload renders one line such as
// "applied: added api; changed web (pending verdict)".
func describeReload(res *model.ReloadStatus) string {
	var parts []string
	if len(res.Added) > 0 {
		parts = append(parts, "added "+strings.Join(res.Added, ", "))
	}
	if len(res.Removed) > 0 {
		parts = append(parts, "removed "+strings.Join(res.Removed, ", "))
	}
	if len(res.Changed) > 0 {
		s := "changed " + strings.Join(res.Changed, ", ")
		if len(res.Pending) > 0 {
			s += " (pending verdict: " + strings.Join(res.Pending, ", ") + ")"
		}
		parts = append(parts, s)
	}
	if res.Error != "" {
		parts = append(parts, res.Error)
	}
	parts = append(parts, res.Errors...)
	if len(parts) == 0 {
		return res.Outcome
	}
	return res.Outcome + ": " + strings.Join(parts, "; ")
}
