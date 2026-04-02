package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/prometheus/common/version"
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"github.com/vooon/pathosd/internal/bgp"
	"github.com/vooon/pathosd/internal/checks"
	"github.com/vooon/pathosd/internal/config"
	"github.com/vooon/pathosd/internal/httpapi"
	"github.com/vooon/pathosd/internal/logging"
	"github.com/vooon/pathosd/internal/metrics"
	"github.com/vooon/pathosd/internal/policy"
)

func Run(cfg *config.Config) error {
	app := fx.New(
		fx.Supply(cfg),
		fx.Provide(
			provideLogger,
			provideMetrics,
			provideBGPManager,
			provideSchedulers,
			providePolicyManager,
			provideHTTPServer,
		),
		fx.WithLogger(func(logger *slog.Logger) fxevent.Logger {
			fxlog := &fxevent.SlogLogger{Logger: logger.With("component", "fx")}
			fxlog.UseLogLevel(slog.LevelDebug)
			fxlog.UseErrorLevel(slog.LevelError)
			return fxlog
		}),
		fx.Invoke(
			ensureLoggerInitialized,
			registerProcessLifecycle,
			registerBGPLifecycle,
			registerPeerWatcherLifecycle,
			registerSchedulersLifecycle,
			registerHTTPServerLifecycle,
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

func provideLogger(cfg *config.Config) *slog.Logger {
	return logging.Setup(cfg.Logging.Level, cfg.Logging.Format)
}

func provideMetrics(cfg *config.Config) *metrics.Metrics {
	// Compute histogram buckets from the maximum check timeout across all VIPs.
	var maxTimeout time.Duration
	for i := range cfg.VIPs {
		if t := cfg.VIPs[i].Check.Timeout.Duration; t > maxTimeout {
			maxTimeout = t
		}
	}
	buckets := metrics.GenerateCheckBuckets(maxTimeout)
	return metrics.New(buckets)
}

func provideBGPManager(cfg *config.Config) *bgp.Manager {
	return bgp.NewManager(cfg)
}

type SchedulersResult struct {
	fx.Out
	Schedulers map[string]*checks.Scheduler
}

func provideSchedulers(cfg *config.Config) (SchedulersResult, error) {
	scheds := make(map[string]*checks.Scheduler, len(cfg.VIPs))
	for i := range cfg.VIPs {
		v := &cfg.VIPs[i]
		checker, err := checks.NewChecker(&v.Check)
		if err != nil {
			return SchedulersResult{}, fmt.Errorf("creating checker for VIP %q: %w", v.Name, err)
		}
		interval := v.Check.Interval.Duration
		timeout := v.Check.Timeout.Duration
		rise := 1
		if v.Check.Rise != nil {
			rise = *v.Check.Rise
		}
		fall := 3
		if v.Check.Fall != nil {
			fall = *v.Check.Fall
		}
		sched := checks.NewScheduler(checks.SchedulerConfig{
			VIPName:  v.Name,
			Checker:  checker,
			Interval: interval,
			Timeout:  timeout,
			Rise:     rise,
			Fall:     fall,
		})
		scheds[v.Name] = sched
	}
	return SchedulersResult{Schedulers: scheds}, nil
}

func providePolicyManager(cfg *config.Config, m *metrics.Metrics, bgpMgr *bgp.Manager) *policy.Manager {
	return policy.NewManager(cfg.VIPs, m, bgpMgr)
}

func provideHTTPServer(cfg *config.Config, m *metrics.Metrics, bgpMgr *bgp.Manager, pol *policy.Manager, scheds map[string]*checks.Scheduler) *http.Server {
	return httpapi.NewServer(httpapi.ServerDeps{
		Config:     cfg,
		Metrics:    m,
		BGP:        bgpMgr,
		Policy:     pol,
		Schedulers: scheds,
	})
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

func registerPeerWatcherLifecycle(lc fx.Lifecycle, cfg *config.Config, m *metrics.Metrics, bgpMgr *bgp.Manager) {
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
			go bgp.WatchPeerState(watchCtx, bgpMgr.Server(), m, neighborNames)

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

func registerSchedulersLifecycle(lc fx.Lifecycle, scheds map[string]*checks.Scheduler, pol *policy.Manager) {
	var schedCancels []context.CancelFunc

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			schedCancels = make([]context.CancelFunc, 0, len(scheds))
			for _, sched := range scheds {
				sched.SetCallbacks(pol.OnHealthTransition, pol.OnCheckResult)
				schedCtx, cancel := context.WithCancel(context.Background())
				schedCancels = append(schedCancels, cancel)
				go sched.Run(schedCtx)
				slog.Info("scheduler started", "vip", sched.VIPName())
			}
			return nil
		},
		OnStop: func(ctx context.Context) error {
			for _, cancel := range schedCancels {
				cancel()
			}
			return nil
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
