package checks

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/vooon/pathosd/internal/config"
)

type HTTPChecker struct {
	cfg    config.HTTPCheckConfig
	client *http.Client
}

func NewHTTPChecker(cfg *config.HTTPCheckConfig) *HTTPChecker {
	transport := &http.Transport{
		DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		TLSClientConfig: &tls.Config{},
	}
	if cfg.Proto == "https" && cfg.SSLHostname != nil && !*cfg.SSLHostname {
		transport.TLSClientConfig.InsecureSkipVerify = true
	}
	return &HTTPChecker{
		cfg: *cfg,
		client: &http.Client{
			Transport: transport,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

func (c *HTTPChecker) Type() string { return "http" }

func (c *HTTPChecker) Check(ctx context.Context) Result {
	start := time.Now()
	url := fmt.Sprintf("%s://%s:%d%s", c.cfg.Proto, c.cfg.Host, c.cfg.Port, c.cfg.URL)

	req, err := http.NewRequestWithContext(ctx, c.cfg.Method, url, nil)
	if err != nil {
		return Result{Duration: time.Since(start), Err: err, Detail: err.Error()}
	}
	for k, v := range c.cfg.Headers {
		req.Header.Set(k, v)
	}

	resp, err := c.client.Do(req)
	dur := time.Since(start)
	if err != nil {
		timedOut := ctx.Err() == context.DeadlineExceeded
		return Result{Duration: dur, Err: err, Detail: err.Error(), TimedOut: timedOut}
	}
	defer resp.Body.Close()

	codeOK := false
	for _, code := range c.cfg.ResponseCodes {
		if resp.StatusCode == code {
			codeOK = true
			break
		}
	}
	if !codeOK {
		return Result{Duration: dur, Detail: fmt.Sprintf("unexpected status %d (want %v)", resp.StatusCode, c.cfg.ResponseCodes)}
	}

	if c.cfg.ResponseText != "" {
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			return Result{Duration: dur, Err: err, Detail: "error reading body"}
		}
		if !strings.Contains(string(body), c.cfg.ResponseText) {
			return Result{Duration: dur, Detail: fmt.Sprintf("body missing %q", c.cfg.ResponseText)}
		}
	}

	return Result{Success: true, Duration: dur, Detail: fmt.Sprintf("HTTP %d OK", resp.StatusCode)}
}
