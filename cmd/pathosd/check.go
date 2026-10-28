package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/vooon/pathosd/internal/checks"
	"github.com/vooon/pathosd/internal/config"
	"github.com/vooon/pathosd/internal/model"
)

// CheckCmd runs the configured health checks once, without BGP or the
// daemon, and reports the per-check results and the combined VIP health.
type CheckCmd struct {
	Config           string        `help:"Path to configuration file." short:"c" type:"existingfile" required:""`
	VIP              []string      `help:"Only these VIPs (repeatable). Default: all." short:"v" name:"vip"`
	Check            []string      `help:"Only checks with these names (repeatable). Others count as failed when combining." name:"check"`
	Format           string        `help:"Output format." enum:"text,json" default:"text"`
	Timeout          time.Duration `help:"Override every check's timeout (e.g. 5s)."`
	SkipLocalAddress bool          `help:"Run probes even when the VIP address is not configured locally (ignore require_local_address)."`
}

// checkReport is the JSON shape of one VIP in the output.
type checkReport struct {
	VIP     string              `json:"vip"`
	Prefix  string              `json:"prefix"`
	Policy  string              `json:"checks_policy"`
	Healthy bool                `json:"healthy"`
	Checks  []checkResultReport `json:"checks"`
}

type checkResultReport struct {
	Name     string             `json:"name"`
	Type     string             `json:"type"`
	Ran      bool               `json:"ran"`
	Success  bool               `json:"success"`
	Detail   string             `json:"detail,omitempty"`
	Duration time.Duration      `json:"duration"`
	TimedOut bool               `json:"timed_out,omitempty"`
	Details  model.CheckDetails `json:"details,omitempty"`
}

var errChecksFailed = errors.New("checks failed")

func (c *CheckCmd) Run() error {
	cfg, err := config.Load(c.Config)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	reports, err := c.execute(context.Background(), cfg)
	if err != nil {
		return err
	}
	if err := c.print(os.Stdout, reports); err != nil {
		return err
	}
	unhealthy := 0
	for _, r := range reports {
		if !r.Healthy {
			unhealthy++
		}
	}
	if unhealthy > 0 {
		return fmt.Errorf("%w: %d of %d VIPs unhealthy", errChecksFailed, unhealthy, len(reports))
	}
	return nil
}

// execute runs the selected checks of the selected VIPs concurrently and
// combines them with each VIP's checks_policy. A check that was not selected
// counts as unhealthy in the combination, like the API's ?check= trigger.
func (c *CheckCmd) execute(ctx context.Context, cfg *config.Config) ([]checkReport, error) {
	vips, err := selectVIPs(cfg, c.VIP)
	if err != nil {
		return nil, err
	}
	if err := c.verifyCheckNames(vips); err != nil {
		return nil, err
	}

	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		reports = make([]checkReport, len(vips))
	)
	for i, vip := range vips {
		list := vip.CheckList()
		reports[i] = checkReport{VIP: vip.Name, Prefix: vip.Prefix, Policy: vip.EffectiveChecksPolicy(), Checks: make([]checkResultReport, len(list))}
		rep := &reports[i]
		for j, cc := range list {
			rep.Checks[j] = checkResultReport{Name: cc.EffectiveName(), Type: cc.Type}
			if len(c.Check) > 0 && !slices.Contains(c.Check, cc.EffectiveName()) {
				rep.Checks[j].Detail = "not selected"
				continue
			}
			checker, err := c.build(vip, cc)
			if err != nil {
				return nil, fmt.Errorf("vip %s check %s: %w", vip.Name, cc.EffectiveName(), err)
			}
			timeout := cc.Timeout.Duration
			if c.Timeout > 0 {
				timeout = c.Timeout
			}
			wg.Add(1)
			go func(i, j int, checker checks.Checker, timeout time.Duration) {
				defer wg.Done()
				cctx, cancel := context.WithTimeout(ctx, timeout)
				defer cancel()
				res := checker.Check(cctx)
				mu.Lock()
				defer mu.Unlock()
				r := &reports[i].Checks[j]
				r.Ran = true
				r.Success = res.Success
				r.Detail = res.Detail
				if r.Detail == "" && res.Err != nil {
					r.Detail = res.Err.Error()
				}
				r.Duration = res.Duration
				r.TimedOut = res.TimedOut
				r.Details = res.Details
			}(i, j, checker, timeout)
		}
	}
	wg.Wait()

	for i, vip := range vips {
		rep := &reports[i]
		names := make([]string, len(rep.Checks))
		healthy := make(map[string]bool, len(rep.Checks))
		for j, r := range rep.Checks {
			names[j] = r.Name
			healthy[r.Name] = r.Ran && r.Success
		}
		eval, err := config.CompileChecksPolicy(vip.EffectiveChecksPolicy(), names)
		if err != nil {
			return nil, fmt.Errorf("vip %s: %w", vip.Name, err)
		}
		rep.Healthy = eval.Eval(healthy)
	}
	return reports, nil
}

func (c *CheckCmd) build(vip *config.VIPConfig, cc *config.CheckConfig) (checks.Checker, error) {
	checker, err := checks.NewChecker(cc)
	if err != nil {
		return nil, err
	}
	if vip.RequireLocalAddress && !c.SkipLocalAddress {
		guard, err := checks.NewLocalAddressGuard(vip.Name, vip.Prefix, vip.LocalInterface, func(bool) {})
		if err != nil {
			return nil, err
		}
		checker = guard.Wrap(checker)
	}
	return checker, nil
}

func (c *CheckCmd) verifyCheckNames(vips []*config.VIPConfig) error {
	for _, want := range c.Check {
		found := false
		for _, vip := range vips {
			for _, cc := range vip.CheckList() {
				if cc.EffectiveName() == want {
					found = true
				}
			}
		}
		if !found {
			return fmt.Errorf("no check named %q in the selected VIPs", want)
		}
	}
	return nil
}

func selectVIPs(cfg *config.Config, names []string) ([]*config.VIPConfig, error) {
	all := make([]*config.VIPConfig, len(cfg.VIPs))
	for i := range cfg.VIPs {
		all[i] = &cfg.VIPs[i]
	}
	if len(names) == 0 {
		return all, nil
	}
	var out []*config.VIPConfig
	for _, name := range names {
		idx := slices.IndexFunc(all, func(v *config.VIPConfig) bool { return v.Name == name })
		if idx < 0 {
			return nil, fmt.Errorf("no VIP named %q in config", name)
		}
		out = append(out, all[idx])
	}
	return out, nil
}

func (c *CheckCmd) print(w io.Writer, reports []checkReport) error {
	if c.Format == "json" {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(reports)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for i, r := range reports {
		if i > 0 {
			_, _ = fmt.Fprintln(tw)
		}
		state := "healthy"
		if !r.Healthy {
			state = "unhealthy"
		}
		_, _ = fmt.Fprintf(tw, "VIP %s (%s): %s [%s]\n", r.VIP, r.Prefix, state, r.Policy)
		for _, cr := range r.Checks {
			mark := "OK"
			switch {
			case !cr.Ran:
				mark = "SKIP"
			case !cr.Success && cr.TimedOut:
				mark = "TIMEOUT"
			case !cr.Success:
				mark = "FAIL"
			}
			dur := ""
			if cr.Ran {
				dur = cr.Duration.Round(time.Microsecond).String()
			}
			_, _ = fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n", mark, cr.Name, cr.Type, dur, strings.TrimSpace(cr.Detail))
		}
	}
	return tw.Flush()
}
