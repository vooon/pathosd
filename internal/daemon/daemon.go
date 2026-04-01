package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.uber.org/fx"

	"github.com/vooon/pathosd/internal/bgp"
	"github.com/vooon/pathosd/internal/checks"
	"github.com/vooon/pathosd/internal/config"
	"github.com/vooon/pathosd/internal/httpapi"
	"github.com/vooon/pathosd/internal/logging"
	"github.com/vooon/pathosd/internal/metrics"
	"github.com/vooon/pathosd/internal/policy"
)

type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

func Run(cfg *config.Config, info BuildInfo) {
	app := fx.New(
		fx.Supply(cfg, info),
		fx.Provide(
			provideLogger,
			provideMetrics,
			provideBGPManager,
			provideSchedulers,
			providePolicyManager,
			provideHTTPServer,
		),
		fx.Invoke(startDaemon),
		fx.StartTimeout(15*time.Second),
		fx.StopTimeout(30*time.Second),
	)
	app.Run()
}

func provideLogger(cfg *config.Config) *slog.Logger {
	return logging.Setup(cfg.Logging.Level, cfg.Logging.Format)
}

func provideMetrics(info BuildInfo) *metrics.Metrics {
	m := metrics.New()
	m.BuildInfo.WithLabelValues(info.Version, info.Commit, info.Date).Set(1)
	return m
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
		interval := v.CheckInterval.Duration
		timeout := v.CheckTimeout.Duration
		rise := 1
		if v.Rise != nil {
			rise = *v.Rise
		}
		fall := 3
		if v.Fall != nil {
			fall = *v.Fall
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

func provideHTTPServer(cfg *config.Config, m *metrics.Metrics, bgpMgr *bgp.Manager, pol *policy.Manager, scheds map[string]*checks.Scheduler, info BuildInfo) *httpapi.ServerDeps {
	return &httpapi.ServerDeps{
		Config:     cfg,
		Metrics:    m,
		BGP:        bgpMgr,
		Policy:     pol,
		Schedulers: scheds,
		Version:    info.Version,
		Commit:     info.Commit,
	}
}

type daemonDeps struct {
	fx.In
	Lifecycle  fx.Lifecycle
	Config     *config.Config
	Metrics    *metrics.Metrics
	BGP        *bgp.Manager
	Policy     *policy.Manager
	Schedulers map[string]*checks.Scheduler
	HTTPDeps   *httpapi.ServerDeps
	Info       BuildInfo
}

func startDaemon(deps daemonDeps) {
	var (
		bgpCtx    context.Context
		bgpCancel context.CancelFunc
		schedCtxs []context.CancelFunc
		httpSrv   = httpapi.NewServer(*deps.HTTPDeps)
	)

	deps.Lifecycle.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			slog.Info("starting pathosd", "version", deps.Info.Version, "commit", deps.Info.Commit)

			// Start BGP server.
			if err := deps.BGP.Start(ctx); err != nil {
				return fmt.Errorf("BGP start: %w", err)
			}

			// Add BGP peers.
			if err := deps.BGP.AddPeers(ctx); err != nil {
				return fmt.Errorf("BGP add peers: %w", err)
			}

			// Start BGP peer watcher.
			bgpCtx, bgpCancel = context.WithCancel(context.Background())
			var neighborAddrs []string
			for _, n := range deps.Config.BGP.Neighbors {
				neighborAddrs = append(neighborAddrs, n.Address)
			}
			go bgp.WatchPeerState(bgpCtx, deps.BGP.Server(), deps.Metrics, neighborAddrs)

			// Wire scheduler callbacks to policy manager and start schedulers.
			for _, sched := range deps.Schedulers {
				sched.SetCallbacks(deps.Policy.OnHealthTransition, deps.Policy.OnCheckResult)
				schedCtx, schedCancel := context.WithCancel(context.Background())
				schedCtxs = append(schedCtxs, schedCancel)
				go sched.Run(schedCtx)
				slog.Info("scheduler started", "vip", sched.VIPName())
			}

			// Start HTTP API.
			go httpapi.ListenAndServe(context.Background(), httpSrv)

			return nil
		},
		OnStop: func(ctx context.Context) error {
			slog.Info("stopping pathosd")

			// Stop HTTP.
			if err := httpSrv.Shutdown(ctx); err != nil {
				slog.Error("HTTP shutdown error", "error", err)
			}

			// Stop schedulers.
			for _, cancel := range schedCtxs {
				cancel()
			}

			// Stop BGP watcher.
			if bgpCancel != nil {
				bgpCancel()
			}

			// Stop BGP server — sessions drop, routes withdrawn.
			deps.BGP.Stop(ctx)

			return nil
		},
	})
}
