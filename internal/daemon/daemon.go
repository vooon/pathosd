package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
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
	"github.com/vooon/pathosd/internal/reload"
	"github.com/vooon/pathosd/internal/systemd"
	"github.com/vooon/pathosd/internal/telemetry"
)

// ConfigFile is where cfg came from, for hot reload: the path to re-read
// and the bytes that produced cfg, so a reload can tell an edited file
// from an untouched one.
type ConfigFile struct {
	Path string
	Raw  []byte
	// DebugForced means `run --debug` overrode logging.level; reloads keep it.
	DebugForced bool
}

// configPollInterval is how often the config file is compared with the
// running config to raise pathosd_config_file_changed.
const configPollInterval = 5 * time.Second

func Run(cfg *config.Config, file ConfigFile) error {
	app := fx.New(
		fx.Supply(cfg, file),
		fx.Provide(
			provideTelemetry,
			provideLogger,
			provideMetrics,
			provideBGPManager,
			provideSchedulers,
			providePolicyManager,
			provideHTTPServer,
			provideStatusReporter,
			provideReloader,
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
			registerReloadLifecycle,
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

// provideSchedulers builds one scheduler per check into a registry; a
// reload adds, replaces and removes schedulers in it later.
func provideSchedulers(cfg *config.Config, m *metrics.Metrics) (*checks.Registry, error) {
	reg := checks.NewRegistry()
	for i := range cfg.VIPs {
		v := &cfg.VIPs[i]
		scheds, err := reload.NewSchedulers(v, v.CheckList(), m, checks.NewChecker)
		if err != nil {
			return nil, err
		}
		for _, s := range scheds {
			reg.Add(s)
		}
	}
	return reg, nil
}

func providePolicyManager(cfg *config.Config, m *metrics.Metrics, bgpMgr *bgp.Manager) *policy.Manager {
	return policy.NewManager(cfg.VIPs, m, bgpMgr)
}

func provideHTTPServer(cfg *config.Config, m *metrics.Metrics, bgpMgr *bgp.Manager, pol *policy.Manager, reg *checks.Registry, rl *reload.Reloader) *http.Server {
	return httpapi.NewServer(httpapi.ServerDeps{
		Config:   cfg,
		Metrics:  m,
		BGP:      bgpMgr,
		Policy:   pol,
		Registry: reg,
		Reloader: rl,
	})
}

func provideReloader(cfg *config.Config, file ConfigFile, pol *policy.Manager, reg *checks.Registry, m *metrics.Metrics, bgpMgr *bgp.Manager, status *statusReporter) *reload.Reloader {
	rl := reload.New(file.Path, cfg, file.Raw, pol, reg, m, bgpMgr, status.Update)
	if file.DebugForced {
		rl.LockLogLevel()
	}
	status.SetConfigStatus(rl.Status)
	return rl
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

func registerSchedulersLifecycle(lc fx.Lifecycle, reg *checks.Registry, pol *policy.Manager) {
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			for _, vipScheds := range reg.Schedulers() {
				for _, sched := range vipScheds {
					sched.SetCallbacks(pol.OnHealthTransition, pol.OnCheckResult)
					slog.Info("scheduler started", "vip", sched.VIPName(), "check", sched.CheckName())
				}
			}
			reg.Start()
			return nil
		},
		// Wait for in-flight checks so no callback reaches BGP after it stops.
		OnStop: reg.Stop,
	})
}

// registerReloadLifecycle runs the file poller and the shadow-commit loop,
// and turns SIGHUP into a reload. Fx's own signal handling covers only
// SIGINT and SIGTERM.
func registerReloadLifecycle(lc fx.Lifecycle, rl *reload.Reloader) {
	var (
		cancel context.CancelFunc
		done   chan struct{}
		hup    chan os.Signal
	)
	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			ctx, c := context.WithCancel(context.Background())
			cancel = c
			done = make(chan struct{})
			hup = make(chan os.Signal, 1)
			signal.Notify(hup, syscall.SIGHUP)
			go func() {
				defer close(done)
				rl.Run(ctx, configPollInterval)
			}()
			go func() {
				for {
					select {
					case <-ctx.Done():
						return
					case <-hup:
						slog.Info("SIGHUP received, reloading config")
						rl.Reload(ctx)
					}
				}
			}()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			signal.Stop(hup)
			cancel()
			select {
			case <-done:
				return nil
			case <-ctx.Done():
				return ctx.Err()
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
