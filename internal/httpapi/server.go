package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/vooon/pathosd/internal/bgp"
	"github.com/vooon/pathosd/internal/checks"
	"github.com/vooon/pathosd/internal/config"
	"github.com/vooon/pathosd/internal/metrics"
	"github.com/vooon/pathosd/internal/model"
	"github.com/vooon/pathosd/internal/policy"
)

type ServerDeps struct {
	Config     *config.Config
	Metrics    *metrics.Metrics
	BGP        *bgp.Manager
	Policy     *policy.Manager
	Schedulers map[string]*checks.Scheduler
	Version    string
	Commit     string
}

func NewServer(deps ServerDeps) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealthz())
	mux.HandleFunc("GET /readyz", handleReadyz(deps.BGP, deps.Config))
	mux.HandleFunc("GET /status", handleStatus(deps))
	mux.Handle("GET /metrics", promhttp.HandlerFor(deps.Metrics.Registry, promhttp.HandlerOpts{}))
	mux.HandleFunc("POST /api/v1/vips/{name}/check", handleTriggerCheck(deps.Schedulers))
	return &http.Server{Addr: deps.Config.API.Listen, Handler: mux}
}

func handleHealthz() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

func handleReadyz(bgpMgr *bgp.Manager, cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		peers := bgpMgr.GetPeerStates(r.Context())
		allReady := true
		var unready []model.PeerStatus
		for _, p := range peers {
			if !p.Required {
				continue
			}
			if p.SessionState != "established" {
				allReady = false
				unready = append(unready, p)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if allReady {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{"status": "ready", "peers": peers})
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]interface{}{"status": "not_ready", "unready": unready, "peers": peers})
		}
	}
}

func handleStatus(deps ServerDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		status := model.DaemonStatus{
			RouterID: deps.Config.Router.RouterID,
			ASN:      deps.Config.Router.ASN,
			Version:  deps.Version,
			Commit:   deps.Commit,
			Peers:    deps.BGP.GetPeerStates(r.Context()),
			VIPs:     deps.Policy.GetVIPStatuses(),
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(status)
	}
}

func handleTriggerCheck(schedulers map[string]*checks.Scheduler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		sched, ok := schedulers[name]
		if !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"error": "VIP not found: " + name})
			return
		}
		result, err := sched.TriggerCheck(r.Context())
		if err != nil {
			slog.Error("ad-hoc check failed", "vip", name, "error", err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"vip": name, "result": result})
	}
}

func ListenAndServe(ctx context.Context, srv *http.Server) error {
	errCh := make(chan error, 1)
	go func() {
		slog.Info("HTTP API listening", "address", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()
	select {
	case <-ctx.Done():
		return srv.Shutdown(context.Background())
	case err := <-errCh:
		return err
	}
}
