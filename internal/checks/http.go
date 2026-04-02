package checks

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/itchyny/gojq"
	"github.com/vooon/pathosd/internal/config"
)

type HTTPChecker struct {
	cfg     config.HTTPCheckConfig
	client  *http.Client
	reBody  *regexp.Regexp // compiled ResponseRegex, nil if not set
	jqQuery *gojq.Query    // compiled ResponseJQ, nil if not set
}

func NewHTTPChecker(cfg *config.HTTPCheckConfig) (*HTTPChecker, error) {
	transport := &http.Transport{
		DialContext:     (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		TLSClientConfig: &tls.Config{},
	}
	if cfg.TLSInsecure {
		transport.TLSClientConfig.InsecureSkipVerify = true
	}
	// TODO: support cfg.TLSCACert

	c := &HTTPChecker{
		cfg: *cfg,
		client: &http.Client{
			Transport: transport,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}

	if cfg.ResponseRegex != "" {
		re, err := regexp.Compile(cfg.ResponseRegex)
		if err != nil {
			return nil, fmt.Errorf("invalid response_regex: %w", err)
		}
		c.reBody = re
	}

	if cfg.ResponseJQ != "" {
		q, err := gojq.Parse(cfg.ResponseJQ)
		if err != nil {
			return nil, fmt.Errorf("invalid response_jq: %w", err)
		}
		c.jqQuery = q
	}

	return c, nil
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

	if !slices.Contains(c.cfg.ResponseCodes, resp.StatusCode) {
		return Result{Duration: dur, Detail: fmt.Sprintf("unexpected status %d (want %v)", resp.StatusCode, c.cfg.ResponseCodes)}
	}

	if c.cfg.ResponseText != "" || c.reBody != nil || c.jqQuery != nil {
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			return Result{Duration: dur, Err: err, Detail: "error reading body"}
		}

		if c.cfg.ResponseText != "" {
			if !strings.Contains(string(body), c.cfg.ResponseText) {
				return Result{Duration: dur, Detail: fmt.Sprintf("body missing %q", c.cfg.ResponseText)}
			}
		}

		if c.reBody != nil {
			if !c.reBody.Match(body) {
				return Result{Duration: dur, Detail: fmt.Sprintf("body does not match regex %q", c.cfg.ResponseRegex)}
			}
		}

		if c.jqQuery != nil {
			var parsed interface{}
			if err := json.Unmarshal(body, &parsed); err != nil {
				return Result{Duration: dur, Err: err, Detail: fmt.Sprintf("response is not valid JSON: %v", err)}
			}
			iter := c.jqQuery.Run(parsed)
			v, ok := iter.Next()
			if !ok {
				return Result{Duration: dur, Detail: "jq expression produced no output"}
			}
			if err, isErr := v.(error); isErr {
				return Result{Duration: dur, Err: err, Detail: fmt.Sprintf("jq error: %v", err)}
			}
			if v != true {
				return Result{Duration: dur, Detail: fmt.Sprintf("jq expression is not true: %v", v)}
			}
		}
	}

	return Result{Success: true, Duration: dur, Detail: fmt.Sprintf("HTTP %d OK", resp.StatusCode)}
}
