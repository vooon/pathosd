package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/common/version"
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"github.com/vooon/pathosd/internal/bgp"
	"github.com/vooon/pathosd/internal/caps"
	"github.com/vooon/pathosd/internal/checks"
	"github.com/vooon/pathosd/internal/config"
	"github.com/vooon/pathosd/internal/httpapi"
	"github.com/vooon/pathosd/internal/logging"
	"github.com/vooon/pathosd/internal/metrics"
	"github.com/vooon/pathosd/internal/policy"
	"github.com/vooon/pathosd/internal/systemd"
	"github.com/vooon/pathosd/internal/telemetry"
)

func Run(cfg *config.Config) error {
	app := fx.New(
		fx.Supply(cfg),
		fx.Provide(
			provideTelemetry,
			provideLogger,
			provideMetrics,
			provideBGPManager,
			provideSchedulers,
			providePolicyManager,
			provideHTTPServer,
			provideStatusReporter,
		),
		fx.WithLogger(func(logger *slog.Logger) fxevent.Logger {
			fxlog := &fxevent.SlogLogger{Logger: logger.With("component", "fx")}
			fxlog.UseLogLevel(slog.LevelDebug)
			fxlog.UseErrorLevel(slog.LevelError)
			return fxlog
		}),
		fx.Invoke(
			ensureLoggerInitialized,
			raiseCapabilities,
			registerProcessLifecycle,
			registerBGPLifecycle,
			registerPeerWatcherLifecycle,
			registerSchedulersLifecycle,
			registerHTTPServerLifecycle,
			registerSystemdLifecycle,
		),
		fx.StartTimeout(15*time.Second),
		fx.StopTimeout(30*time.Second),
	)

	if err := app.Err(); err != nil {
		return err
	}

	startCtx, cancelStart := context.WithTimeout(context.Background(), app.StartTimeout())
	defer cancelStart()
	if err := app.Start(startCtx); err != nil {
		return fmt.Errorf("starting daemon: %w", err)
	}

	signal := <-app.Wait()
	if signal.Signal != nil {
		slog.Info("shutdown signal received", "signal", signal.Signal.String())
	} else {
		slog.Info("shutdown requested", "exit_code", signal.ExitCode)
	}

	stopCtx, cancelStop := context.WithTimeout(context.Background(), app.StopTimeout())
	defer cancelStop()
	if err := app.Stop(stopCtx); err != nil {
		return fmt.Errorf("stopping daemon: %w", err)
	}
	if signal.ExitCode != 0 {
		return fmt.Errorf("shutdown requested with non-zero exit code %d", signal.ExitCode)
	}
	return nil
}

func provideTelemetry(cfg *config.Config, m *metrics.Metrics, lc fx.Lifecycle) (*telemetry.Provider, error) {
	prov, err := telemetry.Setup(context.Background(), cfg.OTel, m.Registry, version.Version)
	if err != nil {
		return nil, fmt.Errorf("setting up OTEL telemetry: %w", err)
	}
	lc.Append(fx.Hook{
		OnStop: func(ctx context.Context) error {
			return prov.Shutdown(ctx)
		},
	})
	return prov, nil
}

func provideLogger(cfg *config.Config, tel *telemetry.Provider) *slog.Logger {
	// journald stamps every record itself; skip ours unless configured.
	timestamps := !systemd.StderrIsJournal()
	if cfg.Logging.Timestamps != nil {
		timestamps = *cfg.Logging.Timestamps
	}
	if h := tel.LogHandler(); h != nil {
		otelH := h
		if cfg.Logging.OTelLevel != "" {
			var otelLvl slog.Level
			_ = otelLvl.UnmarshalText([]byte(cfg.Logging.OTelLevel))
			otelH = logging.NewLevelFilter(otelLvl, h)
		}
		return logging.Setup(cfg.Logging.Level, cfg.Logging.Format, timestamps, otelH)
	}
	return logging.Setup(cfg.Logging.Level, cfg.Logging.Format, timestamps)
}

func provideMetrics(cfg *config.Config) *metrics.Metrics {
	// Compute histogram buckets from the maximum check timeout across all VIPs.
	var maxTimeout time.Duration
	for i := range cfg.VIPs {
		for _, c := range cfg.VIPs[i].CheckList() {
			if c.Timeout != nil && c.Timeout.Duration > maxTimeout {
				maxTimeout = c.Timeout.Duration
			}
		}
	}
	buckets := metrics.GenerateCheckBuckets(maxTimeout)
	return metrics.New(buckets)
}

func provideBGPManager(cfg *config.Config, m *metrics.Metrics) (*bgp.Manager, error) {
	bgpMgr := bgp.NewManager(cfg, m)
	if err := bgpMgr.RegisterMetrics(m.Registry); err != nil {
		return nil, fmt.Errorf("registering GoBGP metrics: %w", err)
	}
	return bgpMgr, nil
}

type SchedulersResult struct {
	fx.Out
	// Schedulers holds one scheduler per check, keyed by VIP name in config order.
	Schedulers map[string][]*checks.Scheduler
}

func provideSchedulers(cfg *config.Config, m *metrics.Metrics) (SchedulersResult, error) {
	scheds := make(map[string][]*checks.Scheduler, len(cfg.VIPs))
	for i := range cfg.VIPs {
		v := &cfg.VIPs[i]
		var guard *checks.LocalAddressGuard
		if v.RequireLocalAddress {
			gauge := m.VIPLocalAddress.WithLabelValues(v.Name, v.Prefix)
			g, err := checks.NewLocalAddressGuard(v.Name, v.Prefix, v.LocalInterface, func(assigned bool) {
				if assigned {
					gauge.Set(1)
				} else {
					gauge.Set(0)
				}
			})
			if err != nil {
				return SchedulersResult{}, fmt.Errorf("creating local address guard for VIP %q: %w", v.Name, err)
			}
			guard = g
		}
		for _, c := range v.CheckList() {
			checker, err := checks.NewChecker(c)
			if err != nil {
				return SchedulersResult{}, fmt.Errorf("creating checker %q for VIP %q: %w", c.EffectiveName(), v.Name, err)
			}
			if guard != nil {
				checker = guard.Wrap(checker)
			}
			interval, timeout := time.Second, 100*time.Millisecond
			if c.Interval != nil {
				interval = c.Interval.Duration
			}
			if c.Timeout != nil {
				timeout = c.Timeout.Duration
			}
			rise := 1
			if c.Rise != nil {
				rise = *c.Rise
			}
			fall := 3
			if c.Fall != nil {
				fall = *c.Fall
			}
			scheds[v.Name] = append(scheds[v.Name], checks.NewScheduler(checks.SchedulerConfig{
				VIPName:   v.Name,
				CheckName: c.EffectiveName(),
				Checker:   checker,
				Interval:  interval,
				Timeout:   timeout,
				Rise:      rise,
				Fall:      fall,
			}))
		}
	}
	return SchedulersResult{Schedulers: scheds}, nil
}

func providePolicyManager(cfg *config.Config, m *metrics.Metrics, bgpMgr *bgp.Manager) *policy.Manager {
	return policy.NewManager(cfg.VIPs, m, bgpMgr)
}

func provideHTTPServer(cfg *config.Config, m *metrics.Metrics, bgpMgr *bgp.Manager, pol *policy.Manager, scheds map[string][]*checks.Scheduler) *http.Server {
	return httpapi.NewServer(httpapi.ServerDeps{
		Config:     cfg,
		Metrics:    m,
		BGP:        bgpMgr,
		Policy:     pol,
		Schedulers: scheds,
	})
}

// raiseCapabilities makes file capabilities granted to the binary effective
// before any socket is opened. Failure is logged, not fatal: binding port
// 179 or the raw ICMP socket then reports its own permission error.
func raiseCapabilities(logger *slog.Logger) {
	raised, err := caps.RaiseEffective()
	switch {
	case err != nil:
		logger.Warn("could not raise file capabilities", "error", err)
	case raised:
		logger.Info("raised file capabilities to effective")
	}
}

func ensureLoggerInitialized(_ *slog.Logger) {}

func registerProcessLifecycle(lc fx.Lifecycle) {
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			slog.Info("starting pathosd", "version", version.Version, "revision", version.GetRevision())
			return nil
		},
		OnStop: func(ctx context.Context) error {
			slog.Info("stopping pathosd")
			return nil
		},
	})
}

func registerBGPLifecycle(lc fx.Lifecycle, bgpMgr *bgp.Manager) {
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			if err := bgpMgr.Start(ctx); err != nil {
				return fmt.Errorf("BGP start: %w", err)
			}
			if err := bgpMgr.AddPeers(ctx); err != nil {
				return fmt.Errorf("BGP add peers: %w", err)
			}
			return nil
		},
		OnStop: func(ctx context.Context) error {
			bgpMgr.Stop(ctx)
			return nil
		},
	})
}

func registerPeerWatcherLifecycle(lc fx.Lifecycle, cfg *config.Config, m *metrics.Metrics, bgpMgr *bgp.Manager, status *statusReporter) {
	var (
		bgpCancel context.CancelFunc
	)

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			neighborNames := make(map[string]string, len(cfg.BGP.Neighbors))
			for _, n := range cfg.BGP.Neighbors {
				neighborNames[n.Address] = n.Name
			}
			watchCtx, cancel := context.WithCancel(context.Background())
			bgpCancel = cancel
			go bgp.WatchPeerState(watchCtx, bgpMgr.Server(), m, neighborNames, status.Update)

			return nil
		},
		OnStop: func(ctx context.Context) error {
			if bgpCancel != nil {
				bgpCancel()
			}
			return nil
		},
	})
}

func registerSchedulersLifecycle(lc fx.Lifecycle, scheds map[string][]*checks.Scheduler, pol *policy.Manager) {
	var (
		schedCancels []context.CancelFunc
		schedWG      sync.WaitGroup
	)

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			schedCancels = make([]context.CancelFunc, 0, len(scheds))
			for _, vipScheds := range scheds {
				for _, sched := range vipScheds {
					sched.SetCallbacks(pol.OnHealthTransition, pol.OnCheckResult)
					schedCtx, cancel := context.WithCancel(context.Background())
					schedCancels = append(schedCancels, cancel)
					schedWG.Go(func() { sched.Run(schedCtx) })
					slog.Info("scheduler started", "vip", sched.VIPName(), "check", sched.CheckName())
				}
			}
			return nil
		},
		OnStop: func(ctx context.Context) error {
			for _, cancel := range schedCancels {
				cancel()
			}
			// Wait for in-flight checks so no callback reaches BGP after it stops.
			done := make(chan struct{})
			go func() {
				schedWG.Wait()
				close(done)
			}()
			select {
			case <-done:
				return nil
			case <-ctx.Done():
				return fmt.Errorf("waiting for schedulers to stop: %w", ctx.Err())
			}
		},
	})
}

func registerHTTPServerLifecycle(lc fx.Lifecycle, srv *http.Server, shutdowner fx.Shutdowner) {
	var (
		ln        net.Listener
		serveErrs chan error
	)

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			listener, err := net.Listen("tcp", srv.Addr)
			if err != nil {
				return fmt.Errorf("starting HTTP API listener on %q: %w", srv.Addr, err)
			}
			ln = listener
			serveErrs = make(chan error, 1)
			go func() {
				defer close(serveErrs)
				slog.Info("HTTP API listening", "address", srv.Addr)
				if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
					serveErrs <- err
					slog.Error("HTTP API server failed", "error", err)
					if shutdownErr := shutdowner.Shutdown(); shutdownErr != nil {
						slog.Error("failed to trigger daemon shutdown after HTTP error", "error", shutdownErr)
					}
				}
			}()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			if err := srv.Shutdown(ctx); err != nil {
				return fmt.Errorf("stopping HTTP API: %w", err)
			}
			if serveErrs == nil {
				return nil
			}
			select {
			case err, ok := <-serveErrs:
				if ok && err != nil {
					return fmt.Errorf("HTTP API serve loop: %w", err)
				}
			case <-ctx.Done():
				return fmt.Errorf("waiting for HTTP API shutdown: %w", ctx.Err())
			}
			return nil
		},
	})
}

// registerSystemdLifecycle is appended last so READY=1 goes out after every
// other component started, and STOPPING=1 (OnStop hooks run in reverse) is
// the first thing sent on shutdown. Without a notify socket it does nothing.
func registerSystemdLifecycle(lc fx.Lifecycle, pol *policy.Manager, status *statusReporter) {
	pol.SetTransitionListener(status.Update)
	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			if !systemd.NotifyAvailable() {
				return nil
			}
			status.Update()
			if systemd.Ready() {
				slog.Debug("systemd notified: ready")
			}
			return nil
		},
		OnStop: func(context.Context) error {
			systemd.Stopping()
			return nil
		},
	})
}
